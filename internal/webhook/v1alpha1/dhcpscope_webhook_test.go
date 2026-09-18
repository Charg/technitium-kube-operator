/*
Copyright (c) 2026 Chris

Licensed under the MIT License. See the LICENSE file in the project root for
the full license text.
*/

package v1alpha1

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	dnsv1alpha1 "github.com/charg/technitium-operator/api/v1alpha1"
)

var _ = Describe("DHCPScope Webhook", func() {
	var (
		obj       *dnsv1alpha1.DHCPScope
		oldObj    *dnsv1alpha1.DHCPScope
		validator DHCPScopeCustomValidator
		defaulter DHCPScopeCustomDefaulter
	)

	// validScope returns a DHCPScope that satisfies every validator rule, so
	// individual tests can mutate exactly the field under test.
	validScope := func() *dnsv1alpha1.DHCPScope {
		return &dnsv1alpha1.DHCPScope{
			Spec: dnsv1alpha1.DHCPScopeSpec{
				ServerRef:       dnsv1alpha1.SecretReference{Name: testServerName},
				ScopeName:       "test-scope",
				StartingAddress: "192.168.1.100",
				EndingAddress:   "192.168.1.200",
				SubnetMask:      "255.255.255.0",
			},
		}
	}

	BeforeEach(func() {
		obj = &dnsv1alpha1.DHCPScope{}
		oldObj = &dnsv1alpha1.DHCPScope{}
		validator = DHCPScopeCustomValidator{}
		Expect(validator).NotTo(BeNil(), "Expected validator to be initialized")
		defaulter = DHCPScopeCustomDefaulter{}
		Expect(defaulter).NotTo(BeNil(), "Expected defaulter to be initialized")
		Expect(oldObj).NotTo(BeNil(), "Expected oldObj to be initialized")
		Expect(obj).NotTo(BeNil(), "Expected obj to be initialized")
	})

	Context("When creating DHCPScope under Defaulting Webhook", func() {
		It("defaults an empty deletionPolicy to Delete", func() {
			obj.Spec.DeletionPolicy = ""
			Expect(defaulter.Default(ctx, obj)).To(Succeed())
			Expect(obj.Spec.DeletionPolicy).To(Equal(dnsv1alpha1.DeletionPolicyDelete))
		})

		It("leaves an explicit deletionPolicy untouched", func() {
			obj.Spec.DeletionPolicy = dnsv1alpha1.DeletionPolicyOrphan
			Expect(defaulter.Default(ctx, obj)).To(Succeed())
			Expect(obj.Spec.DeletionPolicy).To(Equal(dnsv1alpha1.DeletionPolicyOrphan))
		})

		It("defaults a nil enabled to true", func() {
			obj.Spec.Enabled = nil
			Expect(defaulter.Default(ctx, obj)).To(Succeed())
			Expect(obj.Spec.Enabled).NotTo(BeNil())
			Expect(*obj.Spec.Enabled).To(BeTrue())
		})

		It("leaves an explicit enabled untouched", func() {
			disabled := false
			obj.Spec.Enabled = &disabled
			Expect(defaulter.Default(ctx, obj)).To(Succeed())
			Expect(*obj.Spec.Enabled).To(BeFalse())
		})
	})

	Context("When creating or updating DHCPScope under Validating Webhook", func() {
		It("admits a coherent DHCPScope", func() {
			obj = validScope()
			Expect(validator.ValidateCreate(ctx, obj)).Error().NotTo(HaveOccurred())
		})

		It("admits a DHCPScope with a router address and a reservation within the subnet", func() {
			obj = validScope()
			router := "192.168.1.1"
			obj.Spec.RouterAddress = &router
			obj.Spec.Reservations = []dnsv1alpha1.DHCPReservation{
				{HardwareAddress: "00:11:22:33:44:55", IPAddress: "192.168.1.150"},
			}
			Expect(validator.ValidateCreate(ctx, obj)).Error().NotTo(HaveOccurred())
		})

		It("rejects a DHCPScope with no serverRef", func() {
			obj = validScope()
			obj.Spec.ServerRef = dnsv1alpha1.SecretReference{}
			_, err := validator.ValidateCreate(ctx, obj)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("serverRef"))
		})

		It("rejects a DHCPScope whose serverRef sets a namespace", func() {
			obj = validScope()
			obj.Spec.ServerRef.Namespace = "some-tenant"
			_, err := validator.ValidateCreate(ctx, obj)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("serverRef.namespace"))
		})

		It("rejects an empty scopeName", func() {
			obj = validScope()
			obj.Spec.ScopeName = ""
			_, err := validator.ValidateCreate(ctx, obj)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("scopeName"))
		})

		It("rejects a starting address after the ending address", func() {
			obj = validScope()
			obj.Spec.StartingAddress = "192.168.1.200"
			obj.Spec.EndingAddress = "192.168.1.100"
			_, err := validator.ValidateCreate(ctx, obj)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("startingAddress"))
		})

		It("rejects an invalid starting address", func() {
			obj = validScope()
			obj.Spec.StartingAddress = "not-an-ip"
			_, err := validator.ValidateCreate(ctx, obj)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("startingAddress"))
		})

		It("rejects an invalid subnet mask", func() {
			obj = validScope()
			obj.Spec.SubnetMask = "255.0.255.0"
			_, err := validator.ValidateCreate(ctx, obj)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("subnetMask"))
		})

		It("rejects a router address outside the subnet", func() {
			obj = validScope()
			router := "10.0.0.1"
			obj.Spec.RouterAddress = &router
			_, err := validator.ValidateCreate(ctx, obj)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("routerAddress"))
		})

		It("rejects a reservation with an invalid MAC address", func() {
			obj = validScope()
			obj.Spec.Reservations = []dnsv1alpha1.DHCPReservation{
				{HardwareAddress: "not-a-mac", IPAddress: "192.168.1.150"},
			}
			_, err := validator.ValidateCreate(ctx, obj)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("hardwareAddress"))
		})

		It("rejects a reservation whose IP falls outside the subnet", func() {
			obj = validScope()
			obj.Spec.Reservations = []dnsv1alpha1.DHCPReservation{
				{HardwareAddress: "00:11:22:33:44:55", IPAddress: "10.0.0.150"},
			}
			_, err := validator.ValidateCreate(ctx, obj)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("ipAddress"))
		})

		It("admits an update that keeps the spec coherent", func() {
			oldObj = validScope()
			obj = validScope()
			Expect(validator.ValidateUpdate(ctx, oldObj, obj)).Error().NotTo(HaveOccurred())
		})

		It("rejects an update that drops serverRef", func() {
			oldObj = validScope()
			obj = validScope()
			obj.Spec.ServerRef = dnsv1alpha1.SecretReference{}
			_, err := validator.ValidateUpdate(ctx, oldObj, obj)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("serverRef"))
		})
	})
})
