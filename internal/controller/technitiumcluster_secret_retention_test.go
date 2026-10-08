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
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	dnsv1alpha1 "github.com/charg/technitium-operator/api/v1alpha1"
	"github.com/charg/technitium-operator/internal/technitium"
)

// envtest runs no garbage collector, so these tests assert on the Secret's
// owner references (the only thing GC would act on) rather than on its
// disappearance.
var _ = Describe("TechnitiumCluster Controller admin Secret retention", func() {
	Context("When a TechnitiumCluster with a generated admin Secret is deleted", func() {
		ctx := context.Background()

		var (
			namespace     string
			resourceName  string
			key           types.NamespacedName
			nsCounter     int
			resourceCount int
			createErr     error
		)

		newNamespace := func() string {
			nsCounter++
			name := fmt.Sprintf("tc-secret-ns-%d", nsCounter)
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
					return &fakeBootstrapClient{calls: new(int), fail: createErr != nil, failErr: createErr, token: "minted-token"}, nil
				},
				NewNodeClient: func(endpoint, username, password string) (nodeAPI, error) {
					return &fakeNodeClient{
						state:                &technitium.ClusterState{},
						removeSecondaryCalls: new(int),
						removeSecondaryIDs:   new([]int),
						deleteSecondaryCalls: new(int),
						deleteSecondaryIDs:   new([]int),
						deletePrimaryCalls:   new(int),
					}, nil
				},
			}
		}

		storageSize := resource.MustParse("1Gi")

		createClusterCR := func(mutate func(*dnsv1alpha1.TechnitiumCluster)) {
			cr := &dnsv1alpha1.TechnitiumCluster{
				Name: resourceName,
				Spec: dnsv1alpha1.TechnitiumClusterSpec{
					Image:   "technitium/dns-server:15.4.0",
					Storage: dnsv1alpha1.TechnitiumClusterStorageSpec{Size: storageSize},
				},
			}
			if mutate != nil {
				mutate(cr)
			}
			Expect(k8sClient.Create(ctx, cr)).To(Succeed())
		}

		reconcileOnce := func(r *TechnitiumClusterReconciler) {
			_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: key})
			Expect(err).NotTo(HaveOccurred())
		}

		deleteAndFinalize := func(r *TechnitiumClusterReconciler) {
			var cr dnsv1alpha1.TechnitiumCluster
			Expect(k8sClient.Get(ctx, key, &cr)).To(Succeed())
			Expect(k8sClient.Delete(ctx, &cr)).To(Succeed())
			reconcileOnce(r)

			err := k8sClient.Get(ctx, key, &cr)
			Expect(apierrors.IsNotFound(err)).To(BeTrue(), "expected the TechnitiumCluster to be gone once the finalizer clears, got: %v", err)
		}

		getSecret := func(name string) *corev1.Secret {
			var secret corev1.Secret
			Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, &secret)).To(Succeed())
			return &secret
		}

		generatedSecret := func() *corev1.Secret { return getSecret(resourceName + "-admin") }

		BeforeEach(func() {
			namespace = newNamespace()
			createErr = nil
			resourceCount++
			resourceName = fmt.Sprintf("secret-cluster-%d", resourceCount)
			key = types.NamespacedName{Name: resourceName}
		})

		AfterEach(func() {
			cr := &dnsv1alpha1.TechnitiumCluster{}
			if err := k8sClient.Get(ctx, key, cr); err == nil {
				_ = k8sClient.Delete(ctx, cr)
			}
		})

		It("releases the generated Secret's owner reference under the default Retain policy", func() {
			createClusterCR(nil)
			r := newReconciler()
			reconcileOnce(r)
			Expect(metav1.GetControllerOf(generatedSecret())).NotTo(BeNil())

			deleteAndFinalize(r)

			secret := generatedSecret()
			Expect(secret.OwnerReferences).To(BeEmpty())
			Expect(secret.Data).To(HaveKey("password"))
		})

		It("keeps the owner reference under Delete so the Secret is garbage collected", func() {
			createClusterCR(func(cr *dnsv1alpha1.TechnitiumCluster) {
				cr.Spec.Storage.RetentionPolicy = dnsv1alpha1.PVCRetentionPolicyDelete
			})
			r := newReconciler()
			reconcileOnce(r)

			deleteAndFinalize(r)

			ref := metav1.GetControllerOf(generatedSecret())
			Expect(ref).NotTo(BeNil())
			Expect(ref.Kind).To(Equal("TechnitiumCluster"))
			Expect(ref.Name).To(Equal(resourceName))
		})

		It("never touches a caller-supplied adminSecretRef Secret", func() {
			supplied := &corev1.Secret{
				Name:      "my-admin",
				Namespace: namespace,
				Data:      map[string][]byte{"username": []byte("admin"), "password": []byte("hunter2")},
				// An unrelated owner reference stands in for anything the
				// caller manages; it must survive deletion untouched.
				OwnerReferences: []metav1.OwnerReference{{
					APIVersion: "v1", Kind: "ConfigMap", Name: "owner", UID: "11111111-1111-1111-1111-111111111111",
				}},
			}
			Expect(k8sClient.Create(ctx, supplied)).To(Succeed())

			createClusterCR(func(cr *dnsv1alpha1.TechnitiumCluster) {
				cr.Spec.AdminSecretRef = &dnsv1alpha1.SecretReference{Name: "my-admin"}
			})
			r := newReconciler()
			reconcileOnce(r)

			deleteAndFinalize(r)

			after := getSecret("my-admin")
			Expect(after.OwnerReferences).To(Equal(supplied.OwnerReferences))
			Expect(string(after.Data["password"])).To(Equal("hunter2"))
			var generated corev1.Secret
			err := k8sClient.Get(ctx, types.NamespacedName{Namespace: namespace, Name: resourceName + "-admin"}, &generated)
			Expect(apierrors.IsNotFound(err)).To(BeTrue(), "no generated Secret should exist when adminSecretRef is set")
		})

		It("finalizes cleanly when the generated Secret is already gone", func() {
			createClusterCR(nil)
			r := newReconciler()
			reconcileOnce(r)
			Expect(k8sClient.Delete(ctx, generatedSecret())).To(Succeed())

			deleteAndFinalize(r)
		})

		It("re-adopts the retained Secret on recreation, keeping its password and token", func() {
			createClusterCR(nil)
			r := newReconciler()
			reconcileOnce(r)
			original := generatedSecret()
			password := string(original.Data["password"])
			original.Data["token"] = []byte("minted-token")
			Expect(k8sClient.Update(ctx, original)).To(Succeed())

			deleteAndFinalize(r)
			Expect(generatedSecret().OwnerReferences).To(BeEmpty())

			createClusterCR(nil)
			var recreated dnsv1alpha1.TechnitiumCluster
			Expect(k8sClient.Get(ctx, key, &recreated)).To(Succeed())
			reconcileOnce(r)

			secret := generatedSecret()
			ref := metav1.GetControllerOf(secret)
			Expect(ref).NotTo(BeNil())
			Expect(ref.UID).To(Equal(recreated.UID))
			Expect(string(secret.Data["password"])).To(Equal(password))
			Expect(string(secret.Data["token"])).To(Equal("minted-token"))
		})

		It("re-adopts a Secret whose owner reference still carries a stale UID", func() {
			// Delete policy keeps the old owner reference, and without a GC
			// the Secret outlives the CR just as it would in the window
			// before the real garbage collector reaps it.
			createClusterCR(func(cr *dnsv1alpha1.TechnitiumCluster) {
				cr.Spec.Storage.RetentionPolicy = dnsv1alpha1.PVCRetentionPolicyDelete
			})
			r := newReconciler()
			reconcileOnce(r)
			password := string(generatedSecret().Data["password"])
			staleUID := metav1.GetControllerOf(generatedSecret()).UID
			deleteAndFinalize(r)

			createClusterCR(nil)
			var recreated dnsv1alpha1.TechnitiumCluster
			Expect(k8sClient.Get(ctx, key, &recreated)).To(Succeed())
			Expect(recreated.UID).NotTo(Equal(staleUID))
			reconcileOnce(r)

			secret := generatedSecret()
			Expect(secret.OwnerReferences).To(HaveLen(1))
			Expect(secret.OwnerReferences[0].UID).To(Equal(recreated.UID))
			Expect(string(secret.Data["password"])).To(Equal(password))
		})

		It("goes Degraded with AdminCredentialsRejected when the server rejects the Secret's password", func() {
			createClusterCR(nil)
			r := newReconciler()
			reconcileOnce(r)

			var sts appsv1.StatefulSet
			Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: namespace, Name: resourceName}, &sts)).To(Succeed())
			sts.Status.Replicas = 1
			sts.Status.ReadyReplicas = 1
			Expect(k8sClient.Status().Update(ctx, &sts)).To(Succeed())

			createErr = fmt.Errorf("wrapped: %w", technitium.ErrInvalidCredentials)
			_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: key})
			Expect(err).To(HaveOccurred())

			var cr dnsv1alpha1.TechnitiumCluster
			Expect(k8sClient.Get(ctx, key, &cr)).To(Succeed())
			degraded := meta.FindStatusCondition(cr.Status.Conditions, dnsv1alpha1.TechnitiumClusterConditionDegraded)
			Expect(degraded).NotTo(BeNil())
			Expect(degraded.Status).To(Equal(metav1.ConditionTrue))
			Expect(degraded.Reason).To(Equal("AdminCredentialsRejected"))
			Expect(degraded.Message).To(ContainSubstring("retained PVC"))
		})
	})
})
