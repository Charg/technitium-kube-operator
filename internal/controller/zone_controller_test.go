/*
Copyright (c) 2026 Chris

Licensed under the MIT License. See the LICENSE file in the project root for
the full license text.
*/

package controller

import (
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	dnsv1alpha1 "github.com/charg/technitium-operator/api/v1alpha1"
)

var _ = Describe("Zone Controller", func() {
	Context("When reconciling a resource", func() {
		const resourceName = "test-resource"

		ctx := context.Background()

		// Zone is cluster-scoped, so the lookup key carries no namespace.
		typeNamespacedName := types.NamespacedName{
			Name: resourceName,
		}
		zone := &dnsv1alpha1.Zone{}

		BeforeEach(func() {
			By("creating the custom resource for the Kind Zone")
			err := k8sClient.Get(ctx, typeNamespacedName, zone)
			if err != nil && errors.IsNotFound(err) {
				resource := &dnsv1alpha1.Zone{
					ObjectMeta: metav1.ObjectMeta{
						Name: resourceName,
					},
					Spec: dnsv1alpha1.ZoneSpec{
						ZoneName: "example.com",
					},
				}
				Expect(k8sClient.Create(ctx, resource)).To(Succeed())
			}
		})

		AfterEach(func() {
			// TODO(user): Cleanup logic after each test, like removing the resource instance.
			resource := &dnsv1alpha1.Zone{}
			err := k8sClient.Get(ctx, typeNamespacedName, resource)
			Expect(err).NotTo(HaveOccurred())

			By("Cleanup the specific resource instance Zone")
			Expect(k8sClient.Delete(ctx, resource)).To(Succeed())
		})
		It("should successfully reconcile the resource", func() {
			By("Reconciling the created resource")
			controllerReconciler := &ZoneReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: typeNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())
			// TODO(user): Add more specific assertions depending on your controller's reconciliation logic.
			// Example: If you expect a certain status condition after reconciliation, verify it here.
		})
	})
})
