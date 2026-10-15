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
	"strings"
	"sync"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	dnsv1alpha1 "github.com/charg/technitium-operator/api/v1alpha1"
	"github.com/charg/technitium-operator/internal/technitium"
	"github.com/charg/technitium-operator/test/testimage"
)

// ipUpdateCall records one UpdateNodeIPAddresses call and which ordinal it
// was made against.
type ipUpdateCall struct {
	ordinal int32
	ips     []string
}

// fakeIPCluster is a fully clustered 2-node Technitium cluster whose members
// each carry the IP address the cluster has recorded for them. A pod that
// was rescheduled keeps its old recorded IP until UpdateNodeIPAddresses
// corrects it, which is the drift the controller must detect.
type fakeIPCluster struct {
	mu        sync.Mutex
	podPrefix string
	// recorded is the IP list the cluster holds for each ordinal.
	recorded map[int32][]string
	calls    []ipUpdateCall
	// updateErr, when set, is returned by every UpdateNodeIPAddresses call.
	updateErr error
}

func (f *fakeIPCluster) updates() []ipUpdateCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]ipUpdateCall(nil), f.calls...)
}

// fakeIPNode is one ordinal's nodeAPI over a shared fakeIPCluster. Every
// node reports the same membership, with its own entry marked Self, the way
// real Technitium nodes do.
type fakeIPNode struct {
	ordinal int32
	shared  *fakeIPCluster
}

func (f *fakeIPNode) GetClusterState(ctx context.Context) (*technitium.ClusterState, error) {
	f.shared.mu.Lock()
	defer f.shared.mu.Unlock()

	now := metav1.Now().Time
	nodes := make([]technitium.ClusterNode, 0, 2)
	for o := range int32(2) {
		node := technitium.ClusterNode{
			ID:               int(o) + 1,
			Name:             fmt.Sprintf("%s-%d.cluster.local", f.shared.podPrefix, o),
			IPAddresses:      f.shared.recorded[o],
			Type:             "Secondary",
			State:            "Connected",
			ConfigLastSynced: &now,
		}
		if o == 0 {
			node.Type = "Primary"
		}
		if o == f.ordinal {
			node.State = "Self"
		}
		nodes = append(nodes, node)
	}
	return &technitium.ClusterState{
		ClusterInitialized: true,
		DNSServerDomain:    nodes[f.ordinal].Name,
		ClusterDomain:      "cluster.local",
		Nodes:              nodes,
	}, nil
}

func (f *fakeIPNode) UpdateNodeIPAddresses(ctx context.Context, ipAddresses []string) (*technitium.ClusterState, error) {
	f.shared.mu.Lock()
	f.shared.calls = append(f.shared.calls, ipUpdateCall{ordinal: f.ordinal, ips: ipAddresses})
	if f.shared.updateErr != nil {
		err := f.shared.updateErr
		f.shared.mu.Unlock()
		return nil, err
	}
	f.shared.recorded[f.ordinal] = ipAddresses
	f.shared.mu.Unlock()
	return f.GetClusterState(ctx)
}

func (f *fakeIPNode) InitCluster(ctx context.Context, opts technitium.InitClusterOptions) (*technitium.ClusterState, error) {
	return nil, errors.New("InitCluster must not be called on an already clustered node")
}

func (f *fakeIPNode) InitJoinCluster(ctx context.Context, opts technitium.InitJoinOptions) (*technitium.ClusterState, error) {
	return nil, errors.New("InitJoinCluster must not be called on an already clustered node")
}

func (f *fakeIPNode) RemoveSecondary(ctx context.Context, secondaryNodeID int) error { return nil }
func (f *fakeIPNode) DeleteSecondary(ctx context.Context, secondaryNodeID int) error { return nil }
func (f *fakeIPNode) DeletePrimaryCluster(ctx context.Context, force bool) error     { return nil }

var _ = Describe("TechnitiumCluster Controller node IP refresh", func() {
	Context("When a clustered node's pod IP no longer matches the IP the cluster recorded", func() {
		ctx := context.Background()

		var (
			namespace     string
			resourceName  string
			key           types.NamespacedName
			nsCounter     int
			resourceCount int
			shared        *fakeIPCluster
		)

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
					return &fakeIPNode{ordinal: ordinal, shared: shared}, nil
				},
			}
		}

		createPod := func(ordinal int32, ip string) {
			pod := &corev1.Pod{
				Name:      fmt.Sprintf("%s-%d", resourceName, ordinal),
				Namespace: namespace,
				Labels:    instanceLabels(resourceName),
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{Name: "dns-server", Image: testimage.TechnitiumImage},
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

		// setPodIP simulates the StatefulSet recreating a pod that comes
		// back with a different IP.
		setPodIP := func(ordinal int32, ip string) {
			var got corev1.Pod
			podKey := types.NamespacedName{Namespace: namespace, Name: fmt.Sprintf("%s-%d", resourceName, ordinal)}
			Expect(k8sClient.Get(ctx, podKey, &got)).To(Succeed())
			got.Status.PodIP = ip
			Expect(k8sClient.Status().Update(ctx, &got)).To(Succeed())
		}

		markStatefulSetReady := func(readyReplicas int32) {
			var sts appsv1.StatefulSet
			stsKey := types.NamespacedName{Namespace: namespace, Name: resourceName}
			Expect(k8sClient.Get(ctx, stsKey, &sts)).To(Succeed())
			sts.Status.Replicas = readyReplicas
			sts.Status.ReadyReplicas = readyReplicas
			Expect(k8sClient.Status().Update(ctx, &sts)).To(Succeed())
		}

		reconcileOnce := func(r *TechnitiumClusterReconciler) error {
			_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: key})
			return err
		}

		BeforeEach(func() {
			nsCounter++
			namespace = fmt.Sprintf("tc-ip-refresh-ns-%d", nsCounter)
			Expect(k8sClient.Create(ctx, &corev1.Namespace{Name: namespace})).To(Succeed())

			resourceCount++
			resourceName = fmt.Sprintf("ip-refresh-cluster-%d", resourceCount)
			key = types.NamespacedName{Name: resourceName}
			shared = &fakeIPCluster{
				podPrefix: resourceName,
				recorded: map[int32][]string{
					0: {"10.0.0.1"},
					1: {"10.0.0.2"},
				},
			}

			cr := &dnsv1alpha1.TechnitiumCluster{
				Name: resourceName,
				Spec: dnsv1alpha1.TechnitiumClusterSpec{
					Image:    testimage.TechnitiumImage,
					Storage:  dnsv1alpha1.TechnitiumClusterStorageSpec{Size: resource.MustParse("1Gi")},
					Replicas: ptr.To(int32(2)),
				},
			}
			Expect(k8sClient.Create(ctx, cr)).To(Succeed())

			// First pass creates the workload; pods and readiness follow.
			r := newReconciler()
			Expect(reconcileOnce(r)).To(Succeed())
			createPod(0, "10.0.0.1")
			createPod(1, "10.0.0.2")
			markStatefulSetReady(2)
		})

		AfterEach(func() {
			cr := &dnsv1alpha1.TechnitiumCluster{}
			if err := k8sClient.Get(ctx, key, cr); err == nil {
				_ = k8sClient.Delete(ctx, cr)
			}
		})

		It("leaves a cluster whose recorded IPs match its pod IPs alone", func() {
			r := newReconciler()
			Expect(reconcileOnce(r)).To(Succeed())
			Expect(reconcileOnce(r)).To(Succeed())

			Expect(shared.updates()).To(BeEmpty())
		})

		It("updates a Secondary's IP when its pod is rescheduled onto a new IP", func() {
			setPodIP(1, "10.0.0.22")

			r := newReconciler()
			Expect(reconcileOnce(r)).To(Succeed())

			Expect(shared.updates()).To(Equal([]ipUpdateCall{
				{ordinal: 1, ips: []string{"10.0.0.22"}},
			}))
			Expect(shared.recorded[1]).To(Equal([]string{"10.0.0.22"}))
			Expect(shared.recorded[0]).To(Equal([]string{"10.0.0.1"}))
		})

		It("updates the Primary's IP when its pod is rescheduled onto a new IP", func() {
			setPodIP(0, "10.0.0.11")

			r := newReconciler()
			Expect(reconcileOnce(r)).To(Succeed())

			Expect(shared.updates()).To(Equal([]ipUpdateCall{
				{ordinal: 0, ips: []string{"10.0.0.11"}},
			}))
			Expect(shared.recorded[0]).To(Equal([]string{"10.0.0.11"}))
		})

		It("updates every drifted node once, then stops once the cluster has converged", func() {
			setPodIP(0, "10.0.0.11")
			setPodIP(1, "10.0.0.22")

			r := newReconciler()
			Expect(reconcileOnce(r)).To(Succeed())
			Expect(shared.updates()).To(ConsistOf(
				ipUpdateCall{ordinal: 0, ips: []string{"10.0.0.11"}},
				ipUpdateCall{ordinal: 1, ips: []string{"10.0.0.22"}},
			))

			Expect(reconcileOnce(r)).To(Succeed())
			Expect(shared.updates()).To(HaveLen(2), "a converged cluster must not be updated again")

			var cr dnsv1alpha1.TechnitiumCluster
			Expect(k8sClient.Get(ctx, key, &cr)).To(Succeed())
			Expect(cr.Status.Phase).To(Equal(dnsv1alpha1.TechnitiumClusterPhaseReady))
			Expect(cr.Status.Members).To(Equal("2/2"))
		})

		It("reports the failure when Technitium rejects the IP update", func() {
			setPodIP(1, "10.0.0.22")
			shared.updateErr = errors.New("update refused")

			r := newReconciler()
			err := reconcileOnce(r)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("update refused"))
		})
	})
})
