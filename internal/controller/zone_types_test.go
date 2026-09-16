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
var _ = Describe("Zone CRD validation", func() {
	ctx := context.Background()

	newZone := func(name string) *dnsv1alpha1.Zone {
		return &dnsv1alpha1.Zone{
			Name: name,
			Spec: dnsv1alpha1.ZoneSpec{
				ZoneName:  name + ".example.com",
				ServerRef: dnsv1alpha1.SecretReference{Name: "dns"},
			},
		}
	}

	It("defaults type to Primary when unset", func() {
		zone := newZone("defaulting")
		Expect(k8sClient.Create(ctx, zone)).To(Succeed())
		DeferCleanup(func() { Expect(k8sClient.Delete(ctx, zone)).To(Succeed()) })

		Expect(zone.Spec.Type).To(Equal(dnsv1alpha1.ZoneTypePrimary))
	})

	It("rejects a zone with no zoneName", func() {
		zone := &dnsv1alpha1.Zone{Name: "no-name"}
		Expect(k8sClient.Create(ctx, zone)).NotTo(Succeed())
	})

	It("rejects a zoneName that is not a DNS name", func() {
		zone := &dnsv1alpha1.Zone{
			Name: "bad-name",
			Spec: dnsv1alpha1.ZoneSpec{ZoneName: "not a dns name"},
		}
		Expect(k8sClient.Create(ctx, zone)).NotTo(Succeed())
	})

	It("rejects an unknown zone type", func() {
		zone := newZone("bad-type")
		zone.Spec.Type = dnsv1alpha1.ZoneType("Bogus")
		Expect(k8sClient.Create(ctx, zone)).NotTo(Succeed())
	})

	It("rejects an unknown forwarder protocol", func() {
		zone := newZone("bad-protocol")
		zone.Spec.Type = dnsv1alpha1.ZoneTypeForwarder
		proto := dnsv1alpha1.ForwarderProtocol("Carrier")
		zone.Spec.ForwarderProtocol = &proto
		Expect(k8sClient.Create(ctx, zone)).NotTo(Succeed())
	})

	It("rejects a change to zoneName", func() {
		zone := newZone("immutable")
		Expect(k8sClient.Create(ctx, zone)).To(Succeed())
		DeferCleanup(func() { Expect(k8sClient.Delete(ctx, zone)).To(Succeed()) })

		zone.Spec.ZoneName = "renamed.example.com"
		Expect(k8sClient.Update(ctx, zone)).NotTo(Succeed())
	})
})
