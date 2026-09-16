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

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	dnsv1alpha1 "github.com/charg/technitium-operator/api/v1alpha1"
	"github.com/charg/technitium-operator/internal/technitium"
)

// fakeZoneAPI records the calls the reconciler makes and returns programmed
// results, standing in for a live Technitium server.
type fakeZoneAPI struct {
	getOptions func(zone string) (*technitium.ZoneOptions, error)
	createErr  error
	setErr     error
	deleteErr  error

	createCalls []technitium.CreateZoneOptions
	setCalls    []technitium.ZoneOptionsUpdate
	deleteCalls []string
}

func (f *fakeZoneAPI) CreateZone(_ context.Context, opts technitium.CreateZoneOptions) error {
	f.createCalls = append(f.createCalls, opts)
	return f.createErr
}

func (f *fakeZoneAPI) GetZoneOptions(_ context.Context, zone string) (*technitium.ZoneOptions, error) {
	return f.getOptions(zone)
}

func (f *fakeZoneAPI) SetZoneOptions(_ context.Context, _ string, opts technitium.ZoneOptionsUpdate) error {
	f.setCalls = append(f.setCalls, opts)
	return f.setErr
}

func (f *fakeZoneAPI) DeleteZone(_ context.Context, zone string) error {
	f.deleteCalls = append(f.deleteCalls, zone)
	return f.deleteErr
}

// notFound is a GetZoneOptions stub for a zone the server does not have.
func notFound(string) (*technitium.ZoneOptions, error) {
	return nil, fmt.Errorf("%w: no such zone", technitium.ErrZoneNotFound)
}

var _ = Describe("Zone Controller", func() {
	Context("When reconciling a resource", func() {
		const resourceName = "test-resource"
		const zoneName = "example.com"

		ctx := context.Background()

		// Zone is cluster-scoped, so the lookup key carries no namespace.
		key := types.NamespacedName{Name: resourceName}

		newReconciler := func(api *fakeZoneAPI) *ZoneReconciler {
			return &ZoneReconciler{
				Client:     k8sClient,
				Scheme:     k8sClient.Scheme(),
				Technitium: api,
			}
		}

		createZoneCR := func(mutate func(*dnsv1alpha1.Zone)) {
			resource := &dnsv1alpha1.Zone{
				ObjectMeta: metav1.ObjectMeta{Name: resourceName},
				Spec:       dnsv1alpha1.ZoneSpec{ZoneName: zoneName},
			}
			if mutate != nil {
				mutate(resource)
			}
			Expect(k8sClient.Create(ctx, resource)).To(Succeed())
		}

		AfterEach(func() {
			resource := &dnsv1alpha1.Zone{}
			if err := k8sClient.Get(ctx, key, resource); err != nil {
				return
			}
			// Drop any finalizer with a merge patch so cleanup does not wedge on
			// server-side deletion a test left pending, then best-effort delete.
			// A merge patch avoids resourceVersion conflicts, and removing the
			// finalizer on an already-deleting object garbage-collects it.
			if len(resource.Finalizers) > 0 {
				patch := client.MergeFrom(resource.DeepCopy())
				resource.Finalizers = nil
				_ = k8sClient.Patch(ctx, resource, patch)
			}
			_ = k8sClient.Delete(ctx, resource)
		})

		It("returns without error when the Zone was deleted", func() {
			api := &fakeZoneAPI{getOptions: notFound}
			_, err := newReconciler(api).Reconcile(ctx, reconcile.Request{NamespacedName: key})
			Expect(err).NotTo(HaveOccurred())
			Expect(api.createCalls).To(BeEmpty())
		})

		It("creates the zone on the server when it is absent", func() {
			createZoneCR(nil)
			api := &fakeZoneAPI{getOptions: notFound}

			result, err := newReconciler(api).Reconcile(ctx, reconcile.Request{NamespacedName: key})
			Expect(err).NotTo(HaveOccurred())
			Expect(result.RequeueAfter).To(Equal(driftReconcileInterval))

			Expect(api.createCalls).To(HaveLen(1))
			Expect(api.createCalls[0].Zone).To(Equal(zoneName))
			Expect(api.createCalls[0].Type).To(Equal(string(dnsv1alpha1.ZoneTypePrimary)))

			zone := &dnsv1alpha1.Zone{}
			Expect(k8sClient.Get(ctx, key, zone)).To(Succeed())
			Expect(zone.Status.ZoneCreated).To(BeTrue())
			Expect(zone.Status.ObservedGeneration).To(Equal(zone.Generation))
			Expect(meta.IsStatusConditionTrue(zone.Status.Conditions, conditionReady)).To(BeTrue())
			Expect(zone.Finalizers).To(ContainElement(zoneFinalizer))
		})

		It("deletes the server zone and clears the finalizer on delete", func() {
			createZoneCR(nil)
			api := &fakeZoneAPI{getOptions: notFound}
			_, err := newReconciler(api).Reconcile(ctx, reconcile.Request{NamespacedName: key})
			Expect(err).NotTo(HaveOccurred())

			resource := &dnsv1alpha1.Zone{}
			Expect(k8sClient.Get(ctx, key, resource)).To(Succeed())
			Expect(k8sClient.Delete(ctx, resource)).To(Succeed())

			// The finalizer keeps the object around until the controller runs.
			Expect(k8sClient.Get(ctx, key, resource)).To(Succeed())
			Expect(resource.DeletionTimestamp).NotTo(BeNil())

			_, err = newReconciler(api).Reconcile(ctx, reconcile.Request{NamespacedName: key})
			Expect(err).NotTo(HaveOccurred())
			Expect(api.deleteCalls).To(Equal([]string{zoneName}))

			err = k8sClient.Get(ctx, key, resource)
			Expect(apierrors.IsNotFound(err)).To(BeTrue())
		})

		It("completes deletion when the server zone is already gone", func() {
			createZoneCR(nil)
			api := &fakeZoneAPI{
				getOptions: notFound,
				deleteErr:  fmt.Errorf("%w: no such zone", technitium.ErrZoneNotFound),
			}
			_, err := newReconciler(api).Reconcile(ctx, reconcile.Request{NamespacedName: key})
			Expect(err).NotTo(HaveOccurred())

			resource := &dnsv1alpha1.Zone{}
			Expect(k8sClient.Get(ctx, key, resource)).To(Succeed())
			Expect(k8sClient.Delete(ctx, resource)).To(Succeed())

			_, err = newReconciler(api).Reconcile(ctx, reconcile.Request{NamespacedName: key})
			Expect(err).NotTo(HaveOccurred())

			err = k8sClient.Get(ctx, key, resource)
			Expect(apierrors.IsNotFound(err)).To(BeTrue())
		})

		It("keeps the finalizer when the server delete fails", func() {
			createZoneCR(nil)
			api := &fakeZoneAPI{getOptions: notFound}
			_, err := newReconciler(api).Reconcile(ctx, reconcile.Request{NamespacedName: key})
			Expect(err).NotTo(HaveOccurred())

			resource := &dnsv1alpha1.Zone{}
			Expect(k8sClient.Get(ctx, key, resource)).To(Succeed())
			Expect(k8sClient.Delete(ctx, resource)).To(Succeed())

			failing := &fakeZoneAPI{getOptions: notFound, deleteErr: fmt.Errorf("server unreachable")}
			_, err = newReconciler(failing).Reconcile(ctx, reconcile.Request{NamespacedName: key})
			Expect(err).To(HaveOccurred())

			// The zone must still exist with its finalizer so cleanup is retried.
			Expect(k8sClient.Get(ctx, key, resource)).To(Succeed())
			Expect(resource.Finalizers).To(ContainElement(zoneFinalizer))
		})

		It("leaves the server zone intact when the deletion policy is Orphan", func() {
			createZoneCR(func(z *dnsv1alpha1.Zone) { z.Spec.DeletionPolicy = dnsv1alpha1.DeletionPolicyOrphan })
			api := &fakeZoneAPI{getOptions: notFound}
			_, err := newReconciler(api).Reconcile(ctx, reconcile.Request{NamespacedName: key})
			Expect(err).NotTo(HaveOccurred())

			resource := &dnsv1alpha1.Zone{}
			Expect(k8sClient.Get(ctx, key, resource)).To(Succeed())
			Expect(k8sClient.Delete(ctx, resource)).To(Succeed())

			_, err = newReconciler(api).Reconcile(ctx, reconcile.Request{NamespacedName: key})
			Expect(err).NotTo(HaveOccurred())
			Expect(api.deleteCalls).To(BeEmpty())

			err = k8sClient.Get(ctx, key, resource)
			Expect(apierrors.IsNotFound(err)).To(BeTrue())
		})

		It("passes forwarder and catalog options through on create", func() {
			forwarder := "1.1.1.1"
			catalog := "shared"
			protocol := dnsv1alpha1.ForwarderProtocolHTTPS
			createZoneCR(func(z *dnsv1alpha1.Zone) {
				z.Spec.ZoneName = "fwd.example.com"
				z.Spec.Type = dnsv1alpha1.ZoneTypeForwarder
				z.Spec.Forwarder = &forwarder
				z.Spec.ForwarderProtocol = &protocol
				z.Spec.Catalog = &catalog
			})
			api := &fakeZoneAPI{getOptions: notFound}

			_, err := newReconciler(api).Reconcile(ctx, reconcile.Request{NamespacedName: key})
			Expect(err).NotTo(HaveOccurred())

			Expect(api.createCalls).To(HaveLen(1))
			Expect(api.createCalls[0].Forwarder).To(Equal(forwarder))
			Expect(api.createCalls[0].Protocol).To(Equal(string(protocol)))
			Expect(api.createCalls[0].Catalog).To(Equal(catalog))
		})

		It("treats an already-existing zone on create as success", func() {
			createZoneCR(nil)
			api := &fakeZoneAPI{
				getOptions: notFound,
				createErr:  fmt.Errorf("%w: example.com", technitium.ErrZoneAlreadyExists),
			}

			_, err := newReconciler(api).Reconcile(ctx, reconcile.Request{NamespacedName: key})
			Expect(err).NotTo(HaveOccurred())

			zone := &dnsv1alpha1.Zone{}
			Expect(k8sClient.Get(ctx, key, zone)).To(Succeed())
			Expect(zone.Status.ZoneCreated).To(BeTrue())
		})

		It("makes no changes when an existing zone already matches", func() {
			createZoneCR(nil)
			api := &fakeZoneAPI{
				getOptions: func(zone string) (*technitium.ZoneOptions, error) {
					return &technitium.ZoneOptions{Name: zone, Type: string(dnsv1alpha1.ZoneTypePrimary)}, nil
				},
			}

			_, err := newReconciler(api).Reconcile(ctx, reconcile.Request{NamespacedName: key})
			Expect(err).NotTo(HaveOccurred())
			Expect(api.createCalls).To(BeEmpty())
			Expect(api.setCalls).To(BeEmpty())
		})

		It("does not rewrite status on a repeat reconcile of a matching zone", func() {
			createZoneCR(nil)
			api := &fakeZoneAPI{
				getOptions: func(zone string) (*technitium.ZoneOptions, error) {
					return &technitium.ZoneOptions{Name: zone, Type: string(dnsv1alpha1.ZoneTypePrimary)}, nil
				},
			}
			reconciler := newReconciler(api)

			_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: key})
			Expect(err).NotTo(HaveOccurred())

			first := &dnsv1alpha1.Zone{}
			Expect(k8sClient.Get(ctx, key, first)).To(Succeed())

			_, err = reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: key})
			Expect(err).NotTo(HaveOccurred())

			second := &dnsv1alpha1.Zone{}
			Expect(k8sClient.Get(ctx, key, second)).To(Succeed())
			Expect(second.ResourceVersion).To(Equal(first.ResourceVersion))
		})

		It("corrects catalog drift on an existing zone", func() {
			catalog := "shared"
			createZoneCR(func(z *dnsv1alpha1.Zone) { z.Spec.Catalog = &catalog })
			api := &fakeZoneAPI{
				getOptions: func(zone string) (*technitium.ZoneOptions, error) {
					return &technitium.ZoneOptions{Name: zone, Type: string(dnsv1alpha1.ZoneTypePrimary), Catalog: "stale"}, nil
				},
			}

			_, err := newReconciler(api).Reconcile(ctx, reconcile.Request{NamespacedName: key})
			Expect(err).NotTo(HaveOccurred())
			Expect(api.createCalls).To(BeEmpty())
			Expect(api.setCalls).To(HaveLen(1))
			Expect(api.setCalls[0].Catalog).NotTo(BeNil())
			Expect(*api.setCalls[0].Catalog).To(Equal(catalog))
		})

		It("surfaces and records an error when the server call fails", func() {
			createZoneCR(nil)
			api := &fakeZoneAPI{
				getOptions: notFound,
				createErr:  fmt.Errorf("server unreachable"),
			}

			_, err := newReconciler(api).Reconcile(ctx, reconcile.Request{NamespacedName: key})
			Expect(err).To(HaveOccurred())

			zone := &dnsv1alpha1.Zone{}
			Expect(k8sClient.Get(ctx, key, zone)).To(Succeed())
			Expect(meta.IsStatusConditionFalse(zone.Status.Conditions, conditionReady)).To(BeTrue())
			Expect(meta.IsStatusConditionTrue(zone.Status.Conditions, conditionDegraded)).To(BeTrue())
			cond := meta.FindStatusCondition(zone.Status.Conditions, conditionDegraded)
			Expect(cond).NotTo(BeNil())
			Expect(cond.Message).To(ContainSubstring("server unreachable"))
		})

		It("clears Degraded and returns to Ready once the server recovers", func() {
			createZoneCR(nil)

			failing := &fakeZoneAPI{getOptions: notFound, createErr: fmt.Errorf("server unreachable")}
			_, err := newReconciler(failing).Reconcile(ctx, reconcile.Request{NamespacedName: key})
			Expect(err).To(HaveOccurred())

			healthy := &fakeZoneAPI{getOptions: notFound}
			_, err = newReconciler(healthy).Reconcile(ctx, reconcile.Request{NamespacedName: key})
			Expect(err).NotTo(HaveOccurred())

			zone := &dnsv1alpha1.Zone{}
			Expect(k8sClient.Get(ctx, key, zone)).To(Succeed())
			Expect(meta.IsStatusConditionTrue(zone.Status.Conditions, conditionReady)).To(BeTrue())
			Expect(meta.IsStatusConditionFalse(zone.Status.Conditions, conditionDegraded)).To(BeTrue())
		})
	})
})

var _ = Describe("createOptionsFromSpec", func() {
	It("omits forwarder fields when unset", func() {
		zone := &dnsv1alpha1.Zone{Spec: dnsv1alpha1.ZoneSpec{
			ZoneName: "example.com",
			Type:     dnsv1alpha1.ZoneTypePrimary,
		}}
		opts := createOptionsFromSpec(zone)
		Expect(opts.Forwarder).To(BeEmpty())
		Expect(opts.Protocol).To(BeEmpty())
		Expect(opts.Catalog).To(BeEmpty())
	})
})
