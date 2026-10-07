/*
Copyright (c) 2026 Chris

Licensed under the MIT License. See the LICENSE file in the project root for
the full license text.
*/

package controller

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	dnsv1alpha1 "github.com/charg/technitium-operator/api/v1alpha1"
)

// countingServer is a stand-in Technitium API that answers every call with
// an empty success response and counts how many requests reached it.
func countingServer(hits *atomic.Int32) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok","response":{}}`))
	}))
}

var _ = Describe("serverClientCache", func() {
	const (
		clusterName = "serverref-test"
		namespace   = "default"
	)

	var (
		ctx           context.Context
		primaryHits   atomic.Int32
		balancedHits  atomic.Int32
		primary       *httptest.Server
		balanced      *httptest.Server
		cache         *serverClientCache
		serverRef     dnsv1alpha1.SecretReference
		adminSecretID client.ObjectKey
	)

	setStatus := func(endpoint, primaryEndpoint string) {
		var tc dnsv1alpha1.TechnitiumCluster
		Expect(k8sClient.Get(ctx, client.ObjectKey{Name: clusterName}, &tc)).To(Succeed())
		tc.Status.Endpoint = endpoint
		tc.Status.PrimaryEndpoint = primaryEndpoint
		Expect(k8sClient.Status().Update(ctx, &tc)).To(Succeed())
	}

	BeforeEach(func() {
		ctx = context.Background()
		primaryHits.Store(0)
		balancedHits.Store(0)
		primary = countingServer(&primaryHits)
		balanced = countingServer(&balancedHits)
		cache = &serverClientCache{}
		serverRef = dnsv1alpha1.SecretReference{Name: clusterName}
		adminSecretID = client.ObjectKey{Namespace: namespace, Name: adminSecretName(clusterName)}

		Expect(k8sClient.Create(ctx, &dnsv1alpha1.TechnitiumCluster{
			ObjectMeta: metav1.ObjectMeta{Name: clusterName},
			Spec: dnsv1alpha1.TechnitiumClusterSpec{
				Storage: dnsv1alpha1.TechnitiumClusterStorageSpec{Size: resource.MustParse("1Gi")},
			},
		})).To(Succeed())
		Expect(k8sClient.Create(ctx, &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Namespace: adminSecretID.Namespace, Name: adminSecretID.Name},
			Data:       map[string][]byte{adminSecretTokenKey: []byte("token-1")},
		})).To(Succeed())
	})

	AfterEach(func() {
		primary.Close()
		balanced.Close()
		Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Namespace: adminSecretID.Namespace, Name: adminSecretID.Name},
		}))).To(Succeed())
		Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, &dnsv1alpha1.TechnitiumCluster{
			ObjectMeta: metav1.ObjectMeta{Name: clusterName},
		}))).To(Succeed())
	})

	It("sends API calls to the primary endpoint, not the load-balanced one", func() {
		setStatus(balanced.URL, primary.URL)

		api, err := cache.resolve(ctx, k8sClient, namespace, serverRef)
		Expect(err).NotTo(HaveOccurred())
		_, err = api.GetDNSSettings(ctx)
		Expect(err).NotTo(HaveOccurred())

		Expect(primaryHits.Load()).To(Equal(int32(1)))
		Expect(balancedHits.Load()).To(BeZero())
	})

	It("reports the cluster as not ready while no primary endpoint is set", func() {
		setStatus(balanced.URL, "")

		_, err := cache.resolve(ctx, k8sClient, namespace, serverRef)
		Expect(err).To(MatchError(ContainSubstring("no primary endpoint")))
	})

	It("wraps a missing TechnitiumCluster as NotFound", func() {
		_, err := cache.resolve(ctx, k8sClient, namespace, dnsv1alpha1.SecretReference{Name: "does-not-exist"})
		Expect(apierrors.IsNotFound(err)).To(BeTrue())
	})

	It("reuses the client until the admin secret changes", func() {
		setStatus(balanced.URL, primary.URL)

		first, err := cache.resolve(ctx, k8sClient, namespace, serverRef)
		Expect(err).NotTo(HaveOccurred())
		again, err := cache.resolve(ctx, k8sClient, namespace, serverRef)
		Expect(err).NotTo(HaveOccurred())
		Expect(again).To(BeIdenticalTo(first))

		var secret corev1.Secret
		Expect(k8sClient.Get(ctx, adminSecretID, &secret)).To(Succeed())
		secret.Data[adminSecretTokenKey] = []byte("token-2")
		Expect(k8sClient.Update(ctx, &secret)).To(Succeed())

		rotated, err := cache.resolve(ctx, k8sClient, namespace, serverRef)
		Expect(err).NotTo(HaveOccurred())
		Expect(rotated).NotTo(BeIdenticalTo(first))
	})
})
