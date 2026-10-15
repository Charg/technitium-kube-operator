/*
Copyright (c) 2026 Chris

Licensed under the MIT License. See the LICENSE file in the project root for
the full license text.
*/

package externaldns

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	dnsv1alpha1 "github.com/charg/technitium-operator/api/v1alpha1"
)

var _ = Describe("Translator", func() {
	tr := &Translator{
		Namespace: "dns", ServerRef: "main", OwnerID: "me",
		Zones: []string{"example.com", "lab.example.com"},
	}

	Describe("RecordName", func() {
		It("is deterministic, DNS-1123 valid and short", func() {
			a := RecordName("WWW.example.com.", "a", "1.2.3.4", "")
			Expect(a).To(Equal(RecordName("www.example.com", "A", "1.2.3.4", "")))
			Expect(a).To(MatchRegexp(`^edns-[0-9a-f]{32}$`))
			Expect(RecordName("www.example.com", "A", "1.2.3.5", "")).NotTo(Equal(a))
			Expect(RecordName("www.example.com", "A", "1.2.3.4", "set")).NotTo(Equal(a))
		})
	})

	DescribeTable("ZoneFor",
		func(name, zone string, ok bool) {
			z, found := tr.ZoneFor(name)
			Expect(found).To(Equal(ok))
			Expect(z).To(Equal(zone))
		},
		Entry("apex", "example.com", "example.com", true),
		Entry("subdomain", "www.example.com.", "example.com", true),
		Entry("longest suffix wins", "a.lab.example.com", "lab.example.com", true),
		Entry("label boundary", "notexample.com", "", false),
		Entry("unrelated", "foo.org", "", false),
	)

	DescribeTable("MX",
		func(target string, prio int32, host string, fails bool) {
			p, h, err := ParseMX(target)
			if fails {
				Expect(err).To(HaveOccurred())
				return
			}
			Expect(err).NotTo(HaveOccurred())
			Expect(p).To(Equal(prio))
			Expect(h).To(Equal(host))
		},
		Entry("valid", "10 mail.example.com", int32(10), "mail.example.com", false),
		Entry("no priority", "mail.example.com", int32(0), "", true),
		Entry("bad priority", "x mail.example.com", int32(0), "", true),
		Entry("too many fields", "10 a b", int32(0), "", true),
	)

	Describe("Desired", func() {
		It("builds a Record with labels, zone and TTL", func() {
			ep := &Endpoint{DNSName: "WWW.Example.com.", RecordType: "A", RecordTTL: 300, SetIdentifier: "s1"}
			r, err := tr.Desired(ep, "1.2.3.4")
			Expect(err).NotTo(HaveOccurred())
			Expect(r.Namespace).To(Equal("dns"))
			Expect(r.Labels).To(Equal(map[string]string{
				"app.kubernetes.io/managed-by":       "external-dns",
				"dns.packet.fail/external-dns-owner": "me",
			}))
			Expect(r.Annotations).To(HaveKeyWithValue(AnnotationSetIdentifier, "s1"))
			Expect(r.Spec.ServerRef.Name).To(Equal("main"))
			Expect(r.Spec.ServerRef.Namespace).To(BeEmpty())
			Expect(r.Spec.Zone).To(Equal("example.com"))
			Expect(r.Spec.Name).To(Equal("www.example.com"))
			Expect(r.Spec.Data).To(Equal("1.2.3.4"))
			Expect(*r.Spec.TTL).To(Equal(int32(300)))
		})

		It("omits the TTL when unset and the annotation when no setIdentifier", func() {
			r, err := tr.Desired(&Endpoint{DNSName: "a.example.com", RecordType: "A"}, "1.2.3.4")
			Expect(err).NotTo(HaveOccurred())
			Expect(r.Spec.TTL).To(BeNil())
			Expect(r.Annotations).To(BeEmpty())
		})

		It("splits MX targets and rejects malformed ones", func() {
			r, err := tr.Desired(&Endpoint{DNSName: "example.com", RecordType: "MX"}, "10 mail.example.com")
			Expect(err).NotTo(HaveOccurred())
			Expect(*r.Spec.Priority).To(Equal(int32(10)))
			Expect(r.Spec.Data).To(Equal("mail.example.com"))
			_, err = tr.Desired(&Endpoint{DNSName: "example.com", RecordType: "MX"}, "mail.example.com")
			Expect(err).To(HaveOccurred())
		})

		It("rejects unsupported types and names outside the zones", func() {
			_, err := tr.Desired(&Endpoint{DNSName: "a.example.com", RecordType: "SRV"}, "x")
			Expect(err).To(HaveOccurred())
			_, err = tr.Desired(&Endpoint{DNSName: "a.other.org", RecordType: "A"}, "1.2.3.4")
			Expect(err).To(HaveOccurred())
		})
	})

	Describe("Endpoints", func() {
		It("groups Records by name, type and setIdentifier with sorted targets", func() {
			ttl := int32(60)
			prio := int32(10)
			mk := func(name string, typ dnsv1alpha1.RecordType, data string, set string) dnsv1alpha1.Record {
				r := dnsv1alpha1.Record{}
				r.Spec = dnsv1alpha1.RecordSpec{Name: name, Type: typ, Data: data, TTL: &ttl}
				if set != "" {
					r.Annotations = map[string]string{AnnotationSetIdentifier: set}
				}
				return r
			}
			mx := mk("example.com", dnsv1alpha1.RecordTypeMX, "mail.example.com", "")
			mx.Spec.Priority = &prio
			eps := Endpoints([]dnsv1alpha1.Record{
				mk("www.example.com", dnsv1alpha1.RecordTypeA, "2.2.2.2", ""),
				mk("www.example.com", dnsv1alpha1.RecordTypeA, "1.1.1.1", ""),
				mk("www.example.com", dnsv1alpha1.RecordTypeA, "3.3.3.3", "other"),
				mx,
			})
			Expect(eps).To(HaveLen(3))
			Expect(eps[0].RecordType).To(Equal("MX"))
			Expect(eps[0].Targets).To(Equal([]string{"10 mail.example.com"}))
			Expect(eps[1].Targets).To(Equal([]string{"1.1.1.1", "2.2.2.2"}))
			Expect(eps[1].RecordTTL).To(Equal(int64(60)))
			Expect(eps[2].SetIdentifier).To(Equal("other"))
		})
	})

	Describe("BuildPlan", func() {
		ep := func(ttl int64, targets ...string) *Endpoint {
			return &Endpoint{DNSName: "www.example.com", RecordType: "A", RecordTTL: ttl, Targets: targets}
		}
		name := func(t string) string { return RecordName("www.example.com", "A", t, "") }

		It("creates one Record per target", func() {
			p, err := tr.BuildPlan(&Changes{Create: []*Endpoint{ep(0, "1.1.1.1", "2.2.2.2")}})
			Expect(err).NotTo(HaveOccurred())
			Expect(p.Create).To(HaveLen(2))
			Expect(p.Patch).To(BeEmpty())
			Expect(p.Delete).To(BeEmpty())
		})

		It("diffs updates per target: add, keep with new TTL, remove", func() {
			p, err := tr.BuildPlan(&Changes{
				UpdateOld: []*Endpoint{ep(60, "1.1.1.1", "2.2.2.2")},
				UpdateNew: []*Endpoint{ep(300, "2.2.2.2", "3.3.3.3")},
			})
			Expect(err).NotTo(HaveOccurred())
			Expect(p.Create).To(HaveLen(1))
			Expect(p.Create[0].Name).To(Equal(name("3.3.3.3")))
			Expect(p.Patch).To(HaveLen(1))
			Expect(p.Patch[0].Name).To(Equal(name("2.2.2.2")))
			Expect(p.Delete).To(Equal([]string{name("1.1.1.1")}))
		})

		It("leaves unchanged targets alone", func() {
			p, err := tr.BuildPlan(&Changes{
				UpdateOld: []*Endpoint{ep(60, "1.1.1.1")},
				UpdateNew: []*Endpoint{ep(60, "1.1.1.1")},
			})
			Expect(err).NotTo(HaveOccurred())
			Expect(p.Create).To(BeEmpty())
			Expect(p.Patch).To(BeEmpty())
			Expect(p.Delete).To(BeEmpty())
		})

		It("deletes every target of deleted endpoints", func() {
			p, err := tr.BuildPlan(&Changes{Delete: []*Endpoint{ep(0, "1.1.1.1", "2.2.2.2")}})
			Expect(err).NotTo(HaveOccurred())
			Expect(p.Delete).To(ConsistOf(name("1.1.1.1"), name("2.2.2.2")))
		})

		It("rejects the whole request when one endpoint is invalid", func() {
			_, err := tr.BuildPlan(&Changes{Create: []*Endpoint{
				ep(0, "1.1.1.1"), {DNSName: "x.example.com", RecordType: "MX", Targets: []string{"bad"}},
			}})
			Expect(err).To(HaveOccurred())
		})
	})

	Describe("Adjust", func() {
		It("drops unsupported types and foreign names, and normalises the rest", func() {
			out := tr.Adjust([]*Endpoint{
				{DNSName: "WWW.Example.com.", RecordType: "A", Targets: []string{"1.1.1.1"}},
				{DNSName: "srv.example.com", RecordType: "SRV"},
				{DNSName: "a.other.org", RecordType: "A"},
			})
			Expect(out).To(HaveLen(1))
			Expect(out[0].DNSName).To(Equal("www.example.com"))
		})
	})
})
