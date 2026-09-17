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

// fakeBlocklistAPI records the calls the reconciler makes and returns
// programmed results, standing in for a live Technitium server.
type fakeBlocklistAPI struct {
	settings       technitium.DNSSettings
	getErr         error
	setErr         error
	forceUpdateErr error

	setCalls         []technitium.SetDNSSettingsOptions
	forceUpdateCalls int

	allowedDomains map[string]struct{}
	blockedDomains map[string]struct{}
	addedAllowed   []string
	deletedAllowed []string
	addedBlocked   []string
	deletedBlocked []string
}

func newFakeBlocklistAPI() *fakeBlocklistAPI {
	return &fakeBlocklistAPI{
		allowedDomains: map[string]struct{}{},
		blockedDomains: map[string]struct{}{},
	}
}

func (f *fakeBlocklistAPI) GetDNSSettings(_ context.Context) (*technitium.DNSSettings, error) {
	if f.getErr != nil {
		return nil, f.getErr
	}
	settings := f.settings
	return &settings, nil
}

func (f *fakeBlocklistAPI) SetDNSSettings(_ context.Context, opts technitium.SetDNSSettingsOptions) error {
	f.setCalls = append(f.setCalls, opts)
	if f.setErr != nil {
		return f.setErr
	}
	// Applying the write is what makes a repeat reconcile see an in-sync
	// server, matching how the real Technitium API would behave.
	if opts.EnableBlocking != nil {
		f.settings.EnableBlocking = *opts.EnableBlocking
	}
	if opts.BlockingType != nil {
		f.settings.BlockingType = *opts.BlockingType
	}
	if opts.BlockListURLs != nil {
		f.settings.BlockListURLs = *opts.BlockListURLs
	}
	if opts.BlockListUpdateIntervalHours != nil {
		f.settings.BlockListUpdateIntervalHours = *opts.BlockListUpdateIntervalHours
	}
	return nil
}

func (f *fakeBlocklistAPI) ForceUpdateBlockLists(_ context.Context) error {
	f.forceUpdateCalls++
	return f.forceUpdateErr
}

func (f *fakeBlocklistAPI) AddAllowedZone(_ context.Context, domain string) error {
	f.addedAllowed = append(f.addedAllowed, domain)
	f.allowedDomains[domain] = struct{}{}
	return nil
}

func (f *fakeBlocklistAPI) DeleteAllowedZone(_ context.Context, domain string) error {
	f.deletedAllowed = append(f.deletedAllowed, domain)
	delete(f.allowedDomains, domain)
	return nil
}

func (f *fakeBlocklistAPI) AddBlockedZone(_ context.Context, domain string) error {
	f.addedBlocked = append(f.addedBlocked, domain)
	f.blockedDomains[domain] = struct{}{}
	return nil
}

func (f *fakeBlocklistAPI) DeleteBlockedZone(_ context.Context, domain string) error {
	f.deletedBlocked = append(f.deletedBlocked, domain)
	delete(f.blockedDomains, domain)
	return nil
}

var _ = Describe("Blocklist Controller", func() {
	Context("When reconciling a resource", func() {
		const resourceName = "test-blocklist"
		const serverName = "test-server"

		ctx := context.Background()
		key := types.NamespacedName{Name: resourceName, Namespace: "default"}

		newReconciler := func(api *fakeBlocklistAPI) *BlocklistReconciler {
			return &BlocklistReconciler{
				Client:            k8sClient,
				Scheme:            k8sClient.Scheme(),
				OperatorNamespace: "default",
				NewServerClient: func(_ context.Context, _ dnsv1alpha1.SecretReference) (BlocklistAPI, error) {
					return api, nil
				},
			}
		}

		createBlocklistCR := func(mutate func(*dnsv1alpha1.Blocklist)) {
			enabled := true
			resource := &dnsv1alpha1.Blocklist{
				Name:      resourceName,
				Namespace: "default",
				Spec: dnsv1alpha1.BlocklistSpec{
					ServerRef:      dnsv1alpha1.SecretReference{Name: serverName},
					Enabled:        &enabled,
					BlockListURLs:  []string{"https://example.com/blocklist.txt"},
					AllowedDomains: []string{"allowed.example.com", "safe.example.com"},
					BlockedDomains: []string{"blocked.example.com"},
				},
			}
			if mutate != nil {
				mutate(resource)
			}
			Expect(k8sClient.Create(ctx, resource)).To(Succeed())
		}

		AfterEach(func() {
			resource := &dnsv1alpha1.Blocklist{}
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

		It("returns without error when the Blocklist was deleted", func() {
			api := newFakeBlocklistAPI()
			_, err := newReconciler(api).Reconcile(ctx, reconcile.Request{NamespacedName: key})
			Expect(err).NotTo(HaveOccurred())
			Expect(api.setCalls).To(BeEmpty())
		})

		It("applies blocking settings and adds allowed and blocked domains when the server differs", func() {
			createBlocklistCR(nil)
			api := newFakeBlocklistAPI()

			result, err := newReconciler(api).Reconcile(ctx, reconcile.Request{NamespacedName: key})
			Expect(err).NotTo(HaveOccurred())
			Expect(result.RequeueAfter).To(Equal(driftReconcileInterval))

			Expect(api.setCalls).To(HaveLen(1))
			Expect(api.forceUpdateCalls).To(Equal(1))
			Expect(api.addedAllowed).To(ConsistOf("allowed.example.com", "safe.example.com"))
			Expect(api.addedBlocked).To(ConsistOf("blocked.example.com"))

			bl := &dnsv1alpha1.Blocklist{}
			Expect(k8sClient.Get(ctx, key, bl)).To(Succeed())
			Expect(meta.IsStatusConditionTrue(bl.Status.Conditions, conditionReady)).To(BeTrue())
			Expect(bl.Status.ObservedGeneration).To(Equal(bl.Generation))
			Expect(bl.Status.AppliedAllowedDomains).To(ConsistOf("allowed.example.com", "safe.example.com"))
			Expect(bl.Status.AppliedBlockedDomains).To(ConsistOf("blocked.example.com"))
			Expect(bl.Status.AppliedBlockListURLs).To(Equal([]string{"https://example.com/blocklist.txt"}))
			Expect(bl.Finalizers).To(ContainElement(blocklistFinalizer))
		})

		It("makes no changes and does not rewrite status when the server already matches", func() {
			createBlocklistCR(nil)

			// Seed status.Applied* to match spec, as a prior successful
			// reconcile would have left it.
			bl := &dnsv1alpha1.Blocklist{}
			Expect(k8sClient.Get(ctx, key, bl)).To(Succeed())
			bl.Status.AppliedAllowedDomains = []string{"allowed.example.com", "safe.example.com"}
			bl.Status.AppliedBlockedDomains = []string{"blocked.example.com"}
			bl.Status.AppliedBlockListURLs = []string{"https://example.com/blocklist.txt"}
			Expect(k8sClient.Status().Update(ctx, bl)).To(Succeed())

			api := newFakeBlocklistAPI()
			api.settings = technitium.DNSSettings{
				EnableBlocking:               true,
				BlockListURLs:                []string{"https://example.com/blocklist.txt"},
				BlockListUpdateIntervalHours: 24, // matches the CRD's default updateIntervalHours
			}
			api.allowedDomains["allowed.example.com"] = struct{}{}
			api.allowedDomains["safe.example.com"] = struct{}{}
			api.blockedDomains["blocked.example.com"] = struct{}{}
			reconciler := newReconciler(api)

			_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: key})
			Expect(err).NotTo(HaveOccurred())
			Expect(api.setCalls).To(BeEmpty())
			Expect(api.forceUpdateCalls).To(Equal(0))
			Expect(api.addedAllowed).To(BeEmpty())
			Expect(api.addedBlocked).To(BeEmpty())
			Expect(api.deletedAllowed).To(BeEmpty())
			Expect(api.deletedBlocked).To(BeEmpty())

			first := &dnsv1alpha1.Blocklist{}
			Expect(k8sClient.Get(ctx, key, first)).To(Succeed())

			_, err = reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: key})
			Expect(err).NotTo(HaveOccurred())
			Expect(api.setCalls).To(BeEmpty())

			second := &dnsv1alpha1.Blocklist{}
			Expect(k8sClient.Get(ctx, key, second)).To(Succeed())
			Expect(second.ResourceVersion).To(Equal(first.ResourceVersion))
		})

		It("converges domains on drift", func() {
			createBlocklistCR(func(bl *dnsv1alpha1.Blocklist) {
				bl.Spec.AllowedDomains = []string{"a.example.com", "b.example.com"}
				bl.Spec.BlockedDomains = nil
			})
			api := newFakeBlocklistAPI()
			reconciler := newReconciler(api)

			_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: key})
			Expect(err).NotTo(HaveOccurred())
			Expect(api.addedAllowed).To(ConsistOf("a.example.com", "b.example.com"))

			bl := &dnsv1alpha1.Blocklist{}
			Expect(k8sClient.Get(ctx, key, bl)).To(Succeed())
			bl.Spec.AllowedDomains = []string{"b.example.com", "c.example.com"}
			Expect(k8sClient.Update(ctx, bl)).To(Succeed())

			_, err = reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: key})
			Expect(err).NotTo(HaveOccurred())
			Expect(api.addedAllowed).To(ConsistOf("a.example.com", "b.example.com", "c.example.com"))
			Expect(api.deletedAllowed).To(ConsistOf("a.example.com"))

			Expect(k8sClient.Get(ctx, key, bl)).To(Succeed())
			Expect(bl.Status.AppliedAllowedDomains).To(ConsistOf("b.example.com", "c.example.com"))
		})

		It("clears managed configuration from the server on delete with the Delete policy", func() {
			createBlocklistCR(func(bl *dnsv1alpha1.Blocklist) {
				bl.Spec.DeletionPolicy = dnsv1alpha1.DeletionPolicyDelete
			})
			api := newFakeBlocklistAPI()
			reconciler := newReconciler(api)
			_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: key})
			Expect(err).NotTo(HaveOccurred())

			resource := &dnsv1alpha1.Blocklist{}
			Expect(k8sClient.Get(ctx, key, resource)).To(Succeed())
			Expect(k8sClient.Delete(ctx, resource)).To(Succeed())

			// The finalizer keeps the object around until the controller runs.
			Expect(k8sClient.Get(ctx, key, resource)).To(Succeed())
			Expect(resource.DeletionTimestamp).NotTo(BeNil())

			forceUpdatesBeforeDelete := api.forceUpdateCalls
			_, err = reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: key})
			Expect(err).NotTo(HaveOccurred())

			Expect(api.forceUpdateCalls).To(Equal(forceUpdatesBeforeDelete + 1))
			lastSet := api.setCalls[len(api.setCalls)-1]
			Expect(lastSet.BlockListURLs).NotTo(BeNil())
			Expect(*lastSet.BlockListURLs).To(BeEmpty())
			Expect(api.deletedAllowed).To(ConsistOf("allowed.example.com", "safe.example.com"))
			Expect(api.deletedBlocked).To(ConsistOf("blocked.example.com"))

			err = k8sClient.Get(ctx, key, resource)
			Expect(apierrors.IsNotFound(err)).To(BeTrue())
		})

		It("leaves the server untouched on delete with the Orphan policy", func() {
			createBlocklistCR(func(bl *dnsv1alpha1.Blocklist) {
				bl.Spec.DeletionPolicy = dnsv1alpha1.DeletionPolicyOrphan
			})
			api := newFakeBlocklistAPI()
			reconciler := newReconciler(api)
			_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: key})
			Expect(err).NotTo(HaveOccurred())

			resource := &dnsv1alpha1.Blocklist{}
			Expect(k8sClient.Get(ctx, key, resource)).To(Succeed())
			Expect(k8sClient.Delete(ctx, resource)).To(Succeed())

			setCallsBeforeDelete := len(api.setCalls)
			forceUpdatesBeforeDelete := api.forceUpdateCalls
			_, err = reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: key})
			Expect(err).NotTo(HaveOccurred())

			Expect(api.setCalls).To(HaveLen(setCallsBeforeDelete))
			Expect(api.forceUpdateCalls).To(Equal(forceUpdatesBeforeDelete))
			Expect(api.deletedAllowed).To(BeEmpty())
			Expect(api.deletedBlocked).To(BeEmpty())

			err = k8sClient.Get(ctx, key, resource)
			Expect(apierrors.IsNotFound(err)).To(BeTrue())
		})

		It("surfaces and records an error when SetDNSSettings fails", func() {
			createBlocklistCR(nil)
			api := newFakeBlocklistAPI()
			api.setErr = fmt.Errorf("server unreachable")

			_, err := newReconciler(api).Reconcile(ctx, reconcile.Request{NamespacedName: key})
			Expect(err).To(HaveOccurred())

			bl := &dnsv1alpha1.Blocklist{}
			Expect(k8sClient.Get(ctx, key, bl)).To(Succeed())
			Expect(meta.IsStatusConditionFalse(bl.Status.Conditions, conditionReady)).To(BeTrue())
			Expect(meta.IsStatusConditionTrue(bl.Status.Conditions, conditionDegraded)).To(BeTrue())
			cond := meta.FindStatusCondition(bl.Status.Conditions, conditionDegraded)
			Expect(cond).NotTo(BeNil())
			Expect(cond.Message).To(ContainSubstring("server unreachable"))
		})

		It("retries the block list refresh when a prior refresh failed after the settings write landed", func() {
			createBlocklistCR(nil)
			api := newFakeBlocklistAPI()
			// The settings write succeeds and mutates the fake so the server now
			// reports the new URLs, but the refresh that should follow fails.
			api.forceUpdateErr = fmt.Errorf("refresh timed out")
			reconciler := newReconciler(api)

			_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: key})
			Expect(err).To(HaveOccurred())
			Expect(api.setCalls).To(HaveLen(1))
			Expect(api.forceUpdateCalls).To(Equal(1))

			// status.AppliedBlockListURLs must not have advanced, since the
			// refresh never completed.
			bl := &dnsv1alpha1.Blocklist{}
			Expect(k8sClient.Get(ctx, key, bl)).To(Succeed())
			Expect(bl.Status.AppliedBlockListURLs).To(BeEmpty())

			// Next reconcile: the URLs already match on the server, so no second
			// settings write, but the owed refresh runs again and now succeeds.
			api.forceUpdateErr = nil
			_, err = reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: key})
			Expect(err).NotTo(HaveOccurred())
			Expect(api.setCalls).To(HaveLen(1))
			Expect(api.forceUpdateCalls).To(Equal(2))

			Expect(k8sClient.Get(ctx, key, bl)).To(Succeed())
			Expect(bl.Status.AppliedBlockListURLs).To(Equal([]string{"https://example.com/blocklist.txt"}))
		})
	})
})

var _ = Describe("blocklistSettingsInSync", func() {
	It("ignores fields absent from the desired options", func() {
		current := &technitium.DNSSettings{EnableBlocking: true}
		desired := technitium.SetDNSSettingsOptions{}
		Expect(blocklistSettingsInSync(desired, current)).To(BeTrue())
	})

	It("detects drift on a managed field", func() {
		current := &technitium.DNSSettings{EnableBlocking: true}
		enabled := false
		desired := technitium.SetDNSSettingsOptions{EnableBlocking: &enabled}
		Expect(blocklistSettingsInSync(desired, current)).To(BeFalse())
	})

	It("treats a nil desired list and an empty server list as in sync", func() {
		current := &technitium.DNSSettings{BlockListURLs: []string{}}
		desired := technitium.SetDNSSettingsOptions{BlockListURLs: &[]string{}}
		Expect(blocklistSettingsInSync(desired, current)).To(BeTrue())
	})
})

var _ = Describe("reconcileDomainSet", func() {
	It("adds new domains and deletes removed ones, leaving untouched domains alone", func() {
		ctx := context.Background()
		var added, deleted []string
		add := func(_ context.Context, domain string) error {
			added = append(added, domain)
			return nil
		}
		del := func(_ context.Context, domain string) error {
			deleted = append(deleted, domain)
			return nil
		}

		err := reconcileDomainSet(ctx, []string{"a", "b"}, []string{"b", "c"}, add, del)
		Expect(err).NotTo(HaveOccurred())
		Expect(added).To(Equal([]string{"c"}))
		Expect(deleted).To(Equal([]string{"a"}))
	})

	It("returns the first error and stops without persisting partial progress", func() {
		ctx := context.Background()
		add := func(_ context.Context, _ string) error {
			return fmt.Errorf("add failed")
		}
		del := func(_ context.Context, _ string) error {
			return nil
		}

		err := reconcileDomainSet(ctx, nil, []string{"a"}, add, del)
		Expect(err).To(HaveOccurred())
	})
})
