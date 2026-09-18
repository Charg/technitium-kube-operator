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

var _ = Describe("DNSApp Webhook", func() {
	var (
		obj       *dnsv1alpha1.DNSApp
		oldObj    *dnsv1alpha1.DNSApp
		validator DNSAppCustomValidator
		defaulter DNSAppCustomDefaulter
	)

	BeforeEach(func() {
		obj = &dnsv1alpha1.DNSApp{}
		oldObj = &dnsv1alpha1.DNSApp{}
		validator = DNSAppCustomValidator{}
		Expect(validator).NotTo(BeNil(), "Expected validator to be initialized")
		defaulter = DNSAppCustomDefaulter{}
		Expect(defaulter).NotTo(BeNil(), "Expected defaulter to be initialized")
		Expect(oldObj).NotTo(BeNil(), "Expected oldObj to be initialized")
		Expect(obj).NotTo(BeNil(), "Expected obj to be initialized")
	})

	Context("When creating DNSApp under Defaulting Webhook", func() {
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
	})

	Context("When creating or updating DNSApp under Validating Webhook", func() {
		It("admits a DNSApp with a serverRef, appName, and url", func() {
			obj.Spec.ServerRef = dnsv1alpha1.SecretReference{Name: testServerName}
			obj.Spec.AppName = "Split Horizon"
			obj.Spec.URL = "https://download.technitium.com/dns/apps/SplitHorizonApp-v1.4.zip"
			Expect(validator.ValidateCreate(ctx, obj)).Error().NotTo(HaveOccurred())
		})

		It("rejects a DNSApp with no serverRef", func() {
			obj.Spec.AppName = "Split Horizon"
			obj.Spec.URL = "https://example.com/app.zip"
			_, err := validator.ValidateCreate(ctx, obj)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("serverRef"))
		})

		It("rejects a DNSApp whose serverRef sets a namespace", func() {
			obj.Spec.ServerRef = dnsv1alpha1.SecretReference{Name: testServerName, Namespace: "some-tenant"}
			obj.Spec.AppName = "Split Horizon"
			obj.Spec.URL = "https://example.com/app.zip"
			_, err := validator.ValidateCreate(ctx, obj)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("serverRef.namespace"))
		})

		It("rejects a DNSApp with no appName", func() {
			obj.Spec.ServerRef = dnsv1alpha1.SecretReference{Name: testServerName}
			obj.Spec.URL = "https://example.com/app.zip"
			_, err := validator.ValidateCreate(ctx, obj)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("appName"))
		})

		It("rejects a DNSApp with no url", func() {
			obj.Spec.ServerRef = dnsv1alpha1.SecretReference{Name: testServerName}
			obj.Spec.AppName = "Split Horizon"
			_, err := validator.ValidateCreate(ctx, obj)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("url"))
		})

		It("admits an update that keeps serverRef, appName, and url", func() {
			oldObj.Spec.ServerRef = dnsv1alpha1.SecretReference{Name: testServerName}
			oldObj.Spec.AppName = "Split Horizon"
			oldObj.Spec.URL = "https://example.com/app.zip"
			obj.Spec.ServerRef = dnsv1alpha1.SecretReference{Name: testServerName}
			obj.Spec.AppName = "Split Horizon"
			obj.Spec.URL = "https://example.com/app.zip"
			Expect(validator.ValidateUpdate(ctx, oldObj, obj)).Error().NotTo(HaveOccurred())
		})

		It("rejects an update that drops serverRef", func() {
			oldObj.Spec.ServerRef = dnsv1alpha1.SecretReference{Name: testServerName}
			obj.Spec.AppName = "Split Horizon"
			obj.Spec.URL = "https://example.com/app.zip"
			_, err := validator.ValidateUpdate(ctx, oldObj, obj)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("serverRef"))
		})
	})
})
