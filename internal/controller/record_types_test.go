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

	dnsv1alpha1 "github.com/charg/technitium-operator/api/v1alpha1"
)

// These specs assert the CRD schema as enforced by the API server (envtest),
// not controller behavior.
var _ = Describe("Record CRD validation", func() {
	ctx := context.Background()

	newRecord := func(name string) *dnsv1alpha1.Record {
		return &dnsv1alpha1.Record{
			Name:      name,
			Namespace: "default",
			Spec: dnsv1alpha1.RecordSpec{
				ServerRef: dnsv1alpha1.SecretReference{Name: "dns"},
				Zone:      "example.com",
				Name:      name + ".example.com",
				Type:      dnsv1alpha1.RecordTypeA,
				Data:      "1.1.1.1",
			},
		}
	}

	It("defaults ttl to 3600 and deletionPolicy to Delete when unset", func() {
		record := newRecord("defaulting")
		Expect(k8sClient.Create(ctx, record)).To(Succeed())
		DeferCleanup(func() { Expect(k8sClient.Delete(ctx, record)).To(Succeed()) })

		Expect(record.Spec.TTL).NotTo(BeNil())
		Expect(*record.Spec.TTL).To(Equal(int32(3600)))
		Expect(record.Spec.DeletionPolicy).To(Equal(dnsv1alpha1.DeletionPolicyDelete))
	})

	It("rejects a record with no name", func() {
		record := &dnsv1alpha1.Record{Name: "no-name", Namespace: "default"}
		Expect(k8sClient.Create(ctx, record)).NotTo(Succeed())
	})

	It("rejects an unknown record type", func() {
		record := newRecord("bad-type")
		record.Spec.Type = dnsv1alpha1.RecordType("Bogus")
		Expect(k8sClient.Create(ctx, record)).NotTo(Succeed())
	})

	It("rejects a negative ttl", func() {
		record := newRecord("bad-ttl")
		ttl := int32(-1)
		record.Spec.TTL = &ttl
		Expect(k8sClient.Create(ctx, record)).NotTo(Succeed())
	})

	It("rejects a change to name", func() {
		record := newRecord("immutable")
		Expect(k8sClient.Create(ctx, record)).To(Succeed())
		DeferCleanup(func() { Expect(k8sClient.Delete(ctx, record)).To(Succeed()) })

		record.Spec.Name = "renamed.example.com"
		Expect(k8sClient.Update(ctx, record)).NotTo(Succeed())
	})
})
