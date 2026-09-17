/*
Copyright (c) 2026 Chris

Licensed under the MIT License. See the LICENSE file in the project root for
the full license text.
*/

package controller

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	dnsv1alpha1 "github.com/charg/technitium-operator/api/v1alpha1"
	"github.com/charg/technitium-operator/internal/technitium"
)

// fakeRecordEntry is the minimal state fakeTechnitiumRecordServer keeps per
// record: just enough to answer records/get and to detect add/delete.
type fakeRecordEntry struct {
	ttl       int32
	ipAddress string
}

// fakeTechnitiumRecordServer is an in-memory stand-in for a Technitium DNS
// Server. It implements just enough of the record API (A records only, which
// is all this suite exercises) for the reconciler to drive a real
// technitium.Client against it over HTTP.
type fakeTechnitiumRecordServer struct {
	*httptest.Server

	mu      sync.Mutex
	records map[string]fakeRecordEntry // domain -> entry
	// failAdd, when set, makes /api/zones/records/add return a server error so
	// the error path can be exercised end to end.
	failAdd bool

	addCount    int
	deleteCount int
}

func newFakeTechnitiumRecordServer() *fakeTechnitiumRecordServer {
	f := &fakeTechnitiumRecordServer{records: map[string]fakeRecordEntry{}}
	f.Server = httptest.NewServer(http.HandlerFunc(f.handle))
	return f
}

func (f *fakeTechnitiumRecordServer) handle(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()

	domain := r.URL.Query().Get("domain")
	writeJSON := func(body string) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, body)
	}

	switch r.URL.Path {
	case "/api/zones/records/get":
		entry, ok := f.records[domain]
		if !ok {
			writeJSON(`{"status":"ok","response":{"records":[]}}`)
			return
		}
		writeJSON(fmt.Sprintf(`{"status":"ok","response":{"records":[`+
			`{"name":%q,"type":"A","ttl":%d,"rData":{"ipAddress":%q}}]}}`,
			domain, entry.ttl, entry.ipAddress))
	case "/api/zones/records/add":
		if f.failAdd {
			writeJSON(`{"status":"error","errorMessage":"internal server error"}`)
			return
		}
		f.addCount++
		ttl := int32(3600)
		f.records[domain] = fakeRecordEntry{ttl: ttl, ipAddress: r.URL.Query().Get("ipAddress")}
		writeJSON(`{"status":"ok"}`)
	case "/api/zones/records/delete":
		if _, ok := f.records[domain]; !ok {
			writeJSON(`{"status":"error","errorMessage":"No such record was found: ` + domain + `"}`)
			return
		}
		f.deleteCount++
		delete(f.records, domain)
		writeJSON(`{"status":"ok"}`)
	case "/api/zones/records/update":
		writeJSON(`{"status":"ok"}`)
	default:
		writeJSON(`{"status":"ok"}`)
	}
}

func (f *fakeTechnitiumRecordServer) has(domain string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, ok := f.records[domain]
	return ok
}

var _ = Describe("Record Controller against a fake Technitium server", func() {
	const resourceName = "integration-record"
	const recordDomain = "integration.example.com"
	const recordZone = "example.com"
	const serverName = "integration-server"

	ctx := context.Background()
	key := types.NamespacedName{Name: resourceName, Namespace: "default"}

	var server *fakeTechnitiumRecordServer
	var reconciler *RecordReconciler

	BeforeEach(func() {
		server = newFakeTechnitiumRecordServer()
		tech, err := technitium.NewClient(server.URL, technitium.WithToken("test-token"))
		Expect(err).NotTo(HaveOccurred())
		reconciler = &RecordReconciler{
			Client:            k8sClient,
			Scheme:            k8sClient.Scheme(),
			OperatorNamespace: "default",
			NewServerClient: func(_ context.Context, _ dnsv1alpha1.SecretReference) (RecordAPI, error) {
				return tech, nil
			},
		}

		record := &dnsv1alpha1.Record{
			Name:      resourceName,
			Namespace: "default",
			Spec: dnsv1alpha1.RecordSpec{
				ServerRef: dnsv1alpha1.SecretReference{Name: serverName},
				Zone:      recordZone,
				Name:      recordDomain,
				Type:      dnsv1alpha1.RecordTypeA,
				Data:      "1.1.1.1",
			},
		}
		Expect(k8sClient.Create(ctx, record)).To(Succeed())
	})

	AfterEach(func() {
		resource := &dnsv1alpha1.Record{}
		if err := k8sClient.Get(ctx, key, resource); err == nil {
			if len(resource.Finalizers) > 0 {
				patch := client.MergeFrom(resource.DeepCopy())
				resource.Finalizers = nil
				_ = k8sClient.Patch(ctx, resource, patch)
			}
			_ = k8sClient.Delete(ctx, resource)
		}
		server.Close()
	})

	It("creates the record and marks it Ready", func() {
		_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: key})
		Expect(err).NotTo(HaveOccurred())

		Expect(server.has(recordDomain)).To(BeTrue())
		Expect(server.addCount).To(Equal(1))

		record := &dnsv1alpha1.Record{}
		Expect(k8sClient.Get(ctx, key, record)).To(Succeed())
		Expect(meta.IsStatusConditionTrue(record.Status.Conditions, conditionReady)).To(BeTrue())
	})

	It("is idempotent across repeated reconciles", func() {
		_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: key})
		Expect(err).NotTo(HaveOccurred())
		_, err = reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: key})
		Expect(err).NotTo(HaveOccurred())

		// The second reconcile finds the record present with matching rdata and
		// TTL, so it does not recreate it.
		Expect(server.addCount).To(Equal(1))
	})

	It("deletes the record from the server and clears the finalizer", func() {
		_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: key})
		Expect(err).NotTo(HaveOccurred())

		resource := &dnsv1alpha1.Record{}
		Expect(k8sClient.Get(ctx, key, resource)).To(Succeed())
		Expect(k8sClient.Delete(ctx, resource)).To(Succeed())

		_, err = reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: key})
		Expect(err).NotTo(HaveOccurred())

		Expect(server.deleteCount).To(Equal(1))
		Expect(server.has(recordDomain)).To(BeFalse())
		Expect(k8sClient.Get(ctx, key, resource)).To(MatchError(ContainSubstring("not found")))
	})

	It("marks the record Degraded when the server rejects the add", func() {
		server.failAdd = true

		_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: key})
		Expect(err).To(HaveOccurred())

		record := &dnsv1alpha1.Record{}
		Expect(k8sClient.Get(ctx, key, record)).To(Succeed())
		Expect(meta.IsStatusConditionTrue(record.Status.Conditions, conditionDegraded)).To(BeTrue())
		Expect(meta.IsStatusConditionFalse(record.Status.Conditions, conditionReady)).To(BeTrue())
	})
})
