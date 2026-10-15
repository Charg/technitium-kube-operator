/*
Copyright (c) 2026 Chris

Licensed under the MIT License. See the LICENSE file in the project root for
the full license text.
*/

package externaldns

import (
	"cmp"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"

	dnsv1alpha1 "github.com/charg/technitium-operator/api/v1alpha1"
)

const (
	// LabelManagedBy and its value mark Records owned by ExternalDNS.
	LabelManagedBy      = "app.kubernetes.io/managed-by"
	LabelManagedByValue = "external-dns"
	// LabelOwner records which webhook instance (--owner-id) owns a Record.
	LabelOwner = "dns.packet.fail/external-dns-owner"
	// AnnotationSetIdentifier stores the endpoint's setIdentifier.
	AnnotationSetIdentifier = "dns.packet.fail/external-dns-set-identifier"
)

// supportedTypes are the record types the Record CRD can represent.
var supportedTypes = map[string]dnsv1alpha1.RecordType{
	"A": dnsv1alpha1.RecordTypeA, "AAAA": dnsv1alpha1.RecordTypeAAAA,
	"CNAME": dnsv1alpha1.RecordTypeCNAME, "TXT": dnsv1alpha1.RecordTypeTXT,
	"NS": dnsv1alpha1.RecordTypeNS, "PTR": dnsv1alpha1.RecordTypePTR,
	"MX": dnsv1alpha1.RecordTypeMX,
}

// Translator converts between ExternalDNS endpoints and Record resources.
type Translator struct {
	Namespace string
	ServerRef string
	OwnerID   string
	Zones     []string
}

// RecordName returns the deterministic Record object name for one target of
// an endpoint.
func RecordName(dnsName, recordType, target, setIdentifier string) string {
	h := sha256.New()
	for _, p := range []string{normalizeName(dnsName), strings.ToUpper(recordType), target, setIdentifier} {
		h.Write([]byte(p))
		h.Write([]byte{0})
	}
	return "edns-" + hex.EncodeToString(h.Sum(nil)[:16])
}

// ZoneFor returns the longest configured zone that contains name on a label
// boundary, or false when there is none.
func (t *Translator) ZoneFor(name string) (string, bool) {
	name = normalizeName(name)
	best := ""
	for _, z := range t.Zones {
		z = normalizeName(z)
		if (name == z || strings.HasSuffix(name, "."+z)) && len(z) > len(best) {
			best = z
		}
	}
	return best, best != ""
}

// ParseMX splits an ExternalDNS MX target "<priority> <host>".
func ParseMX(target string) (int32, string, error) {
	f := strings.Fields(target)
	if len(f) != 2 {
		return 0, "", fmt.Errorf("malformed MX target %q: want \"<priority> <host>\"", target)
	}
	p, err := strconv.ParseUint(f[0], 10, 16)
	if err != nil {
		return 0, "", fmt.Errorf("malformed MX target %q: invalid priority: %w", target, err)
	}
	return int32(p), f[1], nil
}

// Desired builds the Record for one target of an endpoint.
func (t *Translator) Desired(ep *Endpoint, target string) (*dnsv1alpha1.Record, error) {
	rt, ok := supportedTypes[strings.ToUpper(ep.RecordType)]
	if !ok {
		return nil, fmt.Errorf("unsupported record type %q for %s", ep.RecordType, ep.DNSName)
	}
	name := normalizeName(ep.DNSName)
	zone, ok := t.ZoneFor(name)
	if !ok {
		return nil, fmt.Errorf("%s is not in any configured zone", ep.DNSName)
	}
	rec := &dnsv1alpha1.Record{
		Name:      RecordName(ep.DNSName, ep.RecordType, target, ep.SetIdentifier),
		Namespace: t.Namespace,
		Labels:    t.Selector(),
		Spec: dnsv1alpha1.RecordSpec{
			ServerRef: dnsv1alpha1.SecretReference{Name: t.ServerRef},
			Zone:      zone,
			Name:      name,
			Type:      rt,
			Data:      target,
		},
	}
	if ep.SetIdentifier != "" {
		rec.Annotations = map[string]string{AnnotationSetIdentifier: ep.SetIdentifier}
	}
	if rt == dnsv1alpha1.RecordTypeMX {
		prio, host, err := ParseMX(target)
		if err != nil {
			return nil, err
		}
		rec.Spec.Priority, rec.Spec.Data = &prio, host
	}
	if ep.RecordTTL > 0 {
		ttl := int32(min(ep.RecordTTL, math.MaxInt32)) //nolint:gosec // clamped
		rec.Spec.TTL = &ttl
	}
	return rec, nil
}

// Selector is the label set identifying Records owned by this webhook.
func (t *Translator) Selector() map[string]string {
	return map[string]string{LabelManagedBy: LabelManagedByValue, LabelOwner: t.OwnerID}
}

// Endpoints groups Records by (name, type, setIdentifier), one endpoint per
// group with sorted targets.
func Endpoints(records []dnsv1alpha1.Record) []*Endpoint {
	type key struct{ name, typ, set string }
	groups := map[key]*Endpoint{}
	var order []key
	for i := range records {
		r := &records[i]
		k := key{r.Spec.Name, string(r.Spec.Type), r.Annotations[AnnotationSetIdentifier]}
		ep, ok := groups[k]
		if !ok {
			ep = &Endpoint{DNSName: k.name, RecordType: k.typ, SetIdentifier: k.set}
			if r.Spec.TTL != nil {
				ep.RecordTTL = int64(*r.Spec.TTL)
			}
			groups[k] = ep
			order = append(order, k)
		}
		target := r.Spec.Data
		if r.Spec.Type == dnsv1alpha1.RecordTypeMX && r.Spec.Priority != nil {
			target = fmt.Sprintf("%d %s", *r.Spec.Priority, r.Spec.Data)
		}
		ep.Targets = append(ep.Targets, target)
	}
	out := make([]*Endpoint, 0, len(order))
	for _, k := range order {
		ep := groups[k]
		slices.Sort(ep.Targets)
		out = append(out, ep)
	}
	slices.SortStableFunc(out, func(a, b *Endpoint) int {
		return cmp.Or(
			strings.Compare(a.DNSName, b.DNSName),
			strings.Compare(a.RecordType, b.RecordType),
			strings.Compare(a.SetIdentifier, b.SetIdentifier),
		)
	})
	return out
}

// Plan is the ordered set of Record operations for a Changes request.
type Plan struct {
	Create []*dnsv1alpha1.Record // create, or patch when already present
	Patch  []*dnsv1alpha1.Record // kept targets whose TTL changed
	Delete []string              // Record object names
}

type epKey struct{ name, typ, set string }

func keyOf(ep *Endpoint) epKey {
	return epKey{normalizeName(ep.DNSName), strings.ToUpper(ep.RecordType), ep.SetIdentifier}
}

// BuildPlan translates Changes into a Plan. Nothing is applied, so malformed
// input is rejected before any Record is touched.
func (t *Translator) BuildPlan(c *Changes) (*Plan, error) {
	p := &Plan{}
	deleted := map[string]bool{}
	del := func(ep *Endpoint, target string) {
		n := RecordName(ep.DNSName, ep.RecordType, target, ep.SetIdentifier)
		if !deleted[n] {
			deleted[n] = true
			p.Delete = append(p.Delete, n)
		}
	}

	for _, ep := range c.Create {
		for _, tg := range ep.Targets {
			r, err := t.Desired(ep, tg)
			if err != nil {
				return nil, err
			}
			p.Create = append(p.Create, r)
		}
	}

	olds := map[epKey]*Endpoint{}
	var oldOrder []epKey
	for _, ep := range c.UpdateOld {
		if _, dup := olds[keyOf(ep)]; !dup {
			oldOrder = append(oldOrder, keyOf(ep))
		}
		olds[keyOf(ep)] = ep
	}
	seen := map[epKey]bool{}
	for _, nw := range c.UpdateNew {
		old := olds[keyOf(nw)]
		seen[keyOf(nw)] = true
		oldSet := map[string]bool{}
		if old != nil {
			for _, tg := range old.Targets {
				oldSet[tg] = true
			}
		}
		newSet := map[string]bool{}
		for _, tg := range nw.Targets {
			newSet[tg] = true
			r, err := t.Desired(nw, tg)
			if err != nil {
				return nil, err
			}
			switch {
			case !oldSet[tg]:
				p.Create = append(p.Create, r)
			case old.RecordTTL != nw.RecordTTL:
				p.Patch = append(p.Patch, r)
			}
		}
		if old != nil {
			for _, tg := range old.Targets {
				if !newSet[tg] {
					del(old, tg)
				}
			}
		}
	}
	for _, k := range oldOrder {
		if !seen[k] {
			for _, tg := range olds[k].Targets {
				del(olds[k], tg)
			}
		}
	}
	for _, ep := range c.Delete {
		for _, tg := range ep.Targets {
			del(ep, tg)
		}
	}
	return p, nil
}

// Adjust drops endpoints the webhook cannot represent and normalises names.
func (t *Translator) Adjust(in []*Endpoint) []*Endpoint {
	out := make([]*Endpoint, 0, len(in))
	for _, ep := range in {
		if ep == nil {
			continue
		}
		if _, ok := supportedTypes[strings.ToUpper(ep.RecordType)]; !ok {
			continue
		}
		if _, ok := t.ZoneFor(ep.DNSName); !ok {
			continue
		}
		c := *ep
		c.DNSName = normalizeName(ep.DNSName)
		c.RecordType = strings.ToUpper(ep.RecordType)
		out = append(out, &c)
	}
	return out
}
