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
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	dnsv1alpha1 "github.com/charg/technitium-operator/api/v1alpha1"
	"github.com/charg/technitium-operator/internal/technitium"
)

var _ = Describe("TechnitiumCluster Controller multi-replica provisioning", func() {
	Context("When spec.replicas is greater than 1", func() {
		ctx := context.Background()

		var (
			namespace     string
			resourceName  string
			key           types.NamespacedName
			nsCounter     int
			resourceCount int
		)

		newNamespace := func() string {
			nsCounter++
			name := fmt.Sprintf("tc-replicas-ns-%d", nsCounter)
			ns := &corev1.Namespace{Name: name}
			Expect(k8sClient.Create(ctx, ns)).To(Succeed())
			return name
		}

		// newReconciler wires a fake bootstrap client that always mints a
		// token and a fake node client that reports every ordinal as
		// uninitialized, so a multi-replica reconcile never dials the
		// unreachable per-pod endpoints envtest cannot serve.
		newReconciler := func() *TechnitiumClusterReconciler {
			return &TechnitiumClusterReconciler{
				Client:            k8sClient,
				Scheme:            k8sClient.Scheme(),
				OperatorNamespace: namespace,
				NewBootstrapClient: func(endpoint, username, password string) (bootstrapAPI, error) {
					return &fakeBootstrapClient{calls: new(int), token: "minted-token"}, nil
				},
				NewNodeClient: func(endpoint, username, password string) (nodeAPI, error) {
					return &fakeNodeClient{state: &technitium.ClusterState{
						Version:            "13.4.0",
						DNSServerDomain:    resourceName,
						ClusterInitialized: false,
					}}, nil
				},
			}
		}

		storageSize := resource.MustParse("1Gi")

		createClusterCR := func(replicas int32) *dnsv1alpha1.TechnitiumCluster {
			resourceCount++
			resourceName = fmt.Sprintf("replica-cluster-%d", resourceCount)
			key = types.NamespacedName{Name: resourceName}

			cr := &dnsv1alpha1.TechnitiumCluster{
				Name: resourceName,
				Spec: dnsv1alpha1.TechnitiumClusterSpec{
					Image:    "technitium/dns-server:15.4.0",
					Storage:  dnsv1alpha1.TechnitiumClusterStorageSpec{Size: storageSize},
					Replicas: ptr.To(replicas),
				},
			}
			Expect(k8sClient.Create(ctx, cr)).To(Succeed())
			return cr
		}

		// markStatefulSetReady simulates kubelet reporting readyReplicas pods
		// ready: envtest runs no kubelet, so nothing else will ever populate
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
		})

		AfterEach(func() {
			cr := &dnsv1alpha1.TechnitiumCluster{}
			if err := k8sClient.Get(ctx, key, cr); err == nil {
				_ = k8sClient.Delete(ctx, cr)
			}
		})

		It("scales the StatefulSet to spec.replicas with a single volumeClaimTemplate", func() {
			createClusterCR(3)
			_, err := newReconciler().Reconcile(ctx, reconcile.Request{NamespacedName: key})
			Expect(err).NotTo(HaveOccurred())

			var sts appsv1.StatefulSet
			Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: namespace, Name: resourceName}, &sts)).To(Succeed())

			Expect(*sts.Spec.Replicas).To(Equal(int32(3)))
			// PVC-per-pod is the StatefulSet's own behavior once it has a
			// volumeClaimTemplate: asserting the template exists plus the
			// replica count is what confirms three independent PVCs will be
			// created, one per ordinal.
			Expect(sts.Spec.VolumeClaimTemplates).To(HaveLen(1))
			Expect(sts.Spec.VolumeClaimTemplates[0].Name).To(Equal("data"))
		})

		It("stays Provisioning when only some replicas are ready, and advances to Clustering once all are ready", func() {
			createClusterCR(3)
			r := newReconciler()
			_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: key})
			Expect(err).NotTo(HaveOccurred())

			markStatefulSetReady(2)
			_, err = r.Reconcile(ctx, reconcile.Request{NamespacedName: key})
			Expect(err).NotTo(HaveOccurred())

			var cr dnsv1alpha1.TechnitiumCluster
			Expect(k8sClient.Get(ctx, key, &cr)).To(Succeed())
			Expect(cr.Status.Phase).To(Equal(dnsv1alpha1.TechnitiumClusterPhaseProvisioning))
			Expect(cr.Status.ReadyReplicas).To(Equal(int32(2)))

			markStatefulSetReady(3)
			_, err = r.Reconcile(ctx, reconcile.Request{NamespacedName: key})
			Expect(err).NotTo(HaveOccurred())

			// This test never creates Pod objects (envtest runs no
			// StatefulSet controller), so reconcileClustering can never
			// resolve a primary IP and clustering never converges here: it
			// stays Clustering rather than reaching Ready. Coverage of the
			// full init/join path through to Ready lives in
			// technitiumcluster_clustering_test.go, which does create Pods.
			Expect(k8sClient.Get(ctx, key, &cr)).To(Succeed())
			Expect(cr.Status.Phase).To(Equal(dnsv1alpha1.TechnitiumClusterPhaseClustering))
			Expect(cr.Status.ReadyReplicas).To(Equal(int32(3)))
		})

		It("reports one status.Nodes entry per replica and status.Members as N/N once all are ready", func() {
			createClusterCR(3)
			r := newReconciler()
			_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: key})
			Expect(err).NotTo(HaveOccurred())

			markStatefulSetReady(3)
			_, err = r.Reconcile(ctx, reconcile.Request{NamespacedName: key})
			Expect(err).NotTo(HaveOccurred())

			var cr dnsv1alpha1.TechnitiumCluster
			Expect(k8sClient.Get(ctx, key, &cr)).To(Succeed())
			Expect(cr.Status.Nodes).To(HaveLen(3))
			for i, node := range cr.Status.Nodes {
				Expect(node.Name).To(Equal(fmt.Sprintf("%s-%d", resourceName, i)))
			}
			Expect(cr.Status.Nodes[0].Role).To(Equal("Primary"))
			Expect(cr.Status.Nodes[1].Role).To(Equal("Secondary"))
			Expect(cr.Status.Nodes[2].Role).To(Equal("Secondary"))
			// Each standalone node counts as joined via the readiness
			// placeholder (nodeReadyState) until real cluster init/join
			// exists, so all three count toward Members here.
			Expect(cr.Status.Members).To(Equal("3/3"))
		})
	})
})
