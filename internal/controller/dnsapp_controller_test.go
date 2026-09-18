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

// fakeDNSAppAPI records the calls the reconciler makes and returns programmed
// results, standing in for a live Technitium server. installed models the
// server's actual app list, keyed by app name, so install/update/uninstall
// calls mutate it the way the real API would.
type fakeDNSAppAPI struct {
	installed map[string]string // appName -> version
	configs   map[string]string // appName -> config

	// pendingVersion is the version DownloadAndUpdateApp lands the app on. The
	// fake has no independent notion of "what version does this URL contain",
	// so the test sets this to whatever it wants the update to converge to.
	pendingVersion string

	listErr      error
	installErr   error
	updateErr    error
	uninstallErr error
	getConfigErr error
	setConfigErr error

	installCalls   []string // downloadURL per install call
	updateCalls    []string // downloadURL per update call
	uninstallCalls int
	setConfigCalls []string
}

func newFakeDNSAppAPI() *fakeDNSAppAPI {
	return &fakeDNSAppAPI{
		installed: map[string]string{},
		configs:   map[string]string{},
	}
}

func (f *fakeDNSAppAPI) ListApps(_ context.Context) ([]technitium.DNSAppInfo, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	var apps []technitium.DNSAppInfo
	for name, version := range f.installed {
		apps = append(apps, technitium.DNSAppInfo{Name: name, Version: version})
	}
	return apps, nil
}

func (f *fakeDNSAppAPI) DownloadAndInstallApp(_ context.Context, name, downloadURL string) error {
	f.installCalls = append(f.installCalls, downloadURL)
	if f.installErr != nil {
		return f.installErr
	}
	if _, ok := f.installed[name]; ok {
		return technitium.ErrAppAlreadyInstalled
	}
	f.installed[name] = "1.0"
	return nil
}

func (f *fakeDNSAppAPI) DownloadAndUpdateApp(_ context.Context, name, downloadURL string) error {
	f.updateCalls = append(f.updateCalls, downloadURL)
	if f.updateErr != nil {
		return f.updateErr
	}
	// The reconciler only calls update when spec.version is set and differs
	// from what is installed; simulate the server landing on that version.
	f.installed[name] = f.pendingVersion
	return nil
}

func (f *fakeDNSAppAPI) UninstallApp(_ context.Context, name string) error {
	f.uninstallCalls++
	if f.uninstallErr != nil {
		return f.uninstallErr
	}
	if _, ok := f.installed[name]; !ok {
		return technitium.ErrAppNotInstalled
	}
	delete(f.installed, name)
	delete(f.configs, name)
	return nil
}

func (f *fakeDNSAppAPI) GetAppConfig(_ context.Context, name string) (string, error) {
	if f.getConfigErr != nil {
		return "", f.getConfigErr
	}
	return f.configs[name], nil
}

func (f *fakeDNSAppAPI) SetAppConfig(_ context.Context, name, config string) error {
	f.setConfigCalls = append(f.setConfigCalls, config)
	if f.setConfigErr != nil {
		return f.setConfigErr
	}
	f.configs[name] = config
	return nil
}

var _ = Describe("DNSApp Controller", func() {
	Context("When reconciling a resource", func() {
		const resourceName = "test-dnsapp"
		const serverName = "test-server"
		const appName = "Split Horizon"
		const appURL = "https://download.technitium.com/dns/apps/SplitHorizonApp-v1.4.zip"

		ctx := context.Background()
		key := types.NamespacedName{Name: resourceName, Namespace: "default"}

		newReconciler := func(api *fakeDNSAppAPI) *DNSAppReconciler {
			return &DNSAppReconciler{
				Client:            k8sClient,
				Scheme:            k8sClient.Scheme(),
				OperatorNamespace: "default",
				NewServerClient: func(_ context.Context, _ dnsv1alpha1.SecretReference) (DNSAppAPI, error) {
					return api, nil
				},
			}
		}

		createDNSAppCR := func(mutate func(*dnsv1alpha1.DNSApp)) {
			resource := &dnsv1alpha1.DNSApp{
				Name:      resourceName,
				Namespace: "default",
				Spec: dnsv1alpha1.DNSAppSpec{
					ServerRef: dnsv1alpha1.SecretReference{Name: serverName},
					AppName:   appName,
					URL:       appURL,
				},
			}
			if mutate != nil {
				mutate(resource)
			}
			Expect(k8sClient.Create(ctx, resource)).To(Succeed())
		}

		AfterEach(func() {
			resource := &dnsv1alpha1.DNSApp{}
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

		It("returns without error when the DNSApp was deleted", func() {
			api := newFakeDNSAppAPI()
			_, err := newReconciler(api).Reconcile(ctx, reconcile.Request{NamespacedName: key})
			Expect(err).NotTo(HaveOccurred())
			Expect(api.installCalls).To(BeEmpty())
		})

		It("installs the app when absent and records Ready and installedVersion", func() {
			createDNSAppCR(nil)
			api := newFakeDNSAppAPI()

			result, err := newReconciler(api).Reconcile(ctx, reconcile.Request{NamespacedName: key})
			Expect(err).NotTo(HaveOccurred())
			Expect(result.RequeueAfter).To(Equal(driftReconcileInterval))

			Expect(api.installCalls).To(ConsistOf(appURL))
			Expect(api.installed).To(HaveKeyWithValue(appName, "1.0"))

			app := &dnsv1alpha1.DNSApp{}
			Expect(k8sClient.Get(ctx, key, app)).To(Succeed())
			Expect(meta.IsStatusConditionTrue(app.Status.Conditions, conditionReady)).To(BeTrue())
			Expect(app.Status.ObservedGeneration).To(Equal(app.Generation))
			Expect(app.Status.InstalledVersion).To(Equal("1.0"))
			Expect(app.Finalizers).To(ContainElement(dnsAppFinalizer))
		})

		It("makes no changes and does not rewrite status when already installed with a matching version", func() {
			createDNSAppCR(func(app *dnsv1alpha1.DNSApp) {
				app.Spec.Version = "1.0"
			})
			api := newFakeDNSAppAPI()
			api.installed[appName] = "1.0"
			reconciler := newReconciler(api)

			_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: key})
			Expect(err).NotTo(HaveOccurred())
			Expect(api.installCalls).To(BeEmpty())
			Expect(api.updateCalls).To(BeEmpty())

			first := &dnsv1alpha1.DNSApp{}
			Expect(k8sClient.Get(ctx, key, first)).To(Succeed())

			_, err = reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: key})
			Expect(err).NotTo(HaveOccurred())

			second := &dnsv1alpha1.DNSApp{}
			Expect(k8sClient.Get(ctx, key, second)).To(Succeed())
			Expect(second.ResourceVersion).To(Equal(first.ResourceVersion))
		})

		It("updates the app when the installed version differs from spec.version", func() {
			createDNSAppCR(func(app *dnsv1alpha1.DNSApp) {
				app.Spec.Version = "2.0"
			})
			api := newFakeDNSAppAPI()
			api.installed[appName] = "1.0"
			api.pendingVersion = "2.0"
			reconciler := newReconciler(api)

			_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: key})
			Expect(err).NotTo(HaveOccurred())
			Expect(api.installCalls).To(BeEmpty())
			Expect(api.updateCalls).To(ConsistOf(appURL))

			app := &dnsv1alpha1.DNSApp{}
			Expect(k8sClient.Get(ctx, key, app)).To(Succeed())
			Expect(app.Status.InstalledVersion).To(Equal("2.0"))
		})

		It("applies config when it drifts from spec.config", func() {
			createDNSAppCR(func(app *dnsv1alpha1.DNSApp) {
				app.Spec.Config = `{"key":"value"}`
			})
			api := newFakeDNSAppAPI()
			api.installed[appName] = "1.0"
			reconciler := newReconciler(api)

			_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: key})
			Expect(err).NotTo(HaveOccurred())
			Expect(api.setConfigCalls).To(ConsistOf(`{"key":"value"}`))
			Expect(api.configs[appName]).To(Equal(`{"key":"value"}`))

			// A second reconcile with the config already matching should not
			// call SetAppConfig again.
			_, err = reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: key})
			Expect(err).NotTo(HaveOccurred())
			Expect(api.setConfigCalls).To(HaveLen(1))
		})

		It("uninstalls the app from the server on delete with the Delete policy", func() {
			createDNSAppCR(func(app *dnsv1alpha1.DNSApp) {
				app.Spec.DeletionPolicy = dnsv1alpha1.DeletionPolicyDelete
			})
			api := newFakeDNSAppAPI()
			reconciler := newReconciler(api)
			_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: key})
			Expect(err).NotTo(HaveOccurred())
			Expect(api.installed).To(HaveKey(appName))

			resource := &dnsv1alpha1.DNSApp{}
			Expect(k8sClient.Get(ctx, key, resource)).To(Succeed())
			Expect(k8sClient.Delete(ctx, resource)).To(Succeed())

			// The finalizer keeps the object around until the controller runs.
			Expect(k8sClient.Get(ctx, key, resource)).To(Succeed())
			Expect(resource.DeletionTimestamp).NotTo(BeNil())

			_, err = reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: key})
			Expect(err).NotTo(HaveOccurred())

			Expect(api.uninstallCalls).To(Equal(1))
			Expect(api.installed).NotTo(HaveKey(appName))

			err = k8sClient.Get(ctx, key, resource)
			Expect(apierrors.IsNotFound(err)).To(BeTrue())
		})

		It("leaves the app installed on delete with the Orphan policy", func() {
			createDNSAppCR(func(app *dnsv1alpha1.DNSApp) {
				app.Spec.DeletionPolicy = dnsv1alpha1.DeletionPolicyOrphan
			})
			api := newFakeDNSAppAPI()
			reconciler := newReconciler(api)
			_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: key})
			Expect(err).NotTo(HaveOccurred())

			resource := &dnsv1alpha1.DNSApp{}
			Expect(k8sClient.Get(ctx, key, resource)).To(Succeed())
			Expect(k8sClient.Delete(ctx, resource)).To(Succeed())

			_, err = reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: key})
			Expect(err).NotTo(HaveOccurred())

			Expect(api.uninstallCalls).To(Equal(0))
			Expect(api.installed).To(HaveKey(appName))

			err = k8sClient.Get(ctx, key, resource)
			Expect(apierrors.IsNotFound(err)).To(BeTrue())
		})

		It("surfaces and records an error when SetAppConfig fails, without crashing", func() {
			createDNSAppCR(func(app *dnsv1alpha1.DNSApp) {
				app.Spec.Config = "not valid for this app"
			})
			api := newFakeDNSAppAPI()
			api.installed[appName] = "1.0"
			api.setConfigErr = fmt.Errorf("config validation failed: unexpected token")

			_, err := newReconciler(api).Reconcile(ctx, reconcile.Request{NamespacedName: key})
			Expect(err).To(HaveOccurred())

			app := &dnsv1alpha1.DNSApp{}
			Expect(k8sClient.Get(ctx, key, app)).To(Succeed())
			Expect(meta.IsStatusConditionFalse(app.Status.Conditions, conditionReady)).To(BeTrue())
			Expect(meta.IsStatusConditionTrue(app.Status.Conditions, conditionDegraded)).To(BeTrue())
			cond := meta.FindStatusCondition(app.Status.Conditions, conditionDegraded)
			Expect(cond).NotTo(BeNil())
			Expect(cond.Message).To(ContainSubstring("config validation failed"))
		})
	})
})
