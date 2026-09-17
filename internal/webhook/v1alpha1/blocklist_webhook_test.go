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

var _ = Describe("Blocklist Webhook", func() {
	var (
		obj       *dnsv1alpha1.Blocklist
		oldObj    *dnsv1alpha1.Blocklist
		validator BlocklistCustomValidator
		defaulter BlocklistCustomDefaulter
	)

	BeforeEach(func() {
		obj = &dnsv1alpha1.Blocklist{}
		oldObj = &dnsv1alpha1.Blocklist{}
		validator = BlocklistCustomValidator{}
		Expect(validator).NotTo(BeNil(), "Expected validator to be initialized")
		defaulter = BlocklistCustomDefaulter{}
		Expect(defaulter).NotTo(BeNil(), "Expected defaulter to be initialized")
		Expect(oldObj).NotTo(BeNil(), "Expected oldObj to be initialized")
		Expect(obj).NotTo(BeNil(), "Expected obj to be initialized")
	})

	Context("When creating Blocklist under Defaulting Webhook", func() {
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

	Context("When creating or updating Blocklist under Validating Webhook", func() {
		It("admits a Blocklist with a serverRef", func() {
			obj.Spec.ServerRef = dnsv1alpha1.SecretReference{Name: testServerName}
			Expect(validator.ValidateCreate(ctx, obj)).Error().NotTo(HaveOccurred())
		})

		It("rejects a Blocklist with no serverRef", func() {
			_, err := validator.ValidateCreate(ctx, obj)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("serverRef"))
		})

		It("rejects a Blocklist whose serverRef sets a namespace", func() {
			obj.Spec.ServerRef = dnsv1alpha1.SecretReference{Name: testServerName, Namespace: "some-tenant"}
			_, err := validator.ValidateCreate(ctx, obj)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("serverRef.namespace"))
		})

		It("admits an update that keeps serverRef", func() {
			oldObj.Spec.ServerRef = dnsv1alpha1.SecretReference{Name: testServerName}
			obj.Spec.ServerRef = dnsv1alpha1.SecretReference{Name: testServerName}
			Expect(validator.ValidateUpdate(ctx, oldObj, obj)).Error().NotTo(HaveOccurred())
		})

		It("rejects an update that drops serverRef", func() {
			oldObj.Spec.ServerRef = dnsv1alpha1.SecretReference{Name: testServerName}
			_, err := validator.ValidateUpdate(ctx, oldObj, obj)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("serverRef"))
		})
	})
})
