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
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	dnsv1alpha1 "github.com/charg/technitium-operator/api/v1alpha1"
)

var _ = Describe("TechnitiumCluster Controller", func() {
	Context("When reconciling a resource", func() {
		ctx := context.Background()

		var (
			namespace     string
			resourceName  string
			key           types.NamespacedName
			nsCounter     int
			resourceCount int
		)

		// TechnitiumCluster is cluster-scoped, so its lookup key carries no
		// namespace, but its owned objects live in a namespace this test
		// creates: a fresh one per test isolates the workload objects the
		// same way a fresh CR name isolates the CR itself.
		newNamespace := func() string {
			nsCounter++
			name := fmt.Sprintf("tc-test-ns-%d", nsCounter)
			ns := &corev1.Namespace{Name: name}
			Expect(k8sClient.Create(ctx, ns)).To(Succeed())
			return name
		}

		newReconciler := func() *TechnitiumClusterReconciler {
			return &TechnitiumClusterReconciler{
				Client:            k8sClient,
				Scheme:            k8sClient.Scheme(),
				OperatorNamespace: namespace,
			}
		}

		storageSize := resource.MustParse("1Gi")

		createClusterCR := func(mutate func(*dnsv1alpha1.TechnitiumCluster)) *dnsv1alpha1.TechnitiumCluster {
			resourceCount++
			resourceName = fmt.Sprintf("test-cluster-%d", resourceCount)
			key = types.NamespacedName{Name: resourceName}

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
			return cr
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

		It("provisions the StatefulSet, both Services, and the admin Secret", func() {
			createClusterCR(nil)
			_, err := newReconciler().Reconcile(ctx, reconcile.Request{NamespacedName: key})
			Expect(err).NotTo(HaveOccurred())

			var cr dnsv1alpha1.TechnitiumCluster
			Expect(k8sClient.Get(ctx, key, &cr)).To(Succeed())

			var sts appsv1.StatefulSet
			Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: namespace, Name: resourceName}, &sts)).To(Succeed())
			Expect(sts.OwnerReferences).To(ContainElement(HaveField("Name", cr.Name)))
			Expect(sts.OwnerReferences[0].Controller).NotTo(BeNil())
			Expect(*sts.OwnerReferences[0].Controller).To(BeTrue())

			var clientSvc corev1.Service
			Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: namespace, Name: resourceName}, &clientSvc)).To(Succeed())
			Expect(clientSvc.OwnerReferences).To(ContainElement(HaveField("Name", cr.Name)))

			var headlessSvc corev1.Service
			Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: namespace, Name: resourceName + "-headless"}, &headlessSvc)).To(Succeed())
			Expect(headlessSvc.Spec.ClusterIP).To(Equal(corev1.ClusterIPNone))
			Expect(headlessSvc.OwnerReferences).To(ContainElement(HaveField("Name", cr.Name)))

			var secret corev1.Secret
			Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: namespace, Name: resourceName + "-admin"}, &secret)).To(Succeed())
			Expect(secret.OwnerReferences).To(ContainElement(HaveField("Name", cr.Name)))
			Expect(string(secret.Data["username"])).To(Equal("admin"))
			Expect(secret.Data["password"]).NotTo(BeEmpty())
		})

		It("builds a StatefulSet matching the spec", func() {
			createClusterCR(nil)
			_, err := newReconciler().Reconcile(ctx, reconcile.Request{NamespacedName: key})
			Expect(err).NotTo(HaveOccurred())

			var sts appsv1.StatefulSet
			Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: namespace, Name: resourceName}, &sts)).To(Succeed())

			Expect(*sts.Spec.Replicas).To(Equal(int32(1)))
			Expect(sts.Spec.Template.Spec.Containers).To(HaveLen(1))
			container := sts.Spec.Template.Spec.Containers[0]
			Expect(container.Image).To(Equal("technitium/dns-server:15.4.0"))

			envNames := make([]string, 0, len(container.Env))
			for _, e := range container.Env {
				envNames = append(envNames, e.Name)
			}
			Expect(envNames).To(ContainElement("DNS_SERVER_ADMIN_PASSWORD_FILE"))

			Expect(container.VolumeMounts).To(ContainElement(corev1.VolumeMount{
				Name: "data", MountPath: "/etc/dns",
			}))

			Expect(sts.Spec.VolumeClaimTemplates).To(HaveLen(1))
			Expect(sts.Spec.VolumeClaimTemplates[0].Name).To(Equal("data"))
			Expect(sts.Spec.VolumeClaimTemplates[0].Spec.Resources.Requests.Storage().String()).To(Equal(storageSize.String()))
		})

		It("runs the workload under the restricted Pod Security Standard", func() {
			createClusterCR(nil)
			_, err := newReconciler().Reconcile(ctx, reconcile.Request{NamespacedName: key})
			Expect(err).NotTo(HaveOccurred())

			var sts appsv1.StatefulSet
			Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: namespace, Name: resourceName}, &sts)).To(Succeed())
			podSpec := sts.Spec.Template.Spec

			Expect(podSpec.SecurityContext).NotTo(BeNil())
			Expect(*podSpec.SecurityContext.RunAsNonRoot).To(BeTrue())
			Expect(*podSpec.SecurityContext.RunAsUser).To(Equal(int64(1000)))
			Expect(*podSpec.SecurityContext.FSGroup).To(Equal(int64(1000)))
			Expect(podSpec.SecurityContext.SeccompProfile.Type).To(Equal(corev1.SeccompProfileTypeRuntimeDefault))

			container := podSpec.Containers[0]
			Expect(container.SecurityContext).NotTo(BeNil())
			Expect(*container.SecurityContext.AllowPrivilegeEscalation).To(BeFalse())
			Expect(container.SecurityContext.Capabilities.Drop).To(ConsistOf(corev1.Capability("ALL")))
			// Port 53 binding requires NET_BIND_SERVICE, the one capability the
			// restricted profile permits adding back.
			Expect(container.SecurityContext.Capabilities.Add).To(ConsistOf(corev1.Capability("NET_BIND_SERVICE")))

			// Technitium writes to /var/log/technitium and /tmp, which a non-root
			// process cannot create on the container root filesystem.
			Expect(container.VolumeMounts).To(ContainElement(corev1.VolumeMount{Name: "varlog", MountPath: "/var/log/technitium"}))
			Expect(container.VolumeMounts).To(ContainElement(corev1.VolumeMount{Name: "tmp", MountPath: "/tmp"}))
			Expect(podSpec.Volumes).To(ContainElement(HaveField("Name", "varlog")))
			Expect(podSpec.Volumes).To(ContainElement(HaveField("Name", "tmp")))
		})

		It("corrects a hand-edited image and replica count back to spec", func() {
			createClusterCR(nil)
			r := newReconciler()
			_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: key})
			Expect(err).NotTo(HaveOccurred())

			var sts appsv1.StatefulSet
			stsKey := types.NamespacedName{Namespace: namespace, Name: resourceName}
			Expect(k8sClient.Get(ctx, stsKey, &sts)).To(Succeed())

			sts.Spec.Template.Spec.Containers[0].Image = "someone-else/hand-edited:latest"
			drifted := int32(1)
			sts.Spec.Replicas = &drifted
			Expect(k8sClient.Update(ctx, &sts)).To(Succeed())

			_, err = r.Reconcile(ctx, reconcile.Request{NamespacedName: key})
			Expect(err).NotTo(HaveOccurred())

			Expect(k8sClient.Get(ctx, stsKey, &sts)).To(Succeed())
			Expect(sts.Spec.Template.Spec.Containers[0].Image).To(Equal("technitium/dns-server:15.4.0"))
			Expect(*sts.Spec.Replicas).To(Equal(int32(1)))
		})

		It("never regenerates an existing admin password", func() {
			createClusterCR(nil)
			r := newReconciler()
			_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: key})
			Expect(err).NotTo(HaveOccurred())

			secretKey := types.NamespacedName{Namespace: namespace, Name: resourceName + "-admin"}
			var secret corev1.Secret
			Expect(k8sClient.Get(ctx, secretKey, &secret)).To(Succeed())
			originalPassword := string(secret.Data["password"])
			Expect(originalPassword).NotTo(BeEmpty())

			_, err = r.Reconcile(ctx, reconcile.Request{NamespacedName: key})
			Expect(err).NotTo(HaveOccurred())

			Expect(k8sClient.Get(ctx, secretKey, &secret)).To(Succeed())
			Expect(string(secret.Data["password"])).To(Equal(originalPassword))
		})

		It("sets status.endpoint and phase Provisioning while no replicas are ready", func() {
			createClusterCR(nil)
			r := newReconciler()
			result, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: key})
			Expect(err).NotTo(HaveOccurred())
			Expect(result.RequeueAfter).To(Equal(clusterPollInterval))

			var cr dnsv1alpha1.TechnitiumCluster
			Expect(k8sClient.Get(ctx, key, &cr)).To(Succeed())
			Expect(cr.Status.Endpoint).To(Equal(fmt.Sprintf("http://%s.%s.svc:5380", resourceName, namespace)))
			// envtest has no kubelet, so the StatefulSet's pods never report
			// ready; readyReplicas stays at zero and the phase stays
			// Provisioning rather than advancing to Bootstrapping.
			Expect(cr.Status.ReadyReplicas).To(Equal(int32(0)))
			Expect(cr.Status.Phase).To(Equal(dnsv1alpha1.TechnitiumClusterPhaseProvisioning))
			Expect(cr.Status.ObservedGeneration).To(Equal(cr.Generation))
			Expect(meta.IsStatusConditionTrue(cr.Status.Conditions, dnsv1alpha1.TechnitiumClusterConditionProgressing)).To(BeTrue())
			Expect(meta.IsStatusConditionFalse(cr.Status.Conditions, dnsv1alpha1.TechnitiumClusterConditionAvailable)).To(BeTrue())
		})

		It("does not create a Secret when adminSecretRef is set", func() {
			createClusterCR(func(cr *dnsv1alpha1.TechnitiumCluster) {
				cr.Spec.AdminSecretRef = &dnsv1alpha1.SecretReference{Name: "external-admin-creds"}
			})
			_, err := newReconciler().Reconcile(ctx, reconcile.Request{NamespacedName: key})
			Expect(err).NotTo(HaveOccurred())

			var secret corev1.Secret
			err = k8sClient.Get(ctx, types.NamespacedName{Namespace: namespace, Name: resourceName + "-admin"}, &secret)
			Expect(client.IgnoreNotFound(err)).NotTo(HaveOccurred())
			Expect(err).To(HaveOccurred())
		})
	})
})
