/*
Copyright (c) 2026 Chris

Licensed under the MIT License. See the LICENSE file in the project root for
the full license text.
*/

package controller

import (
	"context"
	"fmt"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	dnsv1alpha1 "github.com/charg/technitium-operator/api/v1alpha1"
	"github.com/charg/technitium-operator/internal/technitium"
)

// defaultUpgradeStallTimeout is how long the node being upgraded may fail its
// gate before the rollout is reported as Stalled.
const defaultUpgradeStallTimeout = 10 * time.Minute

// Reasons of the Upgrading condition (and of Degraded while it is stalled or
// refused).
const (
	reasonRollingOut      = "RollingOut"
	reasonUpToDate        = "UpToDate"
	reasonUpgradeStalled  = "Stalled"
	reasonDowngradeBlock  = "DowngradeBlocked"
	degradedUpgradeStall  = "UpgradeStalled"
	connectedClusterState = "Connected"
)

// dnsServerContainerName is the Technitium container in the pod template.
const dnsServerContainerName = "dns-server"

// rolloutState is what reconcileRollout reports to updateStatus.
type rolloutState struct {
	// status, reason and message are the Upgrading condition.
	status  metav1.ConditionStatus
	reason  string
	message string
	// bookmark is the node being waited on; nil when no rollout is running.
	bookmark *dnsv1alpha1.TechnitiumClusterRolloutStatus
	// degradedReason, when set, also marks the Degraded condition True.
	degradedReason  string
	degradedMessage string
}

// downgradeBlock describes a spec.image change that was refused.
type downgradeBlock struct {
	// image is the image still deployed, which the StatefulSet keeps.
	image     string
	deployed  technitium.Version
	requested technitium.Version
}

func (d *downgradeBlock) message() string {
	return fmt.Sprintf("Refusing to downgrade Technitium from %s to %s: a newer server may have written configuration "+
		"on the data volume that an older one cannot read. The deployed image %s is kept; set the annotation "+
		"%s=\"true\" on the TechnitiumCluster to allow it",
		d.deployed, d.requested, d.image, dnsv1alpha1.AllowDowngradeAnnotation)
}

// templateImage is the Technitium image in a StatefulSet's pod template.
func templateImage(sts *appsv1.StatefulSet) string {
	for _, c := range sts.Spec.Template.Spec.Containers {
		if c.Name == dnsServerContainerName {
			return c.Image
		}
	}
	return ""
}

// checkDowngrade reports whether tc.spec.image would move the deployed
// version backwards without the opt-in annotation. The deployed version is
// the one in the existing StatefulSet's pod template, falling back to
// status.version when that image carries no parseable tag. A version that
// cannot be parsed on either side means no guard.
func checkDowngrade(tc *dnsv1alpha1.TechnitiumCluster, existing *appsv1.StatefulSet) *downgradeBlock {
	if tc.Annotations[dnsv1alpha1.AllowDowngradeAnnotation] == "true" {
		return nil
	}
	requested, ok := technitium.ParseImageVersion(tc.Spec.Image)
	if !ok {
		return nil
	}
	image := templateImage(existing)
	if image == "" {
		return nil
	}
	deployed, ok := technitium.ParseImageVersion(image)
	if !ok {
		v, err := technitium.ParseVersion(tc.Status.Version)
		if err != nil {
			return nil
		}
		deployed = v
	}
	if requested.Compare(deployed) >= 0 {
		return nil
	}
	return &downgradeBlock{image: image, deployed: deployed, requested: requested}
}

func (r *TechnitiumClusterReconciler) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

func (r *TechnitiumClusterReconciler) stallTimeout() time.Duration {
	if r.StallTimeout > 0 {
		return r.StallTimeout
	}
	return defaultUpgradeStallTimeout
}

// rolloutGate decides whether a node has finished upgrading. Everything it
// reads is fetched at most once per pass.
type rolloutGate struct {
	r        *TechnitiumClusterReconciler
	tc       *dnsv1alpha1.TechnitiumCluster
	username string
	password string
	pods     map[int32]clusterPodInfo
	revision string
	target   technitium.Version
	// checkVersion is false when the target image has no parseable version.
	checkVersion bool
	replicas     int32

	primaryRead  bool
	primaryState *technitium.ClusterState
	primaryErr   error
}

func (g *rolloutGate) primary(ctx context.Context) (*technitium.ClusterState, error) {
	if g.primaryRead {
		return g.primaryState, g.primaryErr
	}
	g.primaryRead = true
	c, err := g.r.nodeClientFactory()(nodeEndpoint(g.tc.Name, primaryOrdinal, g.r.OperatorNamespace), g.username, g.password)
	if err != nil {
		g.primaryErr = err
		return nil, err
	}
	g.primaryState, g.primaryErr = c.GetClusterState(ctx)
	return g.primaryState, g.primaryErr
}

// check reports whether the node at ordinal passes the gate: its pod is on
// the update revision and Ready, the node's own API reports the target
// version, and (for a Secondary) the Primary lists it as Connected. When it
// does not, why says which condition failed.
//
// The version read is the node's own top-level one rather than a per-member
// version: mid-rollout the Primary may still run a release that does not
// report per-member versions.
func (g *rolloutGate) check(ctx context.Context, ordinal int32) (ok bool, why string) {
	name := fmt.Sprintf("%s-%d", g.tc.Name, ordinal)
	pod, found := g.pods[ordinal]
	switch {
	case !found:
		return false, "its pod does not exist yet"
	case pod.revision != g.revision:
		return false, "its pod has not been recreated on the new revision"
	case !pod.ready:
		return false, "its pod is not Ready"
	}

	if g.checkVersion {
		node, err := g.r.nodeClientFactory()(nodeEndpoint(g.tc.Name, ordinal, g.r.OperatorNamespace), g.username, g.password)
		if err != nil {
			return false, fmt.Sprintf("building a client failed: %v", err)
		}
		state, err := node.GetClusterState(ctx)
		if err != nil {
			return false, fmt.Sprintf("its API is not reachable: %v", err)
		}
		if v, err := technitium.ParseVersion(state.Version); err != nil || v != g.target {
			return false, fmt.Sprintf("it reports version %q, not %s", state.Version, g.target)
		}
	}

	if ordinal != primaryOrdinal && g.replicas > 1 {
		ps, err := g.primary(ctx)
		if err != nil {
			return false, fmt.Sprintf("the Primary's cluster state is not readable: %v", err)
		}
		// Without a cluster there is no membership to rejoin.
		if ps.ClusterInitialized {
			member := findClusterMember(ps.Nodes, name)
			if member == nil || member.State != connectedClusterState {
				return false, "it is not Connected in the Primary's cluster state"
			}
		}
	}
	return true, ""
}

// reconcileRollout walks the StatefulSet's partition so Technitium nodes
// upgrade one at a time, Secondaries from the highest ordinal down and the
// Primary (ordinal 0) last. The next ordinal is released only once every node
// released so far passes the gate. It reports the Upgrading condition, and
// stops releasing nodes, while still reporting, when the node in flight stays
// stuck past the stall timeout.
func (r *TechnitiumClusterReconciler) reconcileRollout(ctx context.Context, tc *dnsv1alpha1.TechnitiumCluster, out statefulSetOutcome) (rolloutState, error) {
	state, err := r.walkRollout(ctx, tc, out)
	if out.downgrade != nil {
		state.degradedReason = reasonDowngradeBlock
		state.degradedMessage = out.downgrade.message()
		if state.status == metav1.ConditionFalse {
			state.reason = reasonDowngradeBlock
			state.message = state.degradedMessage
		}
	}
	return state, err
}

func (r *TechnitiumClusterReconciler) walkRollout(ctx context.Context, tc *dnsv1alpha1.TechnitiumCluster, out statefulSetOutcome) (rolloutState, error) {
	log := logf.FromContext(ctx)
	sts := out.sts

	replicas := int32(1)
	if sts.Spec.Replicas != nil {
		replicas = *sts.Spec.Replicas
	}
	partition := int32(0)
	if ru := sts.Spec.UpdateStrategy.RollingUpdate; ru != nil && ru.Partition != nil {
		partition = *ru.Partition
	}

	image := templateImage(sts)
	targetVersion, checkVersion := technitium.ParseImageVersion(image)
	targetLabel := image
	if checkVersion {
		targetLabel = targetVersion.String()
	}

	idle := rolloutState{
		status:  metav1.ConditionFalse,
		reason:  reasonUpToDate,
		message: fmt.Sprintf("Every node runs %s", targetLabel),
	}

	cond := meta.FindStatusCondition(tc.Status.Conditions, dnsv1alpha1.TechnitiumClusterConditionUpgrading)
	wasUpgrading := cond != nil && cond.Status == metav1.ConditionTrue
	revisionsDiffer := sts.Status.CurrentRevision != "" && sts.Status.UpdateRevision != "" &&
		sts.Status.CurrentRevision != sts.Status.UpdateRevision
	if !out.templateChanged && partition == 0 && !wasUpgrading && !revisionsDiffer {
		return idle, nil
	}

	rolling := func(format string, args ...any) rolloutState {
		return rolloutState{
			status:   metav1.ConditionTrue,
			reason:   reasonRollingOut,
			message:  fmt.Sprintf(format, args...),
			bookmark: tc.Status.Rollout,
		}
	}

	// The partition was just parked (or the controller has not yet observed the
	// new template): sts.Status describes the previous revision, so wait for
	// the next pass.
	if out.templateChanged {
		return rolling("Starting upgrade to %s", targetLabel), nil
	}
	if sts.Status.ObservedGeneration < sts.Generation || sts.Status.UpdateRevision == "" {
		return rolling("Waiting for the StatefulSet controller to observe the upgrade to %s", targetLabel), nil
	}

	username, password, err := r.adminCredentials(ctx, tc)
	if err != nil {
		return rolloutState{}, err
	}
	pods, err := r.resolveClusterPods(ctx, tc, replicas)
	if err != nil {
		return rolloutState{}, err
	}
	gate := &rolloutGate{
		r: r, tc: tc, username: username, password: password, pods: pods,
		revision: sts.Status.UpdateRevision, target: targetVersion, checkVersion: checkVersion, replicas: replicas,
	}

	// Ordinals below the partition have not been released yet. Every released
	// one must pass the gate before the next is.
	partition = min(partition, replicas)
	gated := make([]bool, replicas)
	reasons := make([]string, replicas)
	updated := 0
	for i := partition; i < replicas; i++ {
		gated[i], reasons[i] = gate.check(ctx, i)
		if gated[i] {
			updated++
		}
	}
	pending := int32(-1)
	for i := partition; i < replicas; i++ {
		if !gated[i] {
			pending = i
			break
		}
	}

	release := func(to int32) error {
		before := sts.DeepCopy()
		sts.Spec.UpdateStrategy.RollingUpdate.Partition = &to
		if err := r.Patch(ctx, sts, client.MergeFrom(before)); err != nil {
			return fmt.Errorf("lowering partition of StatefulSet %s to %d: %w", sts.Name, to, err)
		}
		log.Info("Released node for upgrade", "cluster", tc.Name, "ordinal", to, "target", targetLabel)
		return nil
	}

	started := metav1.NewTime(r.now().Truncate(time.Second))
	switch {
	case pending < 0 && partition > 0:
		// Everything released is done (or nothing is released yet): release the
		// next ordinal down.
		next := partition - 1
		if err := release(next); err != nil {
			return rolloutState{}, err
		}
		pending = next
		reasons[next] = "its pod has not been recreated on the new revision"
	case pending < 0:
		log.Info("Upgrade finished", "cluster", tc.Name, "target", targetLabel)
		return idle, nil
	default:
		// Keep the existing clock while waiting on the same node.
		if b := tc.Status.Rollout; b != nil && b.Node == fmt.Sprintf("%s-%d", tc.Name, pending) {
			started = b.NodeStartedTime
		}
	}

	node := fmt.Sprintf("%s-%d", tc.Name, pending)
	state := rolloutState{
		status:   metav1.ConditionTrue,
		reason:   reasonRollingOut,
		message:  fmt.Sprintf("Upgrading %s to %s (%d/%d nodes updated)", node, targetLabel, updated, replicas),
		bookmark: &dnsv1alpha1.TechnitiumClusterRolloutStatus{Node: node, NodeStartedTime: started},
	}
	if waited := r.now().Sub(started.Time); waited > r.stallTimeout() {
		state.reason = reasonUpgradeStalled
		state.message = fmt.Sprintf("Upgrade to %s is stalled on %s after %s: %s. No further nodes will be restarted until it recovers",
			targetLabel, node, waited.Truncate(time.Second), reasons[pending])
		state.degradedReason = degradedUpgradeStall
		state.degradedMessage = state.message
		log.Info("Upgrade stalled", "cluster", tc.Name, "node", node, "reason", reasons[pending])
	}
	return state, nil
}
