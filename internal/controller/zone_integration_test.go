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

// fakeTechnitiumServer is an in-memory stand-in for a Technitium DNS Server. It
// implements just enough of the zone API for the reconciler to drive a real
// technitium.Client against it over HTTP.
type fakeTechnitiumServer struct {
	*httptest.Server

	mu    sync.Mutex
	zones map[string]string // zone name -> type
	// failCreate, when set, makes /api/zones/create return a server error so the
	// error path can be exercised end to end.
	failCreate bool

	createCount int
	deleteCount int
}

func newFakeTechnitiumServer() *fakeTechnitiumServer {
	f := &fakeTechnitiumServer{zones: map[string]string{}}
	f.Server = httptest.NewServer(http.HandlerFunc(f.handle))
	return f
}

func (f *fakeTechnitiumServer) handle(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()

	zone := r.URL.Query().Get("zone")
	writeJSON := func(body string) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, body)
	}

	switch r.URL.Path {
	case "/api/zones/options/get":
		if _, ok := f.zones[zone]; !ok {
			writeJSON(`{"status":"error","errorMessage":"No such zone was found: ` + zone + `"}`)
			return
		}
		writeJSON(fmt.Sprintf(`{"status":"ok","response":{"name":%q,"type":%q}}`, zone, f.zones[zone]))
	case "/api/zones/create":
		if f.failCreate {
			writeJSON(`{"status":"error","errorMessage":"internal server error"}`)
			return
		}
		if _, ok := f.zones[zone]; ok {
			writeJSON(`{"status":"error","errorMessage":"Zone already exists: ` + zone + `"}`)
			return
		}
		f.createCount++
		typ := r.URL.Query().Get("type")
		if typ == "" {
			typ = string(dnsv1alpha1.ZoneTypePrimary)
		}
		f.zones[zone] = typ
		writeJSON(`{"status":"ok"}`)
	case "/api/zones/delete":
		if _, ok := f.zones[zone]; !ok {
			writeJSON(`{"status":"error","errorMessage":"No such zone was found: ` + zone + `"}`)
			return
		}
		f.deleteCount++
		delete(f.zones, zone)
		writeJSON(`{"status":"ok"}`)
	case "/api/zones/options/set":
		writeJSON(`{"status":"ok"}`)
	default:
		writeJSON(`{"status":"ok"}`)
	}
}

func (f *fakeTechnitiumServer) has(zone string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, ok := f.zones[zone]
	return ok
}

var _ = Describe("Zone Controller against a fake Technitium server", func() {
	const resourceName = "integration-zone"
	const zoneName = "integration.example.com"
	const serverName = "integration-server"

	ctx := context.Background()
	key := types.NamespacedName{Name: resourceName}

	var server *fakeTechnitiumServer
	var reconciler *ZoneReconciler

	BeforeEach(func() {
		server = newFakeTechnitiumServer()
		tech, err := technitium.NewClient(server.URL, technitium.WithToken("test-token"))
		Expect(err).NotTo(HaveOccurred())
		reconciler = &ZoneReconciler{
			Client:            k8sClient,
			Scheme:            k8sClient.Scheme(),
			OperatorNamespace: "default",
			NewServerClient: func(_ context.Context, _ dnsv1alpha1.SecretReference) (ZoneAPI, error) {
				return tech, nil
			},
		}

		zone := &dnsv1alpha1.Zone{
			Name: resourceName,
			Spec: dnsv1alpha1.ZoneSpec{
				ZoneName:  zoneName,
				ServerRef: dnsv1alpha1.SecretReference{Name: serverName},
				Type:      dnsv1alpha1.ZoneTypePrimary,
			},
		}
		Expect(k8sClient.Create(ctx, zone)).To(Succeed())
	})

	AfterEach(func() {
		resource := &dnsv1alpha1.Zone{}
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

	It("creates the zone and marks it Ready", func() {
		_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: key})
		Expect(err).NotTo(HaveOccurred())

		Expect(server.has(zoneName)).To(BeTrue())
		Expect(server.createCount).To(Equal(1))

		zone := &dnsv1alpha1.Zone{}
		Expect(k8sClient.Get(ctx, key, zone)).To(Succeed())
		Expect(meta.IsStatusConditionTrue(zone.Status.Conditions, conditionReady)).To(BeTrue())
	})

	It("is idempotent across repeated reconciles", func() {
		_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: key})
		Expect(err).NotTo(HaveOccurred())
		_, err = reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: key})
		Expect(err).NotTo(HaveOccurred())

		// The second reconcile finds the zone present and does not recreate it.
		Expect(server.createCount).To(Equal(1))
	})

	It("deletes the zone from the server and clears the finalizer", func() {
		_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: key})
		Expect(err).NotTo(HaveOccurred())

		resource := &dnsv1alpha1.Zone{}
		Expect(k8sClient.Get(ctx, key, resource)).To(Succeed())
		Expect(k8sClient.Delete(ctx, resource)).To(Succeed())

		_, err = reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: key})
		Expect(err).NotTo(HaveOccurred())

		Expect(server.deleteCount).To(Equal(1))
		Expect(server.has(zoneName)).To(BeFalse())
		Expect(k8sClient.Get(ctx, key, resource)).To(MatchError(ContainSubstring("not found")))
	})

	It("marks the zone Degraded when the server rejects the create", func() {
		server.failCreate = true

		_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: key})
		Expect(err).To(HaveOccurred())

		zone := &dnsv1alpha1.Zone{}
		Expect(k8sClient.Get(ctx, key, zone)).To(Succeed())
		Expect(meta.IsStatusConditionTrue(zone.Status.Conditions, conditionDegraded)).To(BeTrue())
		Expect(meta.IsStatusConditionFalse(zone.Status.Conditions, conditionReady)).To(BeTrue())
	})
})
