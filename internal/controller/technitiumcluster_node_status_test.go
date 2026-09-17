/*
Copyright (c) 2026 Chris

Licensed under the MIT License. See the LICENSE file in the project root for
the full license text.
*/

package controller

import (
	"context"
	"fmt"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	dnsv1alpha1 "github.com/charg/technitium-operator/api/v1alpha1"
	"github.com/charg/technitium-operator/internal/technitium"
)

// fakeNodeClient is a nodeAPI stand-in that reports a canned cluster state
// without making an HTTP call. Real wire-level coverage of GetClusterState,
// InitCluster, and InitJoinCluster lives in internal/technitium.
//
// initCalls and initJoinCalls are pointers, mirroring fakeBootstrapClient's
// "calls" field, since NewNodeClient constructs a fresh fakeNodeClient per
// endpoint dialed: a plain int field would never accumulate across the
// several ordinals a clustering reconcile touches, only a shared counter
// closed over by the test does.
type fakeNodeClient struct {
	state *technitium.ClusterState
	err   error

	initCalls *int
	initErr   error
	initState *technitium.ClusterState
	joinCalls *int
	joinErr   error
	joinState *technitium.ClusterState
}

func (f *fakeNodeClient) GetClusterState(ctx context.Context) (*technitium.ClusterState, error) {
	return f.state, f.err
}

func (f *fakeNodeClient) InitCluster(ctx context.Context, opts technitium.InitClusterOptions) (*technitium.ClusterState, error) {
	if f.initCalls != nil {
		*f.initCalls++
	}
	if f.initErr != nil {
		return nil, f.initErr
	}
	if f.initState != nil {
		return f.initState, nil
	}
	return f.state, nil
}

func (f *fakeNodeClient) InitJoinCluster(ctx context.Context, opts technitium.InitJoinOptions) (*technitium.ClusterState, error) {
	if f.joinCalls != nil {
		*f.joinCalls++
	}
	if f.joinErr != nil {
		return nil, f.joinErr
	}
	if f.joinState != nil {
		return f.joinState, nil
	}
	return f.state, nil
}

var _ = Describe("TechnitiumCluster Controller node status", func() {
	Context("Once the workload is ready", func() {
		ctx := context.Background()

		var (
			namespace     string
			resourceName  string
			key           types.NamespacedName
			nsCounter     int
			resourceCount int
			gotEndpoints  []string
		)

		newNamespace := func() string {
			nsCounter++
			name := fmt.Sprintf("tc-node-ns-%d", nsCounter)
			ns := &corev1.Namespace{Name: name}
			Expect(k8sClient.Create(ctx, ns)).To(Succeed())
			return name
		}

		// newReconciler wires a fake bootstrap client (bootstrap runs in the
		// same reconcile pass as the node status read once the workload is
		// ready) alongside the fake node client under test, so neither seam
		// ever dials the unreachable per-pod endpoints envtest cannot serve.
		newReconciler := func() *TechnitiumClusterReconciler {
			return &TechnitiumClusterReconciler{
				Client:            k8sClient,
				Scheme:            k8sClient.Scheme(),
				OperatorNamespace: namespace,
				NewBootstrapClient: func(endpoint, username, password string) (bootstrapAPI, error) {
					return &fakeBootstrapClient{calls: new(int), token: "minted-token"}, nil
				},
				NewNodeClient: func(endpoint, username, password string) (nodeAPI, error) {
					gotEndpoints = append(gotEndpoints, endpoint)
					return &fakeNodeClient{state: &technitium.ClusterState{
						Version:            "13.4.0",
						DNSServerDomain:    resourceName,
						ClusterInitialized: false,
					}}, nil
				},
			}
		}

		storageSize := resource.MustParse("1Gi")

		createClusterCR := func() *dnsv1alpha1.TechnitiumCluster {
			resourceCount++
			resourceName = fmt.Sprintf("node-cluster-%d", resourceCount)
			key = types.NamespacedName{Name: resourceName}

			cr := &dnsv1alpha1.TechnitiumCluster{
				Name: resourceName,
				Spec: dnsv1alpha1.TechnitiumClusterSpec{
					Image:   "technitium/dns-server:15.4.0",
					Storage: dnsv1alpha1.TechnitiumClusterStorageSpec{Size: storageSize},
				},
			}
			Expect(k8sClient.Create(ctx, cr)).To(Succeed())
			return cr
		}

		// markStatefulSetReady simulates kubelet reporting the pod ready:
		// envtest runs no kubelet, so nothing else will ever populate
		// ReadyReplicas on its own.
		markStatefulSetReady := func(readyReplicas int32) {
			var sts appsv1.StatefulSet
			stsKey := types.NamespacedName{Namespace: namespace, Name: resourceName}
			Expect(k8sClient.Get(ctx, stsKey, &sts)).To(Succeed())
			sts.Status.Replicas = readyReplicas
			sts.Status.ReadyReplicas = readyReplicas
			Expect(k8sClient.Status().Update(ctx, &sts)).To(Succeed())
		}

		BeforeEach(func() {
			namespace = newNamespace()
			gotEndpoints = nil
		})

		AfterEach(func() {
			cr := &dnsv1alpha1.TechnitiumCluster{}
			if err := k8sClient.Get(ctx, key, cr); err == nil {
				_ = k8sClient.Delete(ctx, cr)
			}
		})

		It("reports the single node as Primary with Members 1/1 while uninitialized", func() {
			createClusterCR()
			r := newReconciler()
			_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: key})
			Expect(err).NotTo(HaveOccurred())

			markStatefulSetReady(1)
			_, err = r.Reconcile(ctx, reconcile.Request{NamespacedName: key})
			Expect(err).NotTo(HaveOccurred())

			var cr dnsv1alpha1.TechnitiumCluster
			Expect(k8sClient.Get(ctx, key, &cr)).To(Succeed())
			Expect(cr.Status.Nodes).To(HaveLen(1))
			Expect(cr.Status.Nodes[0].Name).To(Equal(resourceName + "-0"))
			Expect(cr.Status.Nodes[0].Role).To(Equal("Primary"))
			Expect(cr.Status.Members).To(Equal("1/1"))
		})

		It("queries the ordinal-0 pod through its own per-node endpoint", func() {
			createClusterCR()
			r := newReconciler()
			_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: key})
			Expect(err).NotTo(HaveOccurred())

			markStatefulSetReady(1)
			_, err = r.Reconcile(ctx, reconcile.Request{NamespacedName: key})
			Expect(err).NotTo(HaveOccurred())

			wantEndpoint := fmt.Sprintf("http://%s-0.%s-headless.%s.svc:5380", resourceName, resourceName, namespace)
			Expect(gotEndpoints).To(ContainElement(wantEndpoint))
		})

		It("never queries node state while the workload is not yet ready", func() {
			createClusterCR()
			r := newReconciler()
			_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: key})
			Expect(err).NotTo(HaveOccurred())

			var cr dnsv1alpha1.TechnitiumCluster
			Expect(k8sClient.Get(ctx, key, &cr)).To(Succeed())
			Expect(cr.Status.Nodes).To(BeEmpty())
			Expect(gotEndpoints).To(BeEmpty())
		})
	})
})
