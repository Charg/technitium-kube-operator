/*
Copyright (c) 2026 Chris

Licensed under the MIT License. See the LICENSE file in the project root for
the full license text.
*/

package controller

import (
	"context"
	"errors"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	dnsv1alpha1 "github.com/charg/technitium-operator/api/v1alpha1"
	"github.com/charg/technitium-operator/internal/config"
	"github.com/charg/technitium-operator/internal/technitium"
)

// clusterDriftReconcileInterval is how long to wait before re-checking a fully
// formed cluster. Membership can change out of band (a node promoted, a manual
// leave), so the controller re-reads state periodically.
const clusterDriftReconcileInterval = 5 * time.Minute

// clusterRetryInterval is the shorter requeue used while the cluster is still
// converging: the primary is not initialized yet, or a node is unreachable.
// Convergence should not wait a full drift interval.
const clusterRetryInterval = 30 * time.Second

// Condition types reported on a Cluster's status. Ready is the summary users key
// on (and the Ready printer column); Progressing marks an in-flight init or
// join; Degraded carries the cause when a reconcile cannot proceed at all.
const (
	clusterConditionReady       = "Ready"
	clusterConditionProgressing = "Progressing"
	clusterConditionDegraded    = "Degraded"
)

// ClusterAPI is the subset of the Technitium client the cluster reconciler
// depends on, per node. Depending on the interface keeps reconciliation testable
// against a fake server.
type ClusterAPI interface {
	ClusterState(ctx context.Context) (*technitium.ClusterState, error)
	ClusterInit(ctx context.Context, opts technitium.ClusterInitOptions) (*technitium.ClusterState, error)
	ClusterInitJoin(ctx context.Context, opts technitium.ClusterInitJoinOptions) (*technitium.ClusterState, error)
}

// NodeClientFactory builds a ClusterAPI for a single node from its endpoint and
// resolved authentication options. The default implementation logs in when the
// options carry a username and password rather than a token.
type NodeClientFactory func(ctx context.Context, endpoint string, opts ...technitium.Option) (ClusterAPI, error)

// defaultNodeClientFactory builds the real Technitium client and exchanges
// credentials for a token when needed, so every later call is authenticated.
func defaultNodeClientFactory(ctx context.Context, endpoint string, opts ...technitium.Option) (ClusterAPI, error) {
	c, err := technitium.NewClient(endpoint, opts...)
	if err != nil {
		return nil, err
	}
	if c.Token() == "" {
		if _, err := c.Login(ctx); err != nil {
			return nil, fmt.Errorf("logging in to %s: %w", endpoint, err)
		}
	}
	return c, nil
}

// ClusterReconciler reconciles a Cluster object against the Technitium cluster
// admin API.
type ClusterReconciler struct {
	client.Client
	Scheme *runtime.Scheme

	// OperatorNamespace is the fallback namespace for credential Secret
	// references that omit one.
	OperatorNamespace string
	// InsecureSkipVerify configures the operator's own TLS verification when it
	// connects to each node. It is independent of the node-to-node
	// IgnoreCertificateErrors used during a join.
	InsecureSkipVerify bool
	// NewNodeClient builds a per-node client. Tests inject a fake; production
	// leaves it nil and SetupWithManager wires the default.
	NewNodeClient NodeClientFactory
}

// +kubebuilder:rbac:groups=dns.packet.fail,resources=clusters,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=dns.packet.fail,resources=clusters/status,verbs=get;update;patch
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get

// Reconcile drives the Technitium cluster toward the Cluster spec: it
// initializes the primary once, joins each secondary once, and reports observed
// membership. It never re-initializes a node that is already a member.
//
// Deletion orphans the server-side cluster: with no finalizer, removing the
// resource simply stops reconciliation.
func (r *ClusterReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	var cluster dnsv1alpha1.Cluster
	if err := r.Get(ctx, req.NamespacedName, &cluster); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	outcome, err := r.reconcileCluster(ctx, &cluster)
	if err != nil {
		// A setup error (unreadable primary Secret, missing primary credentials)
		// prevents any progress. Record it and let the manager back off.
		log.Error(err, "Failed to reconcile Cluster", "cluster", cluster.Spec.ClusterDomain)
		if statusErr := r.markClusterDegraded(ctx, req.NamespacedName, err); statusErr != nil {
			log.Error(statusErr, "Failed to update Cluster status", "cluster", cluster.Spec.ClusterDomain)
		}
		return ctrl.Result{}, err
	}

	if statusErr := r.writeClusterStatus(ctx, req.NamespacedName, outcome); statusErr != nil {
		return ctrl.Result{}, statusErr
	}

	if !outcome.ready {
		// Still converging: a node was unreachable or the primary is not yet
		// initialized. Requeue sooner than the drift interval.
		return ctrl.Result{RequeueAfter: clusterRetryInterval}, nil
	}
	return ctrl.Result{RequeueAfter: clusterDriftReconcileInterval}, nil
}

// clusterOutcome carries the observed result of one reconcile pass, ready for
// projection onto status.
type clusterOutcome struct {
	domain string
	nodes  []dnsv1alpha1.ClusterNodeStatus
	// ready is true only when every node in the spec is a confirmed member.
	ready bool
}

// reconcileCluster initializes the primary, then joins each secondary. It
// returns a Go error only for failures that block the whole reconcile; per-node
// failures are recorded in the outcome and converge on requeue.
func (r *ClusterReconciler) reconcileCluster(ctx context.Context, cluster *dnsv1alpha1.Cluster) (clusterOutcome, error) {
	log := logf.FromContext(ctx)
	spec := cluster.Spec

	out := clusterOutcome{ready: true}

	// Primary first: a secondary cannot join until the primary has a cluster.
	primaryState, primaryStatus := r.reconcilePrimary(ctx, spec)
	out.nodes = append(out.nodes, primaryStatus)
	if primaryStatus.Member {
		out.domain = primaryState.Domain
	} else {
		// Without an initialized primary there is nothing for secondaries to join.
		// Record them as pending and requeue.
		out.ready = false
		for _, sec := range spec.Secondaries {
			out.nodes = append(out.nodes, dnsv1alpha1.ClusterNodeStatus{
				Name:    sec.Name,
				Message: "Waiting for the primary node to be initialized",
			})
		}
		return out, nil
	}

	if len(spec.Secondaries) == 0 {
		return out, nil
	}

	// Joining a secondary requires the primary's local administrator credentials,
	// which the server will not accept as a token.
	primaryUser, primaryPass, err := r.primaryCredentials(ctx, spec.Primary)
	if err != nil {
		return clusterOutcome{}, err
	}

	for _, sec := range spec.Secondaries {
		status := r.reconcileSecondary(ctx, spec, sec, primaryUser, primaryPass)
		out.nodes = append(out.nodes, status)
		if !status.Member {
			out.ready = false
		}
	}

	if out.ready {
		log.Info("Cluster fully reconciled", "cluster", spec.ClusterDomain, "nodes", len(out.nodes))
	}
	return out, nil
}

// reconcilePrimary ensures the primary node is initialized. It reads the node's
// state first and only initializes an uninitialized node, treating an
// already-initialized node as success.
func (r *ClusterReconciler) reconcilePrimary(ctx context.Context, spec dnsv1alpha1.ClusterSpec) (*technitium.ClusterState, dnsv1alpha1.ClusterNodeStatus) {
	log := logf.FromContext(ctx)
	node := spec.Primary
	status := dnsv1alpha1.ClusterNodeStatus{Name: node.Name}

	api, err := r.nodeClient(ctx, node)
	if err != nil {
		status.Message = fmt.Sprintf("Connecting to primary: %v", err)
		return nil, status
	}

	state, err := api.ClusterState(ctx)
	if err != nil {
		status.Message = fmt.Sprintf("Reading cluster state: %v", err)
		return nil, status
	}

	if !state.Initialized {
		state, err = api.ClusterInit(ctx, technitium.ClusterInitOptions{
			ClusterDomain:          spec.ClusterDomain,
			PrimaryNodeIPAddresses: node.IPAddresses,
		})
		switch {
		case err == nil:
			log.Info("Initialized cluster primary", "cluster", spec.ClusterDomain, "node", node.Name)
		case errors.Is(err, technitium.ErrClusterAlreadyInitialized):
			// A concurrent init (another replica, a manual action) beat us to it.
			// Re-read to reflect the real membership.
			state, err = api.ClusterState(ctx)
			if err != nil {
				status.Message = fmt.Sprintf("Re-reading cluster state: %v", err)
				return nil, status
			}
		default:
			status.Message = fmt.Sprintf("Initializing cluster: %v", err)
			return nil, status
		}
	}

	applyNodeState(&status, state, technitium.ClusterNodeTypePrimary)
	return state, status
}

// reconcileSecondary ensures one secondary node has joined the cluster. It reads
// the node's own state first and only joins an unjoined node.
func (r *ClusterReconciler) reconcileSecondary(ctx context.Context, spec dnsv1alpha1.ClusterSpec, node dnsv1alpha1.ClusterNodeSpec, primaryUser, primaryPass string) dnsv1alpha1.ClusterNodeStatus {
	log := logf.FromContext(ctx)
	status := dnsv1alpha1.ClusterNodeStatus{Name: node.Name}

	api, err := r.nodeClient(ctx, node)
	if err != nil {
		status.Message = fmt.Sprintf("Connecting to secondary: %v", err)
		return status
	}

	state, err := api.ClusterState(ctx)
	if err != nil {
		status.Message = fmt.Sprintf("Reading cluster state: %v", err)
		return status
	}

	if !state.Initialized {
		opts := technitium.ClusterInitJoinOptions{
			SecondaryNodeIPAddresses: node.IPAddresses,
			PrimaryNodeURL:           spec.Primary.Endpoint,
			PrimaryNodeUsername:      primaryUser,
			PrimaryNodePassword:      primaryPass,
			IgnoreCertificateErrors:  spec.IgnoreCertificateErrors,
		}
		if len(spec.Primary.IPAddresses) > 0 {
			opts.PrimaryNodeIPAddress = spec.Primary.IPAddresses[0]
		}

		state, err = api.ClusterInitJoin(ctx, opts)
		switch {
		case err == nil:
			log.Info("Joined cluster secondary", "cluster", spec.ClusterDomain, "node", node.Name)
		case errors.Is(err, technitium.ErrClusterAlreadyInitialized):
			state, err = api.ClusterState(ctx)
			if err != nil {
				status.Message = fmt.Sprintf("Re-reading cluster state: %v", err)
				return status
			}
		case errors.Is(err, technitium.ErrClusterNotInitialized):
			// The primary lost its cluster between our primary check and this
			// join. Leave the node pending and converge on the next pass.
			status.Message = "Waiting for the primary node to be initialized"
			return status
		default:
			status.Message = fmt.Sprintf("Joining cluster: %v", err)
			return status
		}
	}

	applyNodeState(&status, state, technitium.ClusterNodeTypeSecondary)
	return status
}

// applyNodeState projects an observed cluster state onto a node's status. The
// role is taken from the node's self entry when present, falling back to the
// expected role.
func applyNodeState(status *dnsv1alpha1.ClusterNodeStatus, state *technitium.ClusterState, expectedRole string) {
	status.Member = state.Initialized
	status.Role = expectedRole
	status.Message = ""

	now := metav1.Now()
	status.LastSyncedTime = &now

	if self := state.SelfNode(); self != nil && self.Type != "" {
		status.Role = self.Type
	}
}

// nodeClient resolves a node's credentials Secret and builds a client for it.
func (r *ClusterReconciler) nodeClient(ctx context.Context, node dnsv1alpha1.ClusterNodeSpec) (ClusterAPI, error) {
	secret, err := r.readSecret(ctx, node.CredentialsSecretRef)
	if err != nil {
		return nil, err
	}
	opts, err := config.ClientOptionsFromSecret(secret)
	if err != nil {
		return nil, err
	}
	opts = append(opts, technitium.WithInsecureSkipVerify(r.InsecureSkipVerify))
	return r.NewNodeClient(ctx, node.Endpoint, opts...)
}

// primaryCredentials reads the primary node's Secret and returns its username
// and password, required to join secondaries.
func (r *ClusterReconciler) primaryCredentials(ctx context.Context, primary dnsv1alpha1.ClusterNodeSpec) (username, password string, err error) {
	secret, err := r.readSecret(ctx, primary.CredentialsSecretRef)
	if err != nil {
		return "", "", err
	}
	user, pass, ok := config.UsernamePasswordFromSecret(secret)
	if !ok {
		return "", "", fmt.Errorf("primary credentials secret %q must contain username and password to join secondaries", secret.Name)
	}
	return user, pass, nil
}

// readSecret loads a credentials Secret, defaulting an empty namespace to the
// operator's own namespace.
func (r *ClusterReconciler) readSecret(ctx context.Context, ref dnsv1alpha1.SecretReference) (*corev1.Secret, error) {
	namespace := ref.Namespace
	if namespace == "" {
		namespace = r.OperatorNamespace
	}
	if namespace == "" {
		return nil, fmt.Errorf("credentials secret %q has no namespace and no operator namespace is configured", ref.Name)
	}

	var secret corev1.Secret
	key := types.NamespacedName{Namespace: namespace, Name: ref.Name}
	if err := r.Get(ctx, key, &secret); err != nil {
		return nil, fmt.Errorf("reading credentials secret %s: %w", key, err)
	}
	return &secret, nil
}

// writeClusterStatus re-fetches the Cluster and records the reconcile outcome:
// per-node membership, observed domain, and the Ready/Progressing/Degraded
// summary. Re-fetching avoids writing status onto a stale object.
func (r *ClusterReconciler) writeClusterStatus(ctx context.Context, key client.ObjectKey, outcome clusterOutcome) error {
	var cluster dnsv1alpha1.Cluster
	if err := r.Get(ctx, key, &cluster); err != nil {
		return client.IgnoreNotFound(err)
	}

	cluster.Status.Nodes = outcome.nodes
	cluster.Status.ClusterDomain = outcome.domain
	cluster.Status.ObservedGeneration = cluster.Generation

	if outcome.ready {
		setClusterCondition(&cluster, clusterConditionReady, metav1.ConditionTrue, "ClusterReady", "All nodes are cluster members")
		setClusterCondition(&cluster, clusterConditionProgressing, metav1.ConditionFalse, "ClusterReady", "All nodes are cluster members")
		setClusterCondition(&cluster, clusterConditionDegraded, metav1.ConditionFalse, "ClusterReady", "All nodes are cluster members")
	} else {
		msg := pendingNodesMessage(outcome.nodes)
		setClusterCondition(&cluster, clusterConditionReady, metav1.ConditionFalse, "ClusterProgressing", msg)
		setClusterCondition(&cluster, clusterConditionProgressing, metav1.ConditionTrue, "ClusterProgressing", msg)
		setClusterCondition(&cluster, clusterConditionDegraded, metav1.ConditionFalse, "ClusterProgressing", msg)
	}

	return r.Status().Update(ctx, &cluster)
}

// markClusterDegraded records a whole-reconcile failure so the cause is visible
// on the resource and not only in the logs.
func (r *ClusterReconciler) markClusterDegraded(ctx context.Context, key client.ObjectKey, cause error) error {
	var cluster dnsv1alpha1.Cluster
	if err := r.Get(ctx, key, &cluster); err != nil {
		return client.IgnoreNotFound(err)
	}

	setClusterCondition(&cluster, clusterConditionReady, metav1.ConditionFalse, "ReconcileFailed", cause.Error())
	setClusterCondition(&cluster, clusterConditionProgressing, metav1.ConditionFalse, "ReconcileFailed", cause.Error())
	setClusterCondition(&cluster, clusterConditionDegraded, metav1.ConditionTrue, "ReconcileFailed", cause.Error())

	return r.Status().Update(ctx, &cluster)
}

// pendingNodesMessage summarizes which nodes are not yet members, for the Ready
// condition message.
func pendingNodesMessage(nodes []dnsv1alpha1.ClusterNodeStatus) string {
	for _, n := range nodes {
		if !n.Member && n.Message != "" {
			return fmt.Sprintf("Node %q: %s", n.Name, n.Message)
		}
	}
	return "Waiting for all nodes to become cluster members"
}

// setClusterCondition upserts a status condition stamped with the cluster's
// current generation.
func setClusterCondition(cluster *dnsv1alpha1.Cluster, condType string, status metav1.ConditionStatus, reason, message string) {
	meta.SetStatusCondition(&cluster.Status.Conditions, metav1.Condition{
		Type:               condType,
		Status:             status,
		Reason:             reason,
		Message:            message,
		ObservedGeneration: cluster.Generation,
	})
}

// SetupWithManager sets up the controller with the Manager.
func (r *ClusterReconciler) SetupWithManager(mgr ctrl.Manager) error {
	if r.NewNodeClient == nil {
		r.NewNodeClient = defaultNodeClientFactory
	}
	return ctrl.NewControllerManagedBy(mgr).
		For(&dnsv1alpha1.Cluster{}).
		Named("cluster").
		Complete(r)
}
