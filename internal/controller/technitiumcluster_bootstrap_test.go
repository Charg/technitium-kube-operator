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

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	dnsv1alpha1 "github.com/charg/technitium-operator/api/v1alpha1"
)

// fakeBootstrapClient is a bootstrapAPI stand-in that lets tests control
// whether token minting succeeds, without ever making an HTTP call. Real
// wire-level coverage of CreateToken lives in internal/technitium.
type fakeBootstrapClient struct {
	calls   *int
	fail    bool
	token   string
	failErr error
}

func (f *fakeBootstrapClient) CreateToken(ctx context.Context, tokenName string) (string, error) {
	*f.calls++
	if f.fail {
		if f.failErr != nil {
			return "", f.failErr
		}
		return "", errors.New("admin credentials not yet applied")
	}
	return f.token, nil
}

var _ = Describe("TechnitiumCluster Controller bootstrap", func() {
	Context("When the workload becomes ready", func() {
		ctx := context.Background()

		var (
			namespace     string
			resourceName  string
			key           types.NamespacedName
			nsCounter     int
			resourceCount int
			callCount     int
			fail          bool
		)

		newNamespace := func() string {
			nsCounter++
			name := fmt.Sprintf("tc-boot-ns-%d", nsCounter)
			ns := &corev1.Namespace{Name: name}
			Expect(k8sClient.Create(ctx, ns)).To(Succeed())
			return name
		}

		newReconciler := func() *TechnitiumClusterReconciler {
			return &TechnitiumClusterReconciler{
				Client:            k8sClient,
				Scheme:            k8sClient.Scheme(),
				OperatorNamespace: namespace,
				NewBootstrapClient: func(endpoint, username, password string) (bootstrapAPI, error) {
					return &fakeBootstrapClient{calls: &callCount, fail: fail, token: "minted-token"}, nil
				},
			}
		}

		storageSize := resource.MustParse("1Gi")

		createClusterCR := func() *dnsv1alpha1.TechnitiumCluster {
			resourceCount++
			resourceName = fmt.Sprintf("boot-cluster-%d", resourceCount)
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

		adminSecretKey := func() types.NamespacedName {
			return types.NamespacedName{Namespace: namespace, Name: resourceName + "-admin"}
		}

		BeforeEach(func() {
			namespace = newNamespace()
			callCount = 0
			fail = false
		})

		AfterEach(func() {
			cr := &dnsv1alpha1.TechnitiumCluster{}
			if err := k8sClient.Get(ctx, key, cr); err == nil {
				_ = k8sClient.Delete(ctx, cr)
			}
		})

		It("stays Provisioning and never attempts a token mint while no replicas are ready", func() {
			createClusterCR()
			r := newReconciler()
			result, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: key})
			Expect(err).NotTo(HaveOccurred())
			Expect(result.RequeueAfter).To(Equal(clusterPollInterval))

			var cr dnsv1alpha1.TechnitiumCluster
			Expect(k8sClient.Get(ctx, key, &cr)).To(Succeed())
			Expect(cr.Status.Phase).To(Equal(dnsv1alpha1.TechnitiumClusterPhaseProvisioning))
			Expect(callCount).To(Equal(0))
		})

		It("mints a token and reaches phase Ready once replicas report ready", func() {
			createClusterCR()
			r := newReconciler()
			_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: key})
			Expect(err).NotTo(HaveOccurred())

			markStatefulSetReady(1)

			result, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: key})
			Expect(err).NotTo(HaveOccurred())
			Expect(result.RequeueAfter).To(Equal(clusterDriftInterval))

			var cr dnsv1alpha1.TechnitiumCluster
			Expect(k8sClient.Get(ctx, key, &cr)).To(Succeed())
			Expect(cr.Status.Phase).To(Equal(dnsv1alpha1.TechnitiumClusterPhaseReady))
			Expect(meta.IsStatusConditionTrue(cr.Status.Conditions, dnsv1alpha1.TechnitiumClusterConditionAvailable)).To(BeTrue())

			var secret corev1.Secret
			Expect(k8sClient.Get(ctx, adminSecretKey(), &secret)).To(Succeed())
			Expect(string(secret.Data["token"])).To(Equal("minted-token"))
			Expect(callCount).To(Equal(1))
		})

		It("does not mint a second token on a re-reconcile of an already-bootstrapped instance", func() {
			createClusterCR()
			r := newReconciler()
			_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: key})
			Expect(err).NotTo(HaveOccurred())
			markStatefulSetReady(1)
			_, err = r.Reconcile(ctx, reconcile.Request{NamespacedName: key})
			Expect(err).NotTo(HaveOccurred())
			Expect(callCount).To(Equal(1))

			_, err = r.Reconcile(ctx, reconcile.Request{NamespacedName: key})
			Expect(err).NotTo(HaveOccurred())

			var cr dnsv1alpha1.TechnitiumCluster
			Expect(k8sClient.Get(ctx, key, &cr)).To(Succeed())
			Expect(cr.Status.Phase).To(Equal(dnsv1alpha1.TechnitiumClusterPhaseReady))
			Expect(callCount).To(Equal(1))
		})

		It("stays Bootstrapping and requeues without error while the admin password is not yet applied, then converges to Ready", func() {
			createClusterCR()
			r := newReconciler()
			_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: key})
			Expect(err).NotTo(HaveOccurred())
			markStatefulSetReady(1)

			fail = true
			result, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: key})
			Expect(err).NotTo(HaveOccurred())
			Expect(result.RequeueAfter).To(Equal(clusterPollInterval))

			var cr dnsv1alpha1.TechnitiumCluster
			Expect(k8sClient.Get(ctx, key, &cr)).To(Succeed())
			Expect(cr.Status.Phase).To(Equal(dnsv1alpha1.TechnitiumClusterPhaseBootstrapping))
			Expect(meta.IsStatusConditionFalse(cr.Status.Conditions, dnsv1alpha1.TechnitiumClusterConditionAvailable)).To(BeTrue())

			var secret corev1.Secret
			Expect(k8sClient.Get(ctx, adminSecretKey(), &secret)).To(Succeed())
			Expect(secret.Data["token"]).To(BeEmpty())

			fail = false
			_, err = r.Reconcile(ctx, reconcile.Request{NamespacedName: key})
			Expect(err).NotTo(HaveOccurred())

			Expect(k8sClient.Get(ctx, key, &cr)).To(Succeed())
			Expect(cr.Status.Phase).To(Equal(dnsv1alpha1.TechnitiumClusterPhaseReady))
			Expect(meta.IsStatusConditionTrue(cr.Status.Conditions, dnsv1alpha1.TechnitiumClusterConditionAvailable)).To(BeTrue())
		})
	})
})
