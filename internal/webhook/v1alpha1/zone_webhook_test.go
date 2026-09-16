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
	// TODO (user): Add any additional imports if needed
)

const testZoneName = "example.com"
const testServerName = "dns"

var _ = Describe("Zone Webhook", func() {
	var (
		obj       *dnsv1alpha1.Zone
		oldObj    *dnsv1alpha1.Zone
		validator ZoneCustomValidator
		defaulter ZoneCustomDefaulter
	)

	BeforeEach(func() {
		obj = &dnsv1alpha1.Zone{}
		oldObj = &dnsv1alpha1.Zone{}
		validator = ZoneCustomValidator{}
		Expect(validator).NotTo(BeNil(), "Expected validator to be initialized")
		defaulter = ZoneCustomDefaulter{}
		Expect(defaulter).NotTo(BeNil(), "Expected defaulter to be initialized")
		Expect(oldObj).NotTo(BeNil(), "Expected oldObj to be initialized")
		Expect(obj).NotTo(BeNil(), "Expected obj to be initialized")
	})

	AfterEach(func() {
		// TODO (user): Add any teardown logic common to all tests
	})

	Context("When creating Zone under Defaulting Webhook", func() {
		It("defaults an empty type to Primary", func() {
			obj.Spec.Type = ""
			Expect(defaulter.Default(ctx, obj)).To(Succeed())
			Expect(obj.Spec.Type).To(Equal(dnsv1alpha1.ZoneTypePrimary))
		})

		It("defaults an empty deletionPolicy to Delete", func() {
			obj.Spec.DeletionPolicy = ""
			Expect(defaulter.Default(ctx, obj)).To(Succeed())
			Expect(obj.Spec.DeletionPolicy).To(Equal(dnsv1alpha1.DeletionPolicyDelete))
		})

		It("leaves an explicit type untouched", func() {
			obj.Spec.Type = dnsv1alpha1.ZoneTypeForwarder
			Expect(defaulter.Default(ctx, obj)).To(Succeed())
			Expect(obj.Spec.Type).To(Equal(dnsv1alpha1.ZoneTypeForwarder))
		})
	})

	Context("When creating or updating Zone under Validating Webhook", func() {
		It("admits a Primary zone with only a name", func() {
			obj.Spec.ZoneName = testZoneName
			obj.Spec.ServerRef = dnsv1alpha1.SecretReference{Name: testServerName}
			obj.Spec.Type = dnsv1alpha1.ZoneTypePrimary
			Expect(validator.ValidateCreate(ctx, obj)).Error().NotTo(HaveOccurred())
		})

		It("rejects a zone with no serverRef", func() {
			obj.Spec.ZoneName = testZoneName
			obj.Spec.Type = dnsv1alpha1.ZoneTypePrimary
			_, err := validator.ValidateCreate(ctx, obj)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("serverRef"))
		})

		It("rejects a zone whose serverRef sets a namespace", func() {
			obj.Spec.ZoneName = testZoneName
			obj.Spec.Type = dnsv1alpha1.ZoneTypePrimary
			obj.Spec.ServerRef = dnsv1alpha1.SecretReference{Name: testServerName, Namespace: "some-tenant"}
			_, err := validator.ValidateCreate(ctx, obj)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("serverRef.namespace"))
		})

		It("rejects a Forwarder zone without a forwarder", func() {
			obj.Spec.ZoneName = "fwd.example.com"
			obj.Spec.ServerRef = dnsv1alpha1.SecretReference{Name: testServerName}
			obj.Spec.Type = dnsv1alpha1.ZoneTypeForwarder
			_, err := validator.ValidateCreate(ctx, obj)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("forwarder"))
		})

		It("admits a Forwarder zone with a forwarder", func() {
			fwd := "1.1.1.1"
			obj.Spec.ZoneName = "fwd.example.com"
			obj.Spec.ServerRef = dnsv1alpha1.SecretReference{Name: testServerName}
			obj.Spec.Type = dnsv1alpha1.ZoneTypeForwarder
			obj.Spec.Forwarder = &fwd
			Expect(validator.ValidateCreate(ctx, obj)).Error().NotTo(HaveOccurred())
		})

		It("rejects a Secondary zone without primaryNameServerAddresses", func() {
			obj.Spec.ZoneName = "sec.example.com"
			obj.Spec.ServerRef = dnsv1alpha1.SecretReference{Name: testServerName}
			obj.Spec.Type = dnsv1alpha1.ZoneTypeSecondary
			_, err := validator.ValidateCreate(ctx, obj)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("primaryNameServerAddresses"))
		})

		It("rejects a changed zoneName on update", func() {
			oldObj.Spec.ZoneName = testZoneName
			obj.Spec.ZoneName = "renamed.example.com"
			obj.Spec.ServerRef = dnsv1alpha1.SecretReference{Name: testServerName}
			_, err := validator.ValidateUpdate(ctx, oldObj, obj)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("immutable"))
		})

		It("admits an update that keeps zoneName", func() {
			oldObj.Spec.ZoneName = testZoneName
			obj.Spec.ZoneName = testZoneName
			obj.Spec.ServerRef = dnsv1alpha1.SecretReference{Name: testServerName}
			obj.Spec.Type = dnsv1alpha1.ZoneTypePrimary
			Expect(validator.ValidateUpdate(ctx, oldObj, obj)).Error().NotTo(HaveOccurred())
		})
	})

})
