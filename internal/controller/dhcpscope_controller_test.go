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

// fakeDHCPScopeAPI records the calls the reconciler makes and returns
// programmed results, standing in for a live Technitium server.
type fakeDHCPScopeAPI struct {
	exists          bool
	enabled         bool
	startingAddress string
	endingAddress   string
	subnetMask      string
	routerAddress   string
	dnsServers      []string
	reservations    map[string]technitium.DHCPReservedLease

	getErr    error
	setErr    error
	enableErr error
	deleteErr error
	addErr    error
	removeErr error

	setCalls            []technitium.SetDHCPScopeOptions
	enableCalls         []string
	disableCalls        []string
	deleteCalls         []string
	addedReservations   []string
	removedReservations []string
}

func newFakeDHCPScopeAPI() *fakeDHCPScopeAPI {
	return &fakeDHCPScopeAPI{reservations: map[string]technitium.DHCPReservedLease{}}
}

func (f *fakeDHCPScopeAPI) ListDHCPScopes(_ context.Context) ([]technitium.DHCPScopeInfo, error) {
	if !f.exists {
		return nil, nil
	}
	return []technitium.DHCPScopeInfo{{
		Name: "test-scope", Enabled: f.enabled, StartingAddress: f.startingAddress,
		EndingAddress: f.endingAddress, SubnetMask: f.subnetMask,
	}}, nil
}

func (f *fakeDHCPScopeAPI) GetDHCPScope(_ context.Context, _ string) (*technitium.DHCPScopeDetails, error) {
	if f.getErr != nil {
		return nil, f.getErr
	}
	if !f.exists {
		return nil, fmt.Errorf("%w: no such scope", technitium.ErrDHCPScopeNotFound)
	}
	leases := make([]technitium.DHCPReservedLease, 0, len(f.reservations))
	for _, lease := range f.reservations {
		leases = append(leases, lease)
	}
	return &technitium.DHCPScopeDetails{
		Name: "test-scope", Enabled: f.enabled, StartingAddress: f.startingAddress,
		EndingAddress: f.endingAddress, SubnetMask: f.subnetMask, RouterAddress: f.routerAddress,
		DNSServers: f.dnsServers, ReservedLeases: leases,
	}, nil
}

func (f *fakeDHCPScopeAPI) SetDHCPScope(_ context.Context, opts technitium.SetDHCPScopeOptions) error {
	f.setCalls = append(f.setCalls, opts)
	if f.setErr != nil {
		return f.setErr
	}
	// Applying the write is what makes a repeat reconcile see an in-sync
	// server, matching how the real Technitium API would behave.
	f.exists = true
	f.startingAddress = opts.StartingAddress
	f.endingAddress = opts.EndingAddress
	f.subnetMask = opts.SubnetMask
	if opts.RouterAddress != nil {
		f.routerAddress = *opts.RouterAddress
	}
	if opts.DNSServers != nil {
		f.dnsServers = *opts.DNSServers
	}
	return nil
}

func (f *fakeDHCPScopeAPI) EnableDHCPScope(_ context.Context, name string) error {
	f.enableCalls = append(f.enableCalls, name)
	if f.enableErr != nil {
		return f.enableErr
	}
	f.enabled = true
	return nil
}

func (f *fakeDHCPScopeAPI) DisableDHCPScope(_ context.Context, name string) error {
	f.disableCalls = append(f.disableCalls, name)
	f.enabled = false
	return nil
}

func (f *fakeDHCPScopeAPI) DeleteDHCPScope(_ context.Context, name string) error {
	f.deleteCalls = append(f.deleteCalls, name)
	if f.deleteErr != nil {
		return f.deleteErr
	}
	if !f.exists {
		return fmt.Errorf("%w: no such scope", technitium.ErrDHCPScopeNotFound)
	}
	f.exists = false
	return nil
}

func (f *fakeDHCPScopeAPI) AddReservedLease(_ context.Context, opts technitium.AddReservedLeaseOptions) error {
	f.addedReservations = append(f.addedReservations, opts.HardwareAddress)
	if f.addErr != nil {
		return f.addErr
	}
	f.reservations[opts.HardwareAddress] = technitium.DHCPReservedLease{
		HardwareAddress: opts.HardwareAddress, Address: opts.IPAddress,
	}
	return nil
}

func (f *fakeDHCPScopeAPI) RemoveReservedLease(_ context.Context, _, hardwareAddress string) error {
	f.removedReservations = append(f.removedReservations, hardwareAddress)
	if f.removeErr != nil {
		return f.removeErr
	}
	if _, ok := f.reservations[hardwareAddress]; !ok {
		return fmt.Errorf("%w: no reservation for %s", technitium.ErrDHCPReservationNotFound, hardwareAddress)
	}
	delete(f.reservations, hardwareAddress)
	return nil
}

var _ = Describe("DHCPScope Controller", func() {
	Context("When reconciling a resource", func() {
		const resourceName = "test-dhcpscope"
		const serverName = "test-server"

		ctx := context.Background()
		key := types.NamespacedName{Name: resourceName, Namespace: "default"}

		newReconciler := func(api *fakeDHCPScopeAPI) *DHCPScopeReconciler {
			return &DHCPScopeReconciler{
				Client:            k8sClient,
				Scheme:            k8sClient.Scheme(),
				OperatorNamespace: "default",
				NewServerClient: func(_ context.Context, _ dnsv1alpha1.SecretReference) (DHCPScopeAPI, error) {
					return api, nil
				},
			}
		}

		createDHCPScopeCR := func(mutate func(*dnsv1alpha1.DHCPScope)) {
			resource := &dnsv1alpha1.DHCPScope{
				Name:      resourceName,
				Namespace: "default",
				Spec: dnsv1alpha1.DHCPScopeSpec{
					ServerRef:       dnsv1alpha1.SecretReference{Name: serverName},
					ScopeName:       "test-scope",
					StartingAddress: "192.168.1.100",
					EndingAddress:   "192.168.1.200",
					SubnetMask:      "255.255.255.0",
					Reservations: []dnsv1alpha1.DHCPReservation{
						{HardwareAddress: "00:11:22:33:44:55", IPAddress: "192.168.1.150"},
					},
				},
			}
			if mutate != nil {
				mutate(resource)
			}
			Expect(k8sClient.Create(ctx, resource)).To(Succeed())
		}

		AfterEach(func() {
			resource := &dnsv1alpha1.DHCPScope{}
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

		It("returns without error when the DHCPScope was deleted", func() {
			api := newFakeDHCPScopeAPI()
			_, err := newReconciler(api).Reconcile(ctx, reconcile.Request{NamespacedName: key})
			Expect(err).NotTo(HaveOccurred())
			Expect(api.setCalls).To(BeEmpty())
		})

		It("creates and enables the scope and adds reservations", func() {
			createDHCPScopeCR(nil)
			api := newFakeDHCPScopeAPI()

			result, err := newReconciler(api).Reconcile(ctx, reconcile.Request{NamespacedName: key})
			Expect(err).NotTo(HaveOccurred())
			Expect(result.RequeueAfter).To(Equal(driftReconcileInterval))

			Expect(api.setCalls).To(HaveLen(1))
			Expect(api.enableCalls).To(HaveLen(1))
			Expect(api.addedReservations).To(ConsistOf("00:11:22:33:44:55"))

			scope := &dnsv1alpha1.DHCPScope{}
			Expect(k8sClient.Get(ctx, key, scope)).To(Succeed())
			Expect(meta.IsStatusConditionTrue(scope.Status.Conditions, conditionReady)).To(BeTrue())
			Expect(scope.Status.ObservedGeneration).To(Equal(scope.Generation))
			Expect(scope.Status.ScopeCreated).To(BeTrue())
			Expect(scope.Status.AppliedReservations).To(ConsistOf("00:11:22:33:44:55"))
			Expect(scope.Finalizers).To(ContainElement(dhcpScopeFinalizer))
		})

		It("makes no changes and does not rewrite status when the server already matches", func() {
			createDHCPScopeCR(nil)

			// Seed status.AppliedReservations to match spec, as a prior
			// successful reconcile would have left it.
			scope := &dnsv1alpha1.DHCPScope{}
			Expect(k8sClient.Get(ctx, key, scope)).To(Succeed())
			scope.Status.AppliedReservations = []string{"00:11:22:33:44:55"}
			Expect(k8sClient.Status().Update(ctx, scope)).To(Succeed())

			api := newFakeDHCPScopeAPI()
			api.exists = true
			api.enabled = true
			api.startingAddress = "192.168.1.100"
			api.endingAddress = "192.168.1.200"
			api.subnetMask = "255.255.255.0"
			api.reservations["00:11:22:33:44:55"] = technitium.DHCPReservedLease{
				HardwareAddress: "00:11:22:33:44:55", Address: "192.168.1.150",
			}
			reconciler := newReconciler(api)

			_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: key})
			Expect(err).NotTo(HaveOccurred())
			Expect(api.setCalls).To(BeEmpty())
			Expect(api.enableCalls).To(BeEmpty())
			Expect(api.disableCalls).To(BeEmpty())
			Expect(api.addedReservations).To(BeEmpty())
			Expect(api.removedReservations).To(BeEmpty())

			first := &dnsv1alpha1.DHCPScope{}
			Expect(k8sClient.Get(ctx, key, first)).To(Succeed())

			_, err = reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: key})
			Expect(err).NotTo(HaveOccurred())

			second := &dnsv1alpha1.DHCPScope{}
			Expect(k8sClient.Get(ctx, key, second)).To(Succeed())
			Expect(second.ResourceVersion).To(Equal(first.ResourceVersion))
		})

		It("converges reservations on drift", func() {
			createDHCPScopeCR(nil)
			api := newFakeDHCPScopeAPI()
			reconciler := newReconciler(api)

			_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: key})
			Expect(err).NotTo(HaveOccurred())
			Expect(api.addedReservations).To(ConsistOf("00:11:22:33:44:55"))

			scope := &dnsv1alpha1.DHCPScope{}
			Expect(k8sClient.Get(ctx, key, scope)).To(Succeed())
			scope.Spec.Reservations = []dnsv1alpha1.DHCPReservation{
				{HardwareAddress: "aa:bb:cc:dd:ee:ff", IPAddress: "192.168.1.151"},
			}
			Expect(k8sClient.Update(ctx, scope)).To(Succeed())

			_, err = reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: key})
			Expect(err).NotTo(HaveOccurred())
			Expect(api.addedReservations).To(ConsistOf("00:11:22:33:44:55", "aa:bb:cc:dd:ee:ff"))
			Expect(api.removedReservations).To(ConsistOf("00:11:22:33:44:55"))

			Expect(k8sClient.Get(ctx, key, scope)).To(Succeed())
			Expect(scope.Status.AppliedReservations).To(ConsistOf("aa:bb:cc:dd:ee:ff"))
		})

		It("disables and deletes the scope on delete with the Delete policy", func() {
			createDHCPScopeCR(func(scope *dnsv1alpha1.DHCPScope) {
				scope.Spec.DeletionPolicy = dnsv1alpha1.DeletionPolicyDelete
			})
			api := newFakeDHCPScopeAPI()
			reconciler := newReconciler(api)
			_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: key})
			Expect(err).NotTo(HaveOccurred())

			resource := &dnsv1alpha1.DHCPScope{}
			Expect(k8sClient.Get(ctx, key, resource)).To(Succeed())
			Expect(k8sClient.Delete(ctx, resource)).To(Succeed())

			// The finalizer keeps the object around until the controller runs.
			Expect(k8sClient.Get(ctx, key, resource)).To(Succeed())
			Expect(resource.DeletionTimestamp).NotTo(BeNil())

			_, err = reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: key})
			Expect(err).NotTo(HaveOccurred())

			Expect(api.disableCalls).NotTo(BeEmpty())
			Expect(api.deleteCalls).NotTo(BeEmpty())
			Expect(api.exists).To(BeFalse())

			err = k8sClient.Get(ctx, key, resource)
			Expect(apierrors.IsNotFound(err)).To(BeTrue())
		})

		It("leaves the server untouched on delete with the Orphan policy", func() {
			createDHCPScopeCR(func(scope *dnsv1alpha1.DHCPScope) {
				scope.Spec.DeletionPolicy = dnsv1alpha1.DeletionPolicyOrphan
			})
			api := newFakeDHCPScopeAPI()
			reconciler := newReconciler(api)
			_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: key})
			Expect(err).NotTo(HaveOccurred())

			resource := &dnsv1alpha1.DHCPScope{}
			Expect(k8sClient.Get(ctx, key, resource)).To(Succeed())
			Expect(k8sClient.Delete(ctx, resource)).To(Succeed())

			deleteCallsBeforeDelete := len(api.deleteCalls)
			_, err = reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: key})
			Expect(err).NotTo(HaveOccurred())

			Expect(api.deleteCalls).To(HaveLen(deleteCallsBeforeDelete))
			Expect(api.exists).To(BeTrue())

			err = k8sClient.Get(ctx, key, resource)
			Expect(apierrors.IsNotFound(err)).To(BeTrue())
		})

		It("surfaces and records an error when SetDHCPScope fails", func() {
			createDHCPScopeCR(nil)
			api := newFakeDHCPScopeAPI()
			api.setErr = fmt.Errorf("server unreachable")

			_, err := newReconciler(api).Reconcile(ctx, reconcile.Request{NamespacedName: key})
			Expect(err).To(HaveOccurred())

			scope := &dnsv1alpha1.DHCPScope{}
			Expect(k8sClient.Get(ctx, key, scope)).To(Succeed())
			Expect(meta.IsStatusConditionFalse(scope.Status.Conditions, conditionReady)).To(BeTrue())
			Expect(meta.IsStatusConditionTrue(scope.Status.Conditions, conditionDegraded)).To(BeTrue())
			cond := meta.FindStatusCondition(scope.Status.Conditions, conditionDegraded)
			Expect(cond).NotTo(BeNil())
			Expect(cond.Message).To(ContainSubstring("server unreachable"))
		})
	})
})

var _ = Describe("reconcileReservations", func() {
	It("adds new reservations and removes dropped ones, leaving untouched ones alone", func() {
		ctx := context.Background()
		api := newFakeDHCPScopeAPI()
		api.reservations["a"] = technitium.DHCPReservedLease{HardwareAddress: "a", Address: "10.0.0.1"}
		api.reservations["b"] = technitium.DHCPReservedLease{HardwareAddress: "b", Address: "10.0.0.2"}

		desired := []dnsv1alpha1.DHCPReservation{
			{HardwareAddress: "b", IPAddress: "10.0.0.2"},
			{HardwareAddress: "c", IPAddress: "10.0.0.3"},
		}
		applied, err := reconcileReservations(ctx, []string{"a", "b"}, desired, "test-scope", api)
		Expect(err).NotTo(HaveOccurred())
		Expect(applied).To(ConsistOf("b", "c"))
		Expect(api.addedReservations).To(Equal([]string{"c"}))
		Expect(api.removedReservations).To(Equal([]string{"a"}))
	})

	It("returns the first error and stops without persisting partial progress", func() {
		ctx := context.Background()
		api := newFakeDHCPScopeAPI()
		api.addErr = fmt.Errorf("add failed")

		desired := []dnsv1alpha1.DHCPReservation{{HardwareAddress: "a", IPAddress: "10.0.0.1"}}
		_, err := reconcileReservations(ctx, nil, desired, "test-scope", api)
		Expect(err).To(HaveOccurred())
	})
})
