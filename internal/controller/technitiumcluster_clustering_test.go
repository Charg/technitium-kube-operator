/*
Copyright (c) 2026 Chris

Licensed under the MIT License. See the LICENSE file in the project root for
the full license text.
*/

package controller

import (
	"context"
	"fmt"
	"strings"
	"sync"

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
)

// fakeClusterState is the shared server-side state a real 2-node Technitium
// cluster reaches through init/join. It is shared by every fakeClusterNode
// built for one test so the per-pod clients react to InitCluster and
// InitJoinCluster the way a real server would: once one call lands, every
// subsequent read (including one dispatched to a different ordinal, or a
// later reconcile pass) sees the effect, rather than each ordinal's fake
// carrying its own disconnected canned response.
type fakeClusterState struct {
	mu                 sync.Mutex
	podPrefix          string
	primaryInitialized bool
	secondaryJoined    bool
	initCalls          int
	joinCalls          int
}

// fakeClusterNode is a nodeAPI stand-in for one ordinal that reads and
// mutates the fakeClusterState shared across the whole cluster, so
// reconcileClustering's init-then-join sequencing and buildNodeStatuses's
// authoritative read of the primary can both be exercised without a real
// Technitium server.
type fakeClusterNode struct {
	ordinal int32
	shared  *fakeClusterState
}

func (f *fakeClusterNode) GetClusterState(ctx context.Context) (*technitium.ClusterState, error) {
	f.shared.mu.Lock()
	defer f.shared.mu.Unlock()

	if f.ordinal != 0 {
		// A secondary's own view of itself: it only ever reports initialized
		// once its own initJoin has landed.
		return &technitium.ClusterState{ClusterInitialized: f.shared.secondaryJoined}, nil
	}

	if !f.shared.primaryInitialized {
		return &technitium.ClusterState{ClusterInitialized: false}, nil
	}

	nodes := []technitium.ClusterNode{
		{ID: 1, Name: fmt.Sprintf("%s-0.cluster.local", f.shared.podPrefix), Type: "Primary", State: "Self"},
	}
	if f.shared.secondaryJoined {
		now := metav1.Now().Time
		nodes = append(nodes, technitium.ClusterNode{
			ID: 2, Name: fmt.Sprintf("%s-1.cluster.local", f.shared.podPrefix),
			Type: "Secondary", State: "Connected", ConfigLastSynced: &now,
		})
	}
	return &technitium.ClusterState{ClusterInitialized: true, ClusterDomain: "cluster.local", Nodes: nodes}, nil
}

func (f *fakeClusterNode) InitCluster(ctx context.Context, opts technitium.InitClusterOptions) (*technitium.ClusterState, error) {
	f.shared.mu.Lock()
	defer f.shared.mu.Unlock()
	f.shared.initCalls++
	f.shared.primaryInitialized = true
	return &technitium.ClusterState{ClusterInitialized: true}, nil
}

func (f *fakeClusterNode) InitJoinCluster(ctx context.Context, opts technitium.InitJoinOptions) (*technitium.ClusterState, error) {
	f.shared.mu.Lock()
	defer f.shared.mu.Unlock()
	f.shared.joinCalls++
	f.shared.secondaryJoined = true
	return &technitium.ClusterState{ClusterInitialized: true}, nil
}

var _ = Describe("TechnitiumCluster Controller clustering", func() {
	Context("When spec.replicas is 2", func() {
		ctx := context.Background()

		var (
			namespace     string
			resourceName  string
			key           types.NamespacedName
			nsCounter     int
			resourceCount int
			shared        *fakeClusterState
		)

		newNamespace := func() string {
			nsCounter++
			name := fmt.Sprintf("tc-clustering-ns-%d", nsCounter)
			ns := &corev1.Namespace{Name: name}
			Expect(k8sClient.Create(ctx, ns)).To(Succeed())
			return name
		}

		// newReconciler wires a fake bootstrap client that always mints a
		// token and a NewNodeClient that dispatches by endpoint to the
		// ordinal-specific fake sharing one fakeClusterState, so a
		// clustering reconcile never dials the unreachable per-pod endpoints
		// envtest cannot serve.
		newReconciler := func() *TechnitiumClusterReconciler {
			return &TechnitiumClusterReconciler{
				Client:            k8sClient,
				Scheme:            k8sClient.Scheme(),
				OperatorNamespace: namespace,
				NewBootstrapClient: func(endpoint, username, password string) (bootstrapAPI, error) {
					return &fakeBootstrapClient{calls: new(int), token: "minted-token"}, nil
				},
				NewNodeClient: func(endpoint, username, password string) (nodeAPI, error) {
					ordinal := int32(0)
					if strings.Contains(endpoint, fmt.Sprintf("%s-1.", resourceName)) {
						ordinal = 1
					}
					return &fakeClusterNode{ordinal: ordinal, shared: shared}, nil
				},
			}
		}

		storageSize := resource.MustParse("1Gi")

		createClusterCR := func() *dnsv1alpha1.TechnitiumCluster {
			resourceCount++
			resourceName = fmt.Sprintf("clustering-cluster-%d", resourceCount)
			key = types.NamespacedName{Name: resourceName}
			shared = &fakeClusterState{podPrefix: resourceName}

			cr := &dnsv1alpha1.TechnitiumCluster{
				Name: resourceName,
				Spec: dnsv1alpha1.TechnitiumClusterSpec{
					Image:    "technitium/dns-server:15.4.0",
					Storage:  dnsv1alpha1.TechnitiumClusterStorageSpec{Size: storageSize},
					Replicas: ptr.To(int32(2)),
				},
			}
			Expect(k8sClient.Create(ctx, cr)).To(Succeed())
			return cr
		}

		// markStatefulSetReady simulates kubelet reporting readyReplicas
		// pods ready at the StatefulSet level: envtest runs no kubelet, so
		// nothing else will ever populate ReadyReplicas on its own. It is
		// tracked independently of the Pod objects createPod below manages,
		// which is what lets the "secondary before primary" test simulate
		// the StatefulSet as a whole reporting ready while one ordinal's own
		// Pod has not actually reported an IP yet.
		markStatefulSetReady := func(readyReplicas int32) {
			var sts appsv1.StatefulSet
			stsKey := types.NamespacedName{Namespace: namespace, Name: resourceName}
			Expect(k8sClient.Get(ctx, stsKey, &sts)).To(Succeed())
			sts.Status.Replicas = readyReplicas
			sts.Status.ReadyReplicas = readyReplicas
			Expect(k8sClient.Status().Update(ctx, &sts)).To(Succeed())
		}

		// createPod creates the Pod object for one ordinal directly, since
		// envtest runs no StatefulSet controller to create it. When ip is
		// non-empty the Pod is also given a Ready condition, matching what
		// resolveClusterPods requires to treat the ordinal as reachable.
		createPod := func(ordinal int32, ip string) {
			pod := &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      fmt.Sprintf("%s-%d", resourceName, ordinal),
					Namespace: namespace,
					Labels:    instanceLabels(resourceName),
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{Name: "dns-server", Image: "technitium/dns-server:15.4.0"},
					},
				},
			}
			Expect(k8sClient.Create(ctx, pod)).To(Succeed())

			var got corev1.Pod
			podKey := types.NamespacedName{Namespace: namespace, Name: pod.Name}
			Expect(k8sClient.Get(ctx, podKey, &got)).To(Succeed())
			got.Status.PodIP = ip
			got.Status.Conditions = []corev1.PodCondition{
				{Type: corev1.PodReady, Status: corev1.ConditionTrue},
			}
			Expect(k8sClient.Status().Update(ctx, &got)).To(Succeed())
		}

		BeforeEach(func() {
			namespace = newNamespace()
		})

		AfterEach(func() {
			cr := &dnsv1alpha1.TechnitiumCluster{}
			if err := k8sClient.Get(ctx, key, cr); err == nil {
				_ = k8sClient.Delete(ctx, cr)
			}
		})

		It("reaches Ready with both nodes Connected, calling init once and initJoin once", func() {
			createClusterCR()
			r := newReconciler()
			_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: key})
			Expect(err).NotTo(HaveOccurred())

			createPod(0, "10.0.0.1")
			createPod(1, "10.0.0.2")
			markStatefulSetReady(2)

			_, err = r.Reconcile(ctx, reconcile.Request{NamespacedName: key})
			Expect(err).NotTo(HaveOccurred())

			var cr dnsv1alpha1.TechnitiumCluster
			Expect(k8sClient.Get(ctx, key, &cr)).To(Succeed())
			Expect(cr.Status.Phase).To(Equal(dnsv1alpha1.TechnitiumClusterPhaseReady))
			Expect(cr.Status.Members).To(Equal("2/2"))

			Expect(shared.initCalls).To(Equal(1))
			Expect(shared.joinCalls).To(Equal(1))

			// Per-node status reflects the primary's clusterNodes: role and
			// state come from what the primary itself reported, not the
			// readiness-derived placeholder.
			Expect(cr.Status.Nodes).To(HaveLen(2))
			Expect(cr.Status.Nodes[0].Role).To(Equal("Primary"))
			Expect(cr.Status.Nodes[0].State).To(Equal("Self"))
			Expect(cr.Status.Nodes[1].Role).To(Equal("Secondary"))
			Expect(cr.Status.Nodes[1].State).To(Equal("Connected"))
			Expect(cr.Status.Nodes[1].LastSynced).NotTo(BeNil())
		})

		It("does not call init or initJoin again once both nodes report clusterInitialized", func() {
			createClusterCR()
			r := newReconciler()
			_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: key})
			Expect(err).NotTo(HaveOccurred())

			createPod(0, "10.0.0.1")
			createPod(1, "10.0.0.2")
			markStatefulSetReady(2)

			_, err = r.Reconcile(ctx, reconcile.Request{NamespacedName: key})
			Expect(err).NotTo(HaveOccurred())
			Expect(shared.initCalls).To(Equal(1))
			Expect(shared.joinCalls).To(Equal(1))

			_, err = r.Reconcile(ctx, reconcile.Request{NamespacedName: key})
			Expect(err).NotTo(HaveOccurred())

			var cr dnsv1alpha1.TechnitiumCluster
			Expect(k8sClient.Get(ctx, key, &cr)).To(Succeed())
			Expect(cr.Status.Phase).To(Equal(dnsv1alpha1.TechnitiumClusterPhaseReady))
			Expect(shared.initCalls).To(Equal(1))
			Expect(shared.joinCalls).To(Equal(1))
		})

		It("stays in Clustering without Degraded when the primary pod is not yet ready", func() {
			createClusterCR()
			r := newReconciler()
			_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: key})
			Expect(err).NotTo(HaveOccurred())

			// Only the secondary's Pod is created: the primary ordinal has
			// no Pod object at all, simulating it not having come up yet
			// even though the StatefulSet as a whole is marked ready below.
			createPod(1, "10.0.0.2")
			markStatefulSetReady(2)

			result, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: key})
			Expect(err).NotTo(HaveOccurred())
			Expect(result.RequeueAfter).To(Equal(clusterPollInterval))

			var cr dnsv1alpha1.TechnitiumCluster
			Expect(k8sClient.Get(ctx, key, &cr)).To(Succeed())
			Expect(cr.Status.Phase).To(Equal(dnsv1alpha1.TechnitiumClusterPhaseClustering))
			Expect(meta.IsStatusConditionFalse(cr.Status.Conditions, dnsv1alpha1.TechnitiumClusterConditionDegraded)).To(BeTrue())

			Expect(shared.initCalls).To(Equal(0))
			Expect(shared.joinCalls).To(Equal(0))
		})
	})
})
