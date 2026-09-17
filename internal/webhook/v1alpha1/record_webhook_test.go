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

const testRecordName = "www.example.com"

var _ = Describe("Record Webhook", func() {
	var (
		obj       *dnsv1alpha1.Record
		oldObj    *dnsv1alpha1.Record
		validator RecordCustomValidator
		defaulter RecordCustomDefaulter
	)

	BeforeEach(func() {
		obj = &dnsv1alpha1.Record{}
		oldObj = &dnsv1alpha1.Record{}
		validator = RecordCustomValidator{}
		Expect(validator).NotTo(BeNil(), "Expected validator to be initialized")
		defaulter = RecordCustomDefaulter{}
		Expect(defaulter).NotTo(BeNil(), "Expected defaulter to be initialized")
		Expect(oldObj).NotTo(BeNil(), "Expected oldObj to be initialized")
		Expect(obj).NotTo(BeNil(), "Expected obj to be initialized")
	})

	Context("When creating Record under Defaulting Webhook", func() {
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

	Context("When creating or updating Record under Validating Webhook", func() {
		It("admits an A record with a name, type, and data", func() {
			obj.Spec.Name = testRecordName
			obj.Spec.ServerRef = dnsv1alpha1.SecretReference{Name: testServerName}
			obj.Spec.Type = dnsv1alpha1.RecordTypeA
			obj.Spec.Data = "1.1.1.1"
			Expect(validator.ValidateCreate(ctx, obj)).Error().NotTo(HaveOccurred())
		})

		It("rejects a record with no serverRef", func() {
			obj.Spec.Name = testRecordName
			obj.Spec.Type = dnsv1alpha1.RecordTypeA
			obj.Spec.Data = "1.1.1.1"
			_, err := validator.ValidateCreate(ctx, obj)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("serverRef"))
		})

		It("rejects a record whose serverRef sets a namespace", func() {
			obj.Spec.Name = testRecordName
			obj.Spec.Type = dnsv1alpha1.RecordTypeA
			obj.Spec.Data = "1.1.1.1"
			obj.Spec.ServerRef = dnsv1alpha1.SecretReference{Name: testServerName, Namespace: "some-tenant"}
			_, err := validator.ValidateCreate(ctx, obj)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("serverRef.namespace"))
		})

		It("rejects an MX record without a priority", func() {
			obj.Spec.Name = testRecordName
			obj.Spec.ServerRef = dnsv1alpha1.SecretReference{Name: testServerName}
			obj.Spec.Type = dnsv1alpha1.RecordTypeMX
			obj.Spec.Data = "mail.example.com"
			_, err := validator.ValidateCreate(ctx, obj)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("priority"))
		})

		It("admits an MX record with a priority", func() {
			pref := int32(10)
			obj.Spec.Name = testRecordName
			obj.Spec.ServerRef = dnsv1alpha1.SecretReference{Name: testServerName}
			obj.Spec.Type = dnsv1alpha1.RecordTypeMX
			obj.Spec.Data = "mail.example.com"
			obj.Spec.Priority = &pref
			Expect(validator.ValidateCreate(ctx, obj)).Error().NotTo(HaveOccurred())
		})

		It("rejects a changed name on update", func() {
			oldObj.Spec.Name = testRecordName
			obj.Spec.Name = "renamed.example.com"
			obj.Spec.ServerRef = dnsv1alpha1.SecretReference{Name: testServerName}
			obj.Spec.Type = dnsv1alpha1.RecordTypeA
			obj.Spec.Data = "1.1.1.1"
			_, err := validator.ValidateUpdate(ctx, oldObj, obj)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("immutable"))
		})

		It("admits an update that keeps name", func() {
			oldObj.Spec.Name = testRecordName
			obj.Spec.Name = testRecordName
			obj.Spec.ServerRef = dnsv1alpha1.SecretReference{Name: testServerName}
			obj.Spec.Type = dnsv1alpha1.RecordTypeA
			obj.Spec.Data = "1.1.1.1"
			Expect(validator.ValidateUpdate(ctx, oldObj, obj)).Error().NotTo(HaveOccurred())
		})
	})
})
