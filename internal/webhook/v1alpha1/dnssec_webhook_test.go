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

var _ = Describe("DNSSEC Webhook", func() {
	var (
		obj       *dnsv1alpha1.DNSSEC
		oldObj    *dnsv1alpha1.DNSSEC
		validator DNSSECCustomValidator
		defaulter DNSSECCustomDefaulter
	)

	BeforeEach(func() {
		obj = &dnsv1alpha1.DNSSEC{}
		oldObj = &dnsv1alpha1.DNSSEC{}
		validator = DNSSECCustomValidator{}
		Expect(validator).NotTo(BeNil(), "Expected validator to be initialized")
		defaulter = DNSSECCustomDefaulter{}
		Expect(defaulter).NotTo(BeNil(), "Expected defaulter to be initialized")
		Expect(oldObj).NotTo(BeNil(), "Expected oldObj to be initialized")
		Expect(obj).NotTo(BeNil(), "Expected obj to be initialized")
	})

	Context("When creating DNSSEC under Defaulting Webhook", func() {
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

		It("defaults an empty nsecType to NSEC", func() {
			obj.Spec.NSECType = ""
			Expect(defaulter.Default(ctx, obj)).To(Succeed())
			Expect(obj.Spec.NSECType).To(Equal(dnsv1alpha1.NSECTypeNSEC))
		})

		It("leaves an explicit nsecType untouched", func() {
			obj.Spec.NSECType = dnsv1alpha1.NSECTypeNSEC3
			Expect(defaulter.Default(ctx, obj)).To(Succeed())
			Expect(obj.Spec.NSECType).To(Equal(dnsv1alpha1.NSECTypeNSEC3))
		})
	})

	Context("When creating or updating DNSSEC under Validating Webhook", func() {
		It("admits a DNSSEC with a serverRef, zone, and algorithm", func() {
			obj.Spec.ServerRef = dnsv1alpha1.SecretReference{Name: testServerName}
			obj.Spec.Zone = "example.com"
			obj.Spec.Algorithm = dnsv1alpha1.DNSSECAlgorithmECDSA
			Expect(validator.ValidateCreate(ctx, obj)).Error().NotTo(HaveOccurred())
		})

		It("rejects a DNSSEC with no serverRef", func() {
			obj.Spec.Zone = "example.com"
			_, err := validator.ValidateCreate(ctx, obj)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("serverRef"))
		})

		It("rejects a DNSSEC whose serverRef sets a namespace", func() {
			obj.Spec.ServerRef = dnsv1alpha1.SecretReference{Name: testServerName, Namespace: "some-tenant"}
			obj.Spec.Zone = "example.com"
			_, err := validator.ValidateCreate(ctx, obj)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("serverRef.namespace"))
		})

		It("rejects a DNSSEC with an empty zone", func() {
			obj.Spec.ServerRef = dnsv1alpha1.SecretReference{Name: testServerName}
			_, err := validator.ValidateCreate(ctx, obj)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("zone"))
		})

		It("rejects RSA with a curve set", func() {
			curve := dnsv1alpha1.DNSSECCurveP256
			obj.Spec.ServerRef = dnsv1alpha1.SecretReference{Name: testServerName}
			obj.Spec.Zone = "example.com"
			obj.Spec.Algorithm = dnsv1alpha1.DNSSECAlgorithmRSA
			obj.Spec.Curve = &curve
			_, err := validator.ValidateCreate(ctx, obj)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("curve"))
		})

		It("rejects ECDSA with a hashAlgorithm set", func() {
			hashAlgorithm := dnsv1alpha1.DNSSECHashAlgorithmSHA256
			obj.Spec.ServerRef = dnsv1alpha1.SecretReference{Name: testServerName}
			obj.Spec.Zone = "example.com"
			obj.Spec.Algorithm = dnsv1alpha1.DNSSECAlgorithmECDSA
			obj.Spec.HashAlgorithm = &hashAlgorithm
			_, err := validator.ValidateCreate(ctx, obj)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("hashAlgorithm"))
		})

		It("rejects ECDSA with a kskKeySize set", func() {
			kskKeySize := int32(2048)
			obj.Spec.ServerRef = dnsv1alpha1.SecretReference{Name: testServerName}
			obj.Spec.Zone = "example.com"
			obj.Spec.Algorithm = dnsv1alpha1.DNSSECAlgorithmECDSA
			obj.Spec.KSKKeySize = &kskKeySize
			_, err := validator.ValidateCreate(ctx, obj)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("kskKeySize"))
		})

		It("rejects ECDSA with a zskKeySize set", func() {
			zskKeySize := int32(1024)
			obj.Spec.ServerRef = dnsv1alpha1.SecretReference{Name: testServerName}
			obj.Spec.Zone = "example.com"
			obj.Spec.Algorithm = dnsv1alpha1.DNSSECAlgorithmECDSA
			obj.Spec.ZSKKeySize = &zskKeySize
			_, err := validator.ValidateCreate(ctx, obj)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("zskKeySize"))
		})

		It("admits an update that keeps serverRef", func() {
			oldObj.Spec.ServerRef = dnsv1alpha1.SecretReference{Name: testServerName}
			oldObj.Spec.Zone = "example.com"
			obj.Spec.ServerRef = dnsv1alpha1.SecretReference{Name: testServerName}
			obj.Spec.Zone = "example.com"
			obj.Spec.Algorithm = dnsv1alpha1.DNSSECAlgorithmECDSA
			Expect(validator.ValidateUpdate(ctx, oldObj, obj)).Error().NotTo(HaveOccurred())
		})

		It("rejects an update that drops serverRef", func() {
			oldObj.Spec.ServerRef = dnsv1alpha1.SecretReference{Name: testServerName}
			oldObj.Spec.Zone = "example.com"
			obj.Spec.Zone = "example.com"
			_, err := validator.ValidateUpdate(ctx, oldObj, obj)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("serverRef"))
		})
	})
})
