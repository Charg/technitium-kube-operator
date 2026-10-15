/*
Copyright (c) 2026 Chris

Licensed under the MIT License. See the LICENSE file in the project root for
the full license text.
*/

package externaldns

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	dnsv1alpha1 "github.com/charg/technitium-operator/api/v1alpha1"
)

var _ = Describe("Server", func() {
	var (
		scheme *runtime.Scheme
		c      client.Client
		h      http.Handler
		tr     Translator
	)
	ctx := context.Background()

	build := func(funcs interceptor.Funcs) {
		c = fake.NewClientBuilder().WithScheme(scheme).WithInterceptorFuncs(funcs).Build()
		h = (&Server{Client: c, T: tr}).Handler()
	}
	do := func(method, path, body string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(method, path, strings.NewReader(body)))
		return rec
	}
	list := func() []dnsv1alpha1.Record {
		var l dnsv1alpha1.RecordList
		Expect(c.List(ctx, &l, client.InNamespace("dns"))).To(Succeed())
		return l.Items
	}

	BeforeEach(func() {
		scheme = runtime.NewScheme()
		Expect(dnsv1alpha1.AddToScheme(scheme)).To(Succeed())
		tr = Translator{Namespace: "dns", ServerRef: "main", OwnerID: "me", Zones: []string{"example.com"}}
		build(interceptor.Funcs{})
	})

	It("negotiates the domain filter with the webhook media type", func() {
		rec := do("GET", "/", "")
		Expect(rec.Code).To(Equal(http.StatusOK))
		Expect(rec.Header().Get("Content-Type")).To(Equal(MediaType))
		var f DomainFilter
		Expect(json.Unmarshal(rec.Body.Bytes(), &f)).To(Succeed())
		Expect(f.Include).To(Equal([]string{"example.com"}))
	})

	It("creates one Record per target and lists them back as one endpoint", func() {
		rec := do("POST", "/records", `{"create":[{"dnsName":"www.example.com","recordType":"A","recordTTL":120,"targets":["2.2.2.2","1.1.1.1"]}]}`)
		Expect(rec.Code).To(Equal(http.StatusNoContent))
		Expect(list()).To(HaveLen(2))

		// A Record from another owner must not show up.
		other := &dnsv1alpha1.Record{}
		other.Name, other.Namespace = "foreign", "dns"
		other.Labels = map[string]string{LabelManagedBy: LabelManagedByValue, LabelOwner: "someone-else"}
		other.Spec = dnsv1alpha1.RecordSpec{Name: "x.example.com", Type: "A", Data: "9.9.9.9", Zone: "example.com"}
		Expect(c.Create(ctx, other)).To(Succeed())

		rec = do("GET", "/records", "")
		Expect(rec.Code).To(Equal(http.StatusOK))
		Expect(rec.Header().Get("Content-Type")).To(Equal(MediaType))
		var eps []Endpoint
		Expect(json.Unmarshal(rec.Body.Bytes(), &eps)).To(Succeed())
		Expect(eps).To(HaveLen(1))
		Expect(eps[0].Targets).To(Equal([]string{"1.1.1.1", "2.2.2.2"}))
		Expect(eps[0].RecordTTL).To(Equal(int64(120)))
	})

	It("patches an existing Record when create hits AlreadyExists", func() {
		body := `{"create":[{"dnsName":"www.example.com","recordType":"A","recordTTL":60,"targets":["1.1.1.1"]}]}`
		Expect(do("POST", "/records", body).Code).To(Equal(http.StatusNoContent))
		body = strings.Replace(body, "60", "600", 1)
		Expect(do("POST", "/records", body).Code).To(Equal(http.StatusNoContent))
		items := list()
		Expect(items).To(HaveLen(1))
		Expect(*items[0].Spec.TTL).To(Equal(int32(600)))
	})

	It("applies updates: creates before deletes, TTL patch on kept targets", func() {
		seedBody := `{"create":[{"dnsName":"www.example.com","recordType":"A","recordTTL":60,"targets":["1.1.1.1","2.2.2.2"]}]}`
		Expect(do("POST", "/records", seedBody).Code).To(Equal(http.StatusNoContent))
		seed := list()
		var ops []string
		record := func(op string) { ops = append(ops, op) }
		objs := make([]client.Object, 0, len(seed))
		for i := range seed {
			seed[i].ResourceVersion = ""
			objs = append(objs, &seed[i])
		}
		c = fake.NewClientBuilder().WithScheme(scheme).WithObjects(objs...).WithInterceptorFuncs(interceptor.Funcs{
			Create: func(ctx context.Context, cl client.WithWatch, obj client.Object, o ...client.CreateOption) error {
				record("create")
				return cl.Create(ctx, obj, o...)
			},
			Patch: func(ctx context.Context, cl client.WithWatch, obj client.Object, p client.Patch, o ...client.PatchOption) error {
				record("patch")
				return cl.Patch(ctx, obj, p, o...)
			},
			Delete: func(ctx context.Context, cl client.WithWatch, obj client.Object, o ...client.DeleteOption) error {
				record("delete")
				return cl.Delete(ctx, obj, o...)
			},
		}).Build()
		h = (&Server{Client: c, T: tr}).Handler()

		rec := do("POST", "/records", `{
			"updateOld":[{"dnsName":"www.example.com","recordType":"A","recordTTL":60,"targets":["1.1.1.1","2.2.2.2"]}],
			"updateNew":[{"dnsName":"www.example.com","recordType":"A","recordTTL":300,"targets":["2.2.2.2","3.3.3.3"]}]}`)
		Expect(rec.Code).To(Equal(http.StatusNoContent))
		Expect(ops).To(Equal([]string{"create", "patch", "delete"}))
		targets := make([]string, 0, 2)
		for _, r := range list() {
			targets = append(targets, r.Spec.Data)
		}
		Expect(targets).To(ConsistOf("2.2.2.2", "3.3.3.3"))
	})

	It("ignores NotFound on delete", func() {
		rec := do("POST", "/records", `{"delete":[{"dnsName":"gone.example.com","recordType":"A","targets":["1.1.1.1"]}]}`)
		Expect(rec.Code).To(Equal(http.StatusNoContent))
	})

	It("returns 400 for malformed JSON and invalid endpoints", func() {
		Expect(do("POST", "/records", `{`).Code).To(Equal(http.StatusBadRequest))
		Expect(do("POST", "/records", `{"create":[{"dnsName":"example.com","recordType":"MX","targets":["bad"]}]}`).Code).
			To(Equal(http.StatusBadRequest))
		Expect(list()).To(BeEmpty())
	})

	It("returns 500 when the API server fails", func() {
		build(interceptor.Funcs{Create: func(context.Context, client.WithWatch, client.Object, ...client.CreateOption) error {
			return context.DeadlineExceeded
		}})
		rec := do("POST", "/records", `{"create":[{"dnsName":"www.example.com","recordType":"A","targets":["1.1.1.1"]}]}`)
		Expect(rec.Code).To(Equal(http.StatusInternalServerError))
	})

	It("filters endpoints on /adjustendpoints", func() {
		rec := do("POST", "/adjustendpoints", `[{"dnsName":"WWW.example.com.","recordType":"A","targets":["1.1.1.1"]},{"dnsName":"a.example.com","recordType":"SRV"},{"dnsName":"a.other.org","recordType":"A"}]`)
		Expect(rec.Code).To(Equal(http.StatusOK))
		Expect(rec.Header().Get("Content-Type")).To(Equal(MediaType))
		var eps []Endpoint
		Expect(json.Unmarshal(rec.Body.Bytes(), &eps)).To(Succeed())
		Expect(eps).To(HaveLen(1))
		Expect(eps[0].DNSName).To(Equal("www.example.com"))
	})
})
