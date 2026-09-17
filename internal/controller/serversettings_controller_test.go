/*
Copyright (c) 2026 Chris

Licensed under the MIT License. See the LICENSE file in the project root for
the full license text.
*/

package controller

import (
	"context"
	"fmt"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	dnsv1alpha1 "github.com/charg/technitium-operator/api/v1alpha1"
	"github.com/charg/technitium-operator/internal/technitium"
)

// fakeServerSettingsAPI records the calls the reconciler makes and returns
// programmed results, standing in for a live Technitium server.
type fakeServerSettingsAPI struct {
	settings technitium.DNSSettings
	getErr   error
	setErr   error

	setCalls []technitium.SetDNSSettingsOptions
}

func (f *fakeServerSettingsAPI) GetDNSSettings(_ context.Context) (*technitium.DNSSettings, error) {
	if f.getErr != nil {
		return nil, f.getErr
	}
	settings := f.settings
	return &settings, nil
}

func (f *fakeServerSettingsAPI) SetDNSSettings(_ context.Context, opts technitium.SetDNSSettingsOptions) error {
	f.setCalls = append(f.setCalls, opts)
	if f.setErr != nil {
		return f.setErr
	}
	// Applying the write is what makes a repeat reconcile see an in-sync
	// server, matching how the real Technitium API would behave.
	if opts.Forwarders != nil {
		f.settings.Forwarders = *opts.Forwarders
	}
	if opts.ForwarderProtocol != nil {
		f.settings.ForwarderProtocol = *opts.ForwarderProtocol
	}
	if opts.Recursion != nil {
		f.settings.Recursion = *opts.Recursion
	}
	if opts.RecursionNetworkACL != nil {
		f.settings.RecursionNetworkACL = *opts.RecursionNetworkACL
	}
	if opts.ServeStale != nil {
		f.settings.ServeStale = *opts.ServeStale
	}
	if opts.ServeStaleTTL != nil {
		f.settings.ServeStaleTTL = *opts.ServeStaleTTL
	}
	if opts.CacheMaximumRecordTTL != nil {
		f.settings.CacheMaximumRecordTTL = *opts.CacheMaximumRecordTTL
	}
	if opts.CacheMinimumRecordTTL != nil {
		f.settings.CacheMinimumRecordTTL = *opts.CacheMinimumRecordTTL
	}
	if opts.EnableLogging != nil {
		f.settings.EnableLogging = *opts.EnableLogging
	}
	if opts.LogQueries != nil {
		f.settings.LogQueries = *opts.LogQueries
	}
	if opts.UseLocalTime != nil {
		f.settings.UseLocalTime = *opts.UseLocalTime
	}
	if opts.MaxLogFileDays != nil {
		f.settings.MaxLogFileDays = *opts.MaxLogFileDays
	}
	return nil
}

var _ = Describe("ServerSettings Controller", func() {
	Context("When reconciling a resource", func() {
		const resourceName = "test-serversettings"
		const serverName = "test-server"

		ctx := context.Background()
		key := types.NamespacedName{Name: resourceName, Namespace: "default"}

		newReconciler := func(api *fakeServerSettingsAPI) *ServerSettingsReconciler {
			return &ServerSettingsReconciler{
				Client:            k8sClient,
				Scheme:            k8sClient.Scheme(),
				OperatorNamespace: "default",
				NewServerClient: func(_ context.Context, _ dnsv1alpha1.SecretReference) (ServerSettingsAPI, error) {
					return api, nil
				},
			}
		}

		createServerSettingsCR := func(mutate func(*dnsv1alpha1.ServerSettings)) {
			resource := &dnsv1alpha1.ServerSettings{
				Name:      resourceName,
				Namespace: "default",
				Spec: dnsv1alpha1.ServerSettingsSpec{
					ServerRef: dnsv1alpha1.SecretReference{Name: serverName},
					Forwarders: &dnsv1alpha1.ForwarderSettings{
						Addresses: []string{"1.1.1.1", "8.8.8.8"},
					},
				},
			}
			if mutate != nil {
				mutate(resource)
			}
			Expect(k8sClient.Create(ctx, resource)).To(Succeed())
		}

		AfterEach(func() {
			resource := &dnsv1alpha1.ServerSettings{}
			if err := k8sClient.Get(ctx, key, resource); err != nil {
				return
			}
			// Drop any finalizer with a merge patch so cleanup does not wedge on
			// a test left pending, then best-effort delete.
			if len(resource.Finalizers) > 0 {
				patch := client.MergeFrom(resource.DeepCopy())
				resource.Finalizers = nil
				_ = k8sClient.Patch(ctx, resource, patch)
			}
			_ = k8sClient.Delete(ctx, resource)
		})

		It("returns without error when the ServerSettings was deleted", func() {
			api := &fakeServerSettingsAPI{}
			_, err := newReconciler(api).Reconcile(ctx, reconcile.Request{NamespacedName: key})
			Expect(err).NotTo(HaveOccurred())
			Expect(api.setCalls).To(BeEmpty())
		})

		It("applies settings when the server differs from the spec", func() {
			createServerSettingsCR(nil)
			api := &fakeServerSettingsAPI{}

			result, err := newReconciler(api).Reconcile(ctx, reconcile.Request{NamespacedName: key})
			Expect(err).NotTo(HaveOccurred())
			Expect(result.RequeueAfter).To(Equal(driftReconcileInterval))

			Expect(api.setCalls).To(HaveLen(1))
			Expect(api.setCalls[0].Forwarders).NotTo(BeNil())
			Expect(*api.setCalls[0].Forwarders).To(Equal([]string{"1.1.1.1", "8.8.8.8"}))

			settings := &dnsv1alpha1.ServerSettings{}
			Expect(k8sClient.Get(ctx, key, settings)).To(Succeed())
			Expect(meta.IsStatusConditionTrue(settings.Status.Conditions, conditionReady)).To(BeTrue())
			Expect(settings.Status.ObservedGeneration).To(Equal(settings.Generation))
			Expect(settings.Status.Observed).NotTo(BeNil())
			Expect(settings.Status.Observed.Forwarders).To(Equal([]string{"1.1.1.1", "8.8.8.8"}))
			Expect(settings.Finalizers).To(ContainElement(serverSettingsFinalizer))
		})

		It("makes no changes and does not rewrite status when the server already matches", func() {
			createServerSettingsCR(nil)
			api := &fakeServerSettingsAPI{
				settings: technitium.DNSSettings{Forwarders: []string{"1.1.1.1", "8.8.8.8"}},
			}
			reconciler := newReconciler(api)

			_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: key})
			Expect(err).NotTo(HaveOccurred())
			Expect(api.setCalls).To(BeEmpty())

			first := &dnsv1alpha1.ServerSettings{}
			Expect(k8sClient.Get(ctx, key, first)).To(Succeed())

			_, err = reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: key})
			Expect(err).NotTo(HaveOccurred())
			Expect(api.setCalls).To(BeEmpty())

			second := &dnsv1alpha1.ServerSettings{}
			Expect(k8sClient.Get(ctx, key, second)).To(Succeed())
			Expect(second.ResourceVersion).To(Equal(first.ResourceVersion))
		})

		It("retains server-side settings and clears the finalizer on delete", func() {
			createServerSettingsCR(nil)
			api := &fakeServerSettingsAPI{}
			_, err := newReconciler(api).Reconcile(ctx, reconcile.Request{NamespacedName: key})
			Expect(err).NotTo(HaveOccurred())

			resource := &dnsv1alpha1.ServerSettings{}
			Expect(k8sClient.Get(ctx, key, resource)).To(Succeed())
			Expect(k8sClient.Delete(ctx, resource)).To(Succeed())

			// The finalizer keeps the object around until the controller runs.
			Expect(k8sClient.Get(ctx, key, resource)).To(Succeed())
			Expect(resource.DeletionTimestamp).NotTo(BeNil())

			setCallsBeforeDelete := len(api.setCalls)
			_, err = newReconciler(api).Reconcile(ctx, reconcile.Request{NamespacedName: key})
			Expect(err).NotTo(HaveOccurred())
			Expect(api.setCalls).To(HaveLen(setCallsBeforeDelete))

			err = k8sClient.Get(ctx, key, resource)
			Expect(apierrors.IsNotFound(err)).To(BeTrue())
		})

		It("surfaces and records an error when the server call fails", func() {
			createServerSettingsCR(nil)
			api := &fakeServerSettingsAPI{setErr: fmt.Errorf("server unreachable")}

			_, err := newReconciler(api).Reconcile(ctx, reconcile.Request{NamespacedName: key})
			Expect(err).To(HaveOccurred())

			settings := &dnsv1alpha1.ServerSettings{}
			Expect(k8sClient.Get(ctx, key, settings)).To(Succeed())
			Expect(meta.IsStatusConditionFalse(settings.Status.Conditions, conditionReady)).To(BeTrue())
			Expect(meta.IsStatusConditionTrue(settings.Status.Conditions, conditionDegraded)).To(BeTrue())
			cond := meta.FindStatusCondition(settings.Status.Conditions, conditionDegraded)
			Expect(cond).NotTo(BeNil())
			Expect(cond.Message).To(ContainSubstring("server unreachable"))
		})

		It("surfaces a GetDNSSettings failure without applying settings", func() {
			createServerSettingsCR(nil)
			api := &fakeServerSettingsAPI{getErr: fmt.Errorf("server unreachable")}

			_, err := newReconciler(api).Reconcile(ctx, reconcile.Request{NamespacedName: key})
			Expect(err).To(HaveOccurred())
			Expect(api.setCalls).To(BeEmpty())

			settings := &dnsv1alpha1.ServerSettings{}
			Expect(k8sClient.Get(ctx, key, settings)).To(Succeed())
			Expect(meta.IsStatusConditionTrue(settings.Status.Conditions, conditionDegraded)).To(BeTrue())
		})
	})
})

var _ = Describe("serverSettingsInSync", func() {
	It("ignores fields absent from the desired spec", func() {
		current := &technitium.DNSSettings{Recursion: "Allow"}
		desired := technitium.SetDNSSettingsOptions{}
		Expect(serverSettingsInSync(desired, current)).To(BeTrue())
	})

	It("detects drift on a managed field", func() {
		current := &technitium.DNSSettings{Recursion: "Allow"}
		policy := "Deny"
		desired := technitium.SetDNSSettingsOptions{Recursion: &policy}
		Expect(serverSettingsInSync(desired, current)).To(BeFalse())
	})

	It("treats a nil desired list and an empty server list as in sync", func() {
		// A forwarders group with its addresses omitted yields a non-nil
		// pointer to a nil slice; the server reports no forwarders as []. That
		// pair must not read as drift, or the reconciler would write every tick.
		current := &technitium.DNSSettings{Forwarders: []string{}}
		desired := technitium.SetDNSSettingsOptions{Forwarders: &[]string{}}
		Expect(serverSettingsInSync(desired, current)).To(BeTrue())

		var nilSlice []string
		desired = technitium.SetDNSSettingsOptions{Forwarders: &nilSlice}
		Expect(serverSettingsInSync(desired, current)).To(BeTrue())
	})
})
