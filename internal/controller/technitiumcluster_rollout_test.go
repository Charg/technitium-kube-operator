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
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	dnsv1alpha1 "github.com/charg/technitium-operator/api/v1alpha1"
	"github.com/charg/technitium-operator/internal/technitium"
	"github.com/charg/technitium-operator/test/testimage"
)

// rolloutFake is a fully clustered Technitium cluster. Each ordinal reports
// its own version; connected says whether the Primary lists a Secondary as
// Connected.
type rolloutFake struct {
	mu        sync.Mutex
	name      string
	versions  map[int32]string
	connected map[int32]bool
}

func (f *rolloutFake) setVersion(ordinal int32, v string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.versions[ordinal] = v
	f.connected[ordinal] = true
}

func (f *rolloutFake) setConnected(ordinal int32, c bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.connected[ordinal] = c
}

type rolloutFakeNode struct {
	ordinal int32
	shared  *rolloutFake
}

func (n *rolloutFakeNode) GetClusterState(ctx context.Context) (*technitium.ClusterState, error) {
	f := n.shared
	f.mu.Lock()
	defer f.mu.Unlock()

	ordinals := make([]int, 0, len(f.versions))
	for o := range f.versions {
		ordinals = append(ordinals, int(o))
	}
	slices.Sort(ordinals)

	now := metav1.Now().Time
	nodes := make([]technitium.ClusterNode, 0, len(ordinals))
	for _, oi := range ordinals {
		o := int32(oi)
		node := technitium.ClusterNode{
			ID:               oi + 1,
			Name:             fmt.Sprintf("%s-%d.cluster.local", f.name, o),
			IPAddresses:      []string{fmt.Sprintf("10.0.0.%d", o+1)},
			Type:             "Secondary",
			Version:          f.versions[o],
			State:            "Connected",
			ConfigLastSynced: &now,
		}
		if o == 0 {
			node.Type = "Primary"
		}
		if !f.connected[o] {
			node.State = "Unknown"
		}
		if o == n.ordinal {
			node.State = "Self"
		}
		nodes = append(nodes, node)
	}
	return &technitium.ClusterState{
		ClusterInitialized: true,
		Version:            f.versions[n.ordinal],
		DNSServerDomain:    fmt.Sprintf("%s-%d.cluster.local", f.name, n.ordinal),
		ClusterDomain:      "cluster.local",
		Nodes:              nodes,
	}, nil
}

func (n *rolloutFakeNode) InitCluster(context.Context, technitium.InitClusterOptions) (*technitium.ClusterState, error) {
	return nil, errors.New("InitCluster must not be called")
}

func (n *rolloutFakeNode) InitJoinCluster(context.Context, technitium.InitJoinOptions) (*technitium.ClusterState, error) {
	return nil, errors.New("InitJoinCluster must not be called")
}
func (n *rolloutFakeNode) RemoveSecondary(context.Context, int) error { return nil }
func (n *rolloutFakeNode) DeleteSecondary(context.Context, int) error { return nil }
func (n *rolloutFakeNode) DeletePrimaryCluster(context.Context, bool) error {
	return nil
}
func (n *rolloutFakeNode) UpdateNodeIPAddresses(context.Context, []string) (*technitium.ClusterState, error) {
	return nil, errors.New("UpdateNodeIPAddresses must not be called")
}

var _ = Describe("TechnitiumCluster Controller upgrade rollout", func() {
	ctx := context.Background()

	const (
		oldImage   = testimage.PreviousTechnitiumImage
		newImage   = testimage.TechnitiumImage
		oldVersion = "15.6.0"
		newVersion = "15.6.1"
	)

	var (
		namespace     string
		resourceName  string
		key           types.NamespacedName
		nsCounter     int
		resourceCount int
		fake          *rolloutFake
		clock         time.Time
		r             *TechnitiumClusterReconciler
	)

	// reconcileOnce runs one pass. The StatefulSet controller, absent in
	// envtest, is simulated just enough to acknowledge the latest spec
	// generation first, as it does within moments of every spec write.
	reconcileOnce := func() {
		GinkgoHelper()
		var sts appsv1.StatefulSet
		if err := k8sClient.Get(ctx, types.NamespacedName{Namespace: namespace, Name: resourceName}, &sts); err == nil &&
			sts.Status.ObservedGeneration != sts.Generation && sts.Status.UpdateRevision != "" {
			sts.Status.ObservedGeneration = sts.Generation
			Expect(k8sClient.Status().Update(ctx, &sts)).To(Succeed())
		}
		_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: key})
		Expect(err).NotTo(HaveOccurred())
	}

	getCR := func() *dnsv1alpha1.TechnitiumCluster {
		GinkgoHelper()
		var cr dnsv1alpha1.TechnitiumCluster
		Expect(k8sClient.Get(ctx, key, &cr)).To(Succeed())
		return &cr
	}

	getSts := func() *appsv1.StatefulSet {
		GinkgoHelper()
		var sts appsv1.StatefulSet
		Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: namespace, Name: resourceName}, &sts)).To(Succeed())
		return &sts
	}

	partition := func() int32 {
		GinkgoHelper()
		return *getSts().Spec.UpdateStrategy.RollingUpdate.Partition
	}

	upgrading := func() *metav1.Condition {
		GinkgoHelper()
		return meta.FindStatusCondition(getCR().Status.Conditions, dnsv1alpha1.TechnitiumClusterConditionUpgrading)
	}

	stsImage := func() string {
		GinkgoHelper()
		return templateImage(getSts())
	}

	// setStsStatus simulates the StatefulSet controller's view.
	setStsStatus := func(ready int32, current, update string) {
		GinkgoHelper()
		sts := getSts()
		sts.Status.ObservedGeneration = sts.Generation
		sts.Status.Replicas = ready
		sts.Status.ReadyReplicas = ready
		sts.Status.CurrentRevision = current
		sts.Status.UpdateRevision = update
		Expect(k8sClient.Status().Update(ctx, sts)).To(Succeed())
	}

	podKey := func(ordinal int32) types.NamespacedName {
		return types.NamespacedName{Namespace: namespace, Name: fmt.Sprintf("%s-%d", resourceName, ordinal)}
	}

	// setPod creates or updates the pod at ordinal as running revision.
	setPod := func(ordinal int32, revision string, ready bool) {
		GinkgoHelper()
		labels := instanceLabels(resourceName)
		labels[appsv1.ControllerRevisionHashLabelKey] = revision
		var pod corev1.Pod
		if err := k8sClient.Get(ctx, podKey(ordinal), &pod); err != nil {
			pod = corev1.Pod{
				Name:      podKey(ordinal).Name,
				Namespace: namespace,
				Labels:    labels,
				Spec:      corev1.PodSpec{Containers: []corev1.Container{{Name: dnsServerContainerName, Image: newImage}}},
			}
			Expect(k8sClient.Create(ctx, &pod)).To(Succeed())
		} else {
			pod.Labels = labels
			Expect(k8sClient.Update(ctx, &pod)).To(Succeed())
		}
		Expect(k8sClient.Get(ctx, podKey(ordinal), &pod)).To(Succeed())
		pod.Status.PodIP = fmt.Sprintf("10.0.0.%d", ordinal+1)
		status := corev1.ConditionFalse
		if ready {
			status = corev1.ConditionTrue
		}
		pod.Status.Conditions = []corev1.PodCondition{{Type: corev1.PodReady, Status: status}}
		Expect(k8sClient.Status().Update(ctx, &pod)).To(Succeed())
	}

	// finishNode simulates a node coming back on the new revision, running the
	// new version and rejoined.
	finishNode := func(ordinal int32, revision, version string) {
		GinkgoHelper()
		setPod(ordinal, revision, true)
		fake.setVersion(ordinal, version)
	}

	setImage := func(image string) {
		GinkgoHelper()
		cr := getCR()
		cr.Spec.Image = image
		Expect(k8sClient.Update(ctx, cr)).To(Succeed())
	}

	scale := func(replicas int32) {
		GinkgoHelper()
		cr := getCR()
		cr.Spec.Replicas = ptr.To(replicas)
		Expect(k8sClient.Update(ctx, cr)).To(Succeed())
	}

	// startCluster brings up a settled cluster of the given size on image,
	// with every pod on revision rev-1.
	startCluster := func(replicas int32, image, version string) {
		GinkgoHelper()
		cr := &dnsv1alpha1.TechnitiumCluster{
			Name: resourceName,
			Spec: dnsv1alpha1.TechnitiumClusterSpec{
				Image:    image,
				Storage:  dnsv1alpha1.TechnitiumClusterStorageSpec{Size: resource.MustParse("1Gi")},
				Replicas: ptr.To(replicas),
			},
		}
		Expect(k8sClient.Create(ctx, cr)).To(Succeed())
		reconcileOnce()
		for i := range replicas {
			setPod(i, "rev-1", true)
			fake.setVersion(i, version)
		}
		setStsStatus(replicas, "rev-1", "rev-1")
		reconcileOnce()
	}

	BeforeEach(func() {
		nsCounter++
		namespace = fmt.Sprintf("tc-rollout-ns-%d", nsCounter)
		Expect(k8sClient.Create(ctx, &corev1.Namespace{Name: namespace})).To(Succeed())

		resourceCount++
		resourceName = fmt.Sprintf("rollout-cluster-%d", resourceCount)
		key = types.NamespacedName{Name: resourceName}
		fake = &rolloutFake{name: resourceName, versions: map[int32]string{}, connected: map[int32]bool{}}
		clock = time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

		r = &TechnitiumClusterReconciler{
			Client:            k8sClient,
			Scheme:            k8sClient.Scheme(),
			OperatorNamespace: namespace,
			StallTimeout:      10 * time.Minute,
			Now:               func() time.Time { return clock },
			NewBootstrapClient: func(endpoint, username, password string) (bootstrapAPI, error) {
				return &fakeBootstrapClient{calls: new(int), token: "minted-token"}, nil
			},
			NewNodeClient: func(endpoint, username, password string) (nodeAPI, error) {
				rest := strings.TrimPrefix(endpoint, "http://"+resourceName+"-")
				ordinal, err := strconv.Atoi(rest[:strings.Index(rest, ".")])
				if err != nil {
					return nil, err
				}
				return &rolloutFakeNode{ordinal: int32(ordinal), shared: fake}, nil
			},
		}
	})

	AfterEach(func() {
		cr := &dnsv1alpha1.TechnitiumCluster{}
		if err := k8sClient.Get(ctx, key, cr); err == nil {
			_ = k8sClient.Delete(ctx, cr)
		}
	})

	Context("with three replicas", func() {
		BeforeEach(func() {
			startCluster(3, oldImage, oldVersion)
		})

		It("creates the StatefulSet with a RollingUpdate partition of zero and reports UpToDate", func() {
			sts := getSts()
			Expect(sts.Spec.UpdateStrategy.Type).To(Equal(appsv1.RollingUpdateStatefulSetStrategyType))
			Expect(partition()).To(BeZero())
			cond := upgrading()
			Expect(cond).NotTo(BeNil())
			Expect(cond.Status).To(Equal(metav1.ConditionFalse))
			Expect(cond.Reason).To(Equal("UpToDate"))
			Expect(getCR().Status.Rollout).To(BeNil())
		})

		It("does not restart anything when the template changes, then walks from the highest ordinal down with the Primary last", func() {
			setImage(newImage)
			reconcileOnce()

			// Template changed: parked so no pod restarts yet.
			Expect(stsImage()).To(Equal(newImage))
			Expect(partition()).To(Equal(int32(3)))
			Expect(upgrading().Status).To(Equal(metav1.ConditionTrue))

			// The StatefulSet controller observes the new revision.
			setStsStatus(3, "rev-1", "rev-2")
			reconcileOnce()
			Expect(partition()).To(Equal(int32(2)), "the highest Secondary goes first")
			cond := upgrading()
			Expect(cond.Status).To(Equal(metav1.ConditionTrue))
			Expect(cond.Reason).To(Equal("RollingOut"))
			Expect(cond.Message).To(Equal(fmt.Sprintf("Upgrading %s-2 to %s (0/3 nodes updated)", resourceName, newVersion)))
			Expect(getCR().Status.Rollout.Node).To(Equal(resourceName + "-2"))

			// Nothing changes until the node is back.
			reconcileOnce()
			Expect(partition()).To(Equal(int32(2)))

			finishNode(2, "rev-2", newVersion)
			reconcileOnce()
			Expect(partition()).To(Equal(int32(1)))
			Expect(upgrading().Message).To(Equal(fmt.Sprintf("Upgrading %s-1 to %s (1/3 nodes updated)", resourceName, newVersion)))
			Expect(getCR().Status.Version).To(Equal("mixed"))

			finishNode(1, "rev-2", newVersion)
			reconcileOnce()
			Expect(partition()).To(BeZero(), "the Primary is released last")
			Expect(upgrading().Message).To(Equal(fmt.Sprintf("Upgrading %s-0 to %s (2/3 nodes updated)", resourceName, newVersion)))

			finishNode(0, "rev-2", newVersion)
			setStsStatus(3, "rev-2", "rev-2")
			reconcileOnce()
			Expect(partition()).To(BeZero())
			cond = upgrading()
			Expect(cond.Status).To(Equal(metav1.ConditionFalse))
			Expect(cond.Reason).To(Equal("UpToDate"))
			cr := getCR()
			Expect(cr.Status.Rollout).To(BeNil())
			Expect(cr.Status.Version).To(Equal(newVersion))
			Expect(meta.IsStatusConditionFalse(cr.Status.Conditions, dnsv1alpha1.TechnitiumClusterConditionDegraded)).To(BeTrue())
		})

		It("does not advance until the node reports the target version", func() {
			setImage(newImage)
			reconcileOnce()
			setStsStatus(3, "rev-1", "rev-2")
			reconcileOnce()
			Expect(partition()).To(Equal(int32(2)))

			// New pod, still the old version.
			setPod(2, "rev-2", true)
			reconcileOnce()
			Expect(partition()).To(Equal(int32(2)))

			fake.setVersion(2, newVersion)
			reconcileOnce()
			Expect(partition()).To(Equal(int32(1)))
		})

		It("does not advance until the pod is Ready", func() {
			setImage(newImage)
			reconcileOnce()
			setStsStatus(3, "rev-1", "rev-2")
			reconcileOnce()

			fake.setVersion(2, newVersion)
			setPod(2, "rev-2", false)
			reconcileOnce()
			Expect(partition()).To(Equal(int32(2)))
		})

		It("does not advance until the node is Connected in the Primary's cluster state", func() {
			setImage(newImage)
			reconcileOnce()
			setStsStatus(3, "rev-1", "rev-2")
			reconcileOnce()

			finishNode(2, "rev-2", newVersion)
			fake.setConnected(2, false)
			reconcileOnce()
			Expect(partition()).To(Equal(int32(2)))

			fake.setConnected(2, true)
			reconcileOnce()
			Expect(partition()).To(Equal(int32(1)))
		})

		It("does not release the Primary until the last Secondary is Connected", func() {
			setImage(newImage)
			reconcileOnce()
			setStsStatus(3, "rev-1", "rev-2")
			reconcileOnce()
			finishNode(2, "rev-2", newVersion)
			reconcileOnce()
			Expect(partition()).To(Equal(int32(1)))

			finishNode(1, "rev-2", newVersion)
			fake.setConnected(1, false)
			reconcileOnce()
			Expect(partition()).To(Equal(int32(1)), "the Primary must not restart while a Secondary is cut off")

			fake.setConnected(1, true)
			reconcileOnce()
			Expect(partition()).To(BeZero())
		})

		It("does not advance while the pod is still on the old revision", func() {
			setImage(newImage)
			reconcileOnce()
			setStsStatus(3, "rev-1", "rev-2")
			reconcileOnce()

			// The node reports the right version but its pod was never recreated.
			fake.setVersion(2, newVersion)
			reconcileOnce()
			Expect(partition()).To(Equal(int32(2)))
		})

		It("reports Stalled after the timeout and releases no further node, then resumes when the node recovers", func() {
			setImage(newImage)
			reconcileOnce()
			setStsStatus(3, "rev-1", "rev-2")
			reconcileOnce()
			Expect(partition()).To(Equal(int32(2)))

			// The pod came back but never reconnects.
			finishNode(2, "rev-2", newVersion)
			fake.setConnected(2, false)

			clock = clock.Add(9 * time.Minute)
			reconcileOnce()
			Expect(upgrading().Reason).To(Equal("RollingOut"))

			clock = clock.Add(2 * time.Minute)
			reconcileOnce()
			cond := upgrading()
			Expect(cond.Status).To(Equal(metav1.ConditionTrue))
			Expect(cond.Reason).To(Equal("Stalled"))
			Expect(cond.Message).To(ContainSubstring(resourceName + "-2"))
			Expect(cond.Message).To(ContainSubstring("not Connected"))
			Expect(partition()).To(Equal(int32(2)))
			degraded := meta.FindStatusCondition(getCR().Status.Conditions, dnsv1alpha1.TechnitiumClusterConditionDegraded)
			Expect(degraded.Status).To(Equal(metav1.ConditionTrue))
			Expect(degraded.Reason).To(Equal("UpgradeStalled"))

			// The operator keeps polling and does not release anything while stalled.
			result, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: key})
			Expect(err).NotTo(HaveOccurred())
			Expect(result.RequeueAfter).To(Equal(clusterPollInterval))
			Expect(partition()).To(Equal(int32(2)))

			fake.setConnected(2, true)
			reconcileOnce()
			Expect(partition()).To(Equal(int32(1)))
			Expect(upgrading().Reason).To(Equal("RollingOut"))
			degraded = meta.FindStatusCondition(getCR().Status.Conditions, dnsv1alpha1.TechnitiumClusterConditionDegraded)
			Expect(degraded.Status).To(Equal(metav1.ConditionFalse))
		})

		It("measures the stall from when the node was released, not from the first reconcile of the pass", func() {
			setImage(newImage)
			reconcileOnce()
			setStsStatus(3, "rev-1", "rev-2")
			reconcileOnce()
			started := getCR().Status.Rollout.NodeStartedTime

			// A fresh reconciler (an operator restart) must keep the same clock.
			clock = clock.Add(11 * time.Minute)
			reconcileOnce()
			Expect(getCR().Status.Rollout.NodeStartedTime.Time).To(BeTemporally("==", started.Time))
			Expect(upgrading().Reason).To(Equal("Stalled"))
		})

		It("keeps the walk gated when the target image has no parseable version", func() {
			setImage("registry.example.com/technitium/dns-server:latest")
			reconcileOnce()
			setStsStatus(3, "rev-1", "rev-2")
			reconcileOnce()
			Expect(partition()).To(Equal(int32(2)))

			// The pod is back but reports an unrelated version: no version check.
			setPod(2, "rev-2", true)
			reconcileOnce()
			Expect(partition()).To(Equal(int32(1)))
		})

		It("lets a node created by scale-up during a rollout be the node in flight", func() {
			setImage(newImage)
			reconcileOnce()
			Expect(partition()).To(Equal(int32(3)))

			scale(4)
			reconcileOnce()
			Expect(partition()).To(Equal(int32(3)), "the new ordinal is at or above the partition, so it is created on the new revision")
			setStsStatus(3, "rev-1", "rev-2")
			reconcileOnce()
			Expect(upgrading().Message).To(ContainSubstring(resourceName + "-3"))
			Expect(partition()).To(Equal(int32(3)))

			finishNode(3, "rev-2", newVersion)
			reconcileOnce()
			Expect(partition()).To(Equal(int32(2)))
		})

		It("scales up in steady state without a rollout", func() {
			scale(4)
			reconcileOnce()
			Expect(partition()).To(BeZero())
			Expect(*getSts().Spec.Replicas).To(Equal(int32(4)))
			Expect(upgrading().Reason).To(Equal("UpToDate"))
		})

		It("blocks a downgrade by default and keeps the deployed image", func() {
			setImage(newImage)
			reconcileOnce()
			setStsStatus(3, "rev-1", "rev-2")
			for i := int32(2); i >= 0; i-- {
				reconcileOnce()
				finishNode(i, "rev-2", newVersion)
			}
			reconcileOnce()
			setStsStatus(3, "rev-2", "rev-2")
			reconcileOnce()
			Expect(upgrading().Reason).To(Equal("UpToDate"))

			hashBefore := getSts().Annotations[podTemplateHashAnnotation]
			setImage(oldImage)
			reconcileOnce()

			Expect(stsImage()).To(Equal(newImage))
			Expect(getSts().Annotations[podTemplateHashAnnotation]).To(Equal(hashBefore))
			Expect(partition()).To(BeZero())
			cond := upgrading()
			Expect(cond.Status).To(Equal(metav1.ConditionFalse))
			Expect(cond.Reason).To(Equal("DowngradeBlocked"))
			Expect(cond.Message).To(ContainSubstring(newVersion))
			Expect(cond.Message).To(ContainSubstring(oldVersion))
			Expect(cond.Message).To(ContainSubstring(dnsv1alpha1.AllowDowngradeAnnotation))
			degraded := meta.FindStatusCondition(getCR().Status.Conditions, dnsv1alpha1.TechnitiumClusterConditionDegraded)
			Expect(degraded.Status).To(Equal(metav1.ConditionTrue))
			Expect(degraded.Reason).To(Equal("DowngradeBlocked"))

			// Allowed with the annotation: the same gated walk applies.
			cr := getCR()
			cr.Annotations = map[string]string{dnsv1alpha1.AllowDowngradeAnnotation: "true"}
			Expect(k8sClient.Update(ctx, cr)).To(Succeed())
			reconcileOnce()
			Expect(stsImage()).To(Equal(oldImage))
			Expect(partition()).To(Equal(int32(3)))
			Expect(upgrading().Reason).To(Equal("RollingOut"))
			degraded = meta.FindStatusCondition(getCR().Status.Conditions, dnsv1alpha1.TechnitiumClusterConditionDegraded)
			Expect(degraded.Status).To(Equal(metav1.ConditionFalse))
		})

		It("applies other spec changes while a downgrade is blocked", func() {
			// Same deployed version as oldImage, so ask for something older.
			setImage("technitium/dns-server:15.5.0")
			reconcileOnce()
			Expect(stsImage()).To(Equal(oldImage))
			scale(4)
			reconcileOnce()
			Expect(*getSts().Spec.Replicas).To(Equal(int32(4)))
			Expect(stsImage()).To(Equal(oldImage))
			Expect(upgrading().Reason).To(Equal("DowngradeBlocked"))
		})

		It("does not guard an unparseable image against a parseable deployed one", func() {
			setImage("registry.example.com/technitium/dns-server:latest")
			reconcileOnce()
			Expect(stsImage()).To(Equal("registry.example.com/technitium/dns-server:latest"))
			Expect(upgrading().Reason).NotTo(Equal("DowngradeBlocked"))
		})
	})

	Context("with a single replica", func() {
		BeforeEach(func() {
			startCluster(1, oldImage, oldVersion)
		})

		It("replaces the pod without a partition walk", func() {
			setImage(newImage)
			reconcileOnce()
			Expect(stsImage()).To(Equal(newImage))
			Expect(partition()).To(BeZero())
			Expect(upgrading().Status).To(Equal(metav1.ConditionTrue))

			setStsStatus(1, "rev-1", "rev-2")
			reconcileOnce()
			Expect(partition()).To(BeZero())
			Expect(upgrading().Message).To(Equal(fmt.Sprintf("Upgrading %s-0 to %s (0/1 nodes updated)", resourceName, newVersion)))

			finishNode(0, "rev-2", newVersion)
			setStsStatus(1, "rev-2", "rev-2")
			reconcileOnce()
			Expect(upgrading().Reason).To(Equal("UpToDate"))
		})

		It("still blocks a downgrade", func() {
			setImage("technitium/dns-server:15.5.0")
			reconcileOnce()
			Expect(stsImage()).To(Equal(oldImage))
			Expect(upgrading().Reason).To(Equal("DowngradeBlocked"))

			cr := getCR()
			cr.Annotations = map[string]string{dnsv1alpha1.AllowDowngradeAnnotation: "true"}
			Expect(k8sClient.Update(ctx, cr)).To(Succeed())
			reconcileOnce()
			Expect(stsImage()).To(Equal("technitium/dns-server:15.5.0"))
		})
	})
})
