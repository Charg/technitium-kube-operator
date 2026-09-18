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
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	dnsv1alpha1 "github.com/charg/technitium-operator/api/v1alpha1"
	"github.com/charg/technitium-operator/internal/technitium"
)

// fakeDNSSECAPI records the calls the reconciler makes and returns programmed
// results, standing in for a live Technitium server.
type fakeDNSSECAPI struct {
	props     technitium.DNSSECProperties
	getErr    error
	signErr   error
	unsignErr error

	signCalls   []technitium.SignZoneOptions
	unsignCalls []string
}

func newFakeDNSSECAPI() *fakeDNSSECAPI {
	return &fakeDNSSECAPI{props: technitium.DNSSECProperties{DNSSECStatus: technitium.DNSSECStatusUnsigned}}
}

func (f *fakeDNSSECAPI) GetDNSSECProperties(_ context.Context, _ string) (*technitium.DNSSECProperties, error) {
	if f.getErr != nil {
		return nil, f.getErr
	}
	props := f.props
	return &props, nil
}

func (f *fakeDNSSECAPI) SignZone(_ context.Context, opts technitium.SignZoneOptions) error {
	f.signCalls = append(f.signCalls, opts)
	if f.signErr != nil {
		return f.signErr
	}
	// Applying the sign is what makes a repeat reconcile see the zone as
	// already signed, matching how the real Technitium API would behave.
	status := technitium.DNSSECStatusSignedWithNSEC
	if opts.NxProof != nil && *opts.NxProof == "NSEC3" {
		status = technitium.DNSSECStatusSignedWithNSEC3
	}
	f.props.DNSSECStatus = status
	return nil
}

func (f *fakeDNSSECAPI) UnsignZone(_ context.Context, _ string) error {
	f.unsignCalls = append(f.unsignCalls, f.props.DNSSECStatus)
	if f.unsignErr != nil {
		return f.unsignErr
	}
	f.props.DNSSECStatus = technitium.DNSSECStatusUnsigned
	return nil
}

var _ = Describe("DNSSEC Controller", func() {
	Context("When reconciling a resource", func() {
		const resourceName = "test-dnssec"
		const serverName = "test-server"

		ctx := context.Background()
		key := types.NamespacedName{Name: resourceName, Namespace: "default"}

		newReconciler := func(api *fakeDNSSECAPI) *DNSSECReconciler {
			return &DNSSECReconciler{
				Client:            k8sClient,
				Scheme:            k8sClient.Scheme(),
				OperatorNamespace: "default",
				NewServerClient: func(_ context.Context, _ dnsv1alpha1.SecretReference) (DNSSECAPI, error) {
					return api, nil
				},
			}
		}

		createDNSSECCR := func(mutate func(*dnsv1alpha1.DNSSEC)) {
			resource := &dnsv1alpha1.DNSSEC{
				Name:      resourceName,
				Namespace: "default",
				Spec: dnsv1alpha1.DNSSECSpec{
					ServerRef: dnsv1alpha1.SecretReference{Name: serverName},
					Zone:      "example.com",
					Algorithm: dnsv1alpha1.DNSSECAlgorithmECDSA,
					NSECType:  dnsv1alpha1.NSECTypeNSEC,
				},
			}
			if mutate != nil {
				mutate(resource)
			}
			Expect(k8sClient.Create(ctx, resource)).To(Succeed())
		}

		AfterEach(func() {
			resource := &dnsv1alpha1.DNSSEC{}
			if err := k8sClient.Get(ctx, key, resource); err != nil {
				return
			}
			// Drop any finalizer with a merge patch so cleanup does not wedge on
			// a test left pending, then best-effort delete.
			if len(resource.Finalizers) > 0 {
				patch := client.MergeFrom(resource.DeepCopy())
				resource.Finalizers = nil
				_ = k8sClient.Patch(ctx, resource, patch)
			}
			_ = k8sClient.Delete(ctx, resource)
		})

		It("returns without error when the DNSSEC was deleted", func() {
			api := newFakeDNSSECAPI()
			_, err := newReconciler(api).Reconcile(ctx, reconcile.Request{NamespacedName: key})
			Expect(err).NotTo(HaveOccurred())
			Expect(api.signCalls).To(BeEmpty())
		})

		It("signs an unsigned zone and records Ready plus status.signed", func() {
			createDNSSECCR(nil)
			api := newFakeDNSSECAPI()

			result, err := newReconciler(api).Reconcile(ctx, reconcile.Request{NamespacedName: key})
			Expect(err).NotTo(HaveOccurred())
			Expect(result.RequeueAfter).To(Equal(driftReconcileInterval))

			Expect(api.signCalls).To(HaveLen(1))
			Expect(api.signCalls[0].Zone).To(Equal("example.com"))
			Expect(api.signCalls[0].Algorithm).To(Equal("ECDSA"))

			d := &dnsv1alpha1.DNSSEC{}
			Expect(k8sClient.Get(ctx, key, d)).To(Succeed())
			Expect(meta.IsStatusConditionTrue(d.Status.Conditions, conditionReady)).To(BeTrue())
			Expect(d.Status.ObservedGeneration).To(Equal(d.Generation))
			Expect(d.Status.Signed).To(BeTrue())
			Expect(d.Finalizers).To(ContainElement(dnssecFinalizer))
		})

		It("makes no changes and does not rewrite status when the zone is already signed with the desired nsecType", func() {
			createDNSSECCR(nil)
			api := newFakeDNSSECAPI()
			api.props.DNSSECStatus = technitium.DNSSECStatusSignedWithNSEC
			reconciler := newReconciler(api)

			_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: key})
			Expect(err).NotTo(HaveOccurred())
			Expect(api.signCalls).To(BeEmpty())

			first := &dnsv1alpha1.DNSSEC{}
			Expect(k8sClient.Get(ctx, key, first)).To(Succeed())
			Expect(first.Status.Signed).To(BeTrue())

			_, err = reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: key})
			Expect(err).NotTo(HaveOccurred())
			Expect(api.signCalls).To(BeEmpty())

			second := &dnsv1alpha1.DNSSEC{}
			Expect(k8sClient.Get(ctx, key, second)).To(Succeed())
			Expect(second.ResourceVersion).To(Equal(first.ResourceVersion))
		})

		It("unsigns the zone on delete with the Delete policy", func() {
			createDNSSECCR(func(d *dnsv1alpha1.DNSSEC) {
				d.Spec.DeletionPolicy = dnsv1alpha1.DeletionPolicyDelete
			})
			api := newFakeDNSSECAPI()
			reconciler := newReconciler(api)
			_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: key})
			Expect(err).NotTo(HaveOccurred())
			Expect(api.signCalls).To(HaveLen(1))

			resource := &dnsv1alpha1.DNSSEC{}
			Expect(k8sClient.Get(ctx, key, resource)).To(Succeed())
			Expect(k8sClient.Delete(ctx, resource)).To(Succeed())

			// The finalizer keeps the object around until the controller runs.
			Expect(k8sClient.Get(ctx, key, resource)).To(Succeed())
			Expect(resource.DeletionTimestamp).NotTo(BeNil())

			_, err = reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: key})
			Expect(err).NotTo(HaveOccurred())
			Expect(api.unsignCalls).To(HaveLen(1))

			err = k8sClient.Get(ctx, key, resource)
			Expect(apierrors.IsNotFound(err)).To(BeTrue())
		})

		It("leaves the zone signed on delete with the Orphan policy", func() {
			createDNSSECCR(func(d *dnsv1alpha1.DNSSEC) {
				d.Spec.DeletionPolicy = dnsv1alpha1.DeletionPolicyOrphan
			})
			api := newFakeDNSSECAPI()
			reconciler := newReconciler(api)
			_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: key})
			Expect(err).NotTo(HaveOccurred())

			resource := &dnsv1alpha1.DNSSEC{}
			Expect(k8sClient.Get(ctx, key, resource)).To(Succeed())
			Expect(k8sClient.Delete(ctx, resource)).To(Succeed())

			_, err = reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: key})
			Expect(err).NotTo(HaveOccurred())
			Expect(api.unsignCalls).To(BeEmpty())

			err = k8sClient.Get(ctx, key, resource)
			Expect(apierrors.IsNotFound(err)).To(BeTrue())
		})

		It("surfaces and records an error when SignZone fails", func() {
			createDNSSECCR(nil)
			api := newFakeDNSSECAPI()
			api.signErr = fmt.Errorf("server unreachable")

			_, err := newReconciler(api).Reconcile(ctx, reconcile.Request{NamespacedName: key})
			Expect(err).To(HaveOccurred())

			d := &dnsv1alpha1.DNSSEC{}
			Expect(k8sClient.Get(ctx, key, d)).To(Succeed())
			Expect(meta.IsStatusConditionFalse(d.Status.Conditions, conditionReady)).To(BeTrue())
			Expect(meta.IsStatusConditionTrue(d.Status.Conditions, conditionDegraded)).To(BeTrue())
			cond := meta.FindStatusCondition(d.Status.Conditions, conditionDegraded)
			Expect(cond).NotTo(BeNil())
			Expect(cond.Message).To(ContainSubstring("server unreachable"))
			Expect(d.Status.Signed).To(BeFalse())
		})
	})
})
