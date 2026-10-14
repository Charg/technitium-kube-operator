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

// fakeRecordAPI records the calls the reconciler makes and returns programmed
// results, standing in for a live Technitium server.
type fakeRecordAPI struct {
	getRecords func(domain string) ([]technitium.Record, error)
	addErr     error
	updateErr  error
	deleteErr  error

	addCalls    []technitium.AddRecordOptions
	updateCalls []technitium.UpdateRecordOptions
	deleteCalls []technitium.DeleteRecordOptions
}

func (f *fakeRecordAPI) AddRecord(_ context.Context, opts technitium.AddRecordOptions) error {
	f.addCalls = append(f.addCalls, opts)
	return f.addErr
}

func (f *fakeRecordAPI) GetRecords(_ context.Context, _, domain string) ([]technitium.Record, error) {
	return f.getRecords(domain)
}

func (f *fakeRecordAPI) UpdateRecord(_ context.Context, opts technitium.UpdateRecordOptions) error {
	f.updateCalls = append(f.updateCalls, opts)
	return f.updateErr
}

func (f *fakeRecordAPI) DeleteRecord(_ context.Context, opts technitium.DeleteRecordOptions) error {
	f.deleteCalls = append(f.deleteCalls, opts)
	return f.deleteErr
}

// noRecords is a GetRecords stub for a domain the server has nothing for.
func noRecords(string) ([]technitium.Record, error) { return nil, nil }

var _ = Describe("Record Controller", func() {
	Context("When reconciling a resource", func() {
		const resourceName = "test-record"
		const recordDomain = "www.example.com"
		const recordZone = "example.com"
		const serverName = "test-server"

		ctx := context.Background()
		key := types.NamespacedName{Name: resourceName, Namespace: "default"}

		newReconciler := func(api *fakeRecordAPI) *RecordReconciler {
			return &RecordReconciler{
				Client:            k8sClient,
				Scheme:            k8sClient.Scheme(),
				OperatorNamespace: "default",
				NewServerClient: func(_ context.Context, _ dnsv1alpha1.SecretReference) (RecordAPI, error) {
					return api, nil
				},
			}
		}

		createRecordCR := func(mutate func(*dnsv1alpha1.Record)) {
			resource := &dnsv1alpha1.Record{
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
			if mutate != nil {
				mutate(resource)
			}
			Expect(k8sClient.Create(ctx, resource)).To(Succeed())
		}

		AfterEach(func() {
			resource := &dnsv1alpha1.Record{}
			if err := k8sClient.Get(ctx, key, resource); err != nil {
				return
			}
			// Drop any finalizer with a merge patch so cleanup does not wedge on
			// server-side deletion a test left pending, then best-effort delete.
			if len(resource.Finalizers) > 0 {
				patch := client.MergeFrom(resource.DeepCopy())
				resource.Finalizers = nil
				_ = k8sClient.Patch(ctx, resource, patch)
			}
			_ = k8sClient.Delete(ctx, resource)
		})

		It("returns without error when the Record was deleted", func() {
			api := &fakeRecordAPI{getRecords: noRecords}
			_, err := newReconciler(api).Reconcile(ctx, reconcile.Request{NamespacedName: key})
			Expect(err).NotTo(HaveOccurred())
			Expect(api.addCalls).To(BeEmpty())
		})

		It("creates the record on the server when it is absent", func() {
			createRecordCR(nil)
			api := &fakeRecordAPI{getRecords: noRecords}

			result, err := newReconciler(api).Reconcile(ctx, reconcile.Request{NamespacedName: key})
			Expect(err).NotTo(HaveOccurred())
			Expect(result.RequeueAfter).To(Equal(driftReconcileInterval))

			Expect(api.addCalls).To(HaveLen(1))
			Expect(api.addCalls[0].Domain).To(Equal(recordDomain))
			Expect(api.addCalls[0].Type).To(Equal(string(dnsv1alpha1.RecordTypeA)))
			Expect(api.addCalls[0].IPAddress).To(Equal("1.1.1.1"))

			record := &dnsv1alpha1.Record{}
			Expect(k8sClient.Get(ctx, key, record)).To(Succeed())
			Expect(record.Status.RecordCreated).To(BeTrue())
			Expect(record.Status.ObservedGeneration).To(Equal(record.Generation))
			Expect(meta.IsStatusConditionTrue(record.Status.Conditions, conditionReady)).To(BeTrue())
			Expect(record.Finalizers).To(ContainElement(recordFinalizer))
		})

		It("passes the MX preference through on create", func() {
			pref := int32(10)
			createRecordCR(func(rec *dnsv1alpha1.Record) {
				rec.Spec.Type = dnsv1alpha1.RecordTypeMX
				rec.Spec.Data = "mail.example.com"
				rec.Spec.Priority = &pref
			})
			api := &fakeRecordAPI{getRecords: noRecords}

			_, err := newReconciler(api).Reconcile(ctx, reconcile.Request{NamespacedName: key})
			Expect(err).NotTo(HaveOccurred())

			Expect(api.addCalls).To(HaveLen(1))
			Expect(api.addCalls[0].Exchange).To(Equal("mail.example.com"))
			Expect(api.addCalls[0].Preference).NotTo(BeNil())
			Expect(*api.addCalls[0].Preference).To(Equal(pref))
		})

		It("deletes the server record and clears the finalizer on delete", func() {
			createRecordCR(nil)
			api := &fakeRecordAPI{getRecords: noRecords}
			_, err := newReconciler(api).Reconcile(ctx, reconcile.Request{NamespacedName: key})
			Expect(err).NotTo(HaveOccurred())

			resource := &dnsv1alpha1.Record{}
			Expect(k8sClient.Get(ctx, key, resource)).To(Succeed())
			Expect(k8sClient.Delete(ctx, resource)).To(Succeed())

			// The finalizer keeps the object around until the controller runs.
			Expect(k8sClient.Get(ctx, key, resource)).To(Succeed())
			Expect(resource.DeletionTimestamp).NotTo(BeNil())

			_, err = newReconciler(api).Reconcile(ctx, reconcile.Request{NamespacedName: key})
			Expect(err).NotTo(HaveOccurred())
			Expect(api.deleteCalls).To(HaveLen(1))
			Expect(api.deleteCalls[0].Domain).To(Equal(recordDomain))

			err = k8sClient.Get(ctx, key, resource)
			Expect(apierrors.IsNotFound(err)).To(BeTrue())
		})

		It("completes deletion when the server record is already gone", func() {
			createRecordCR(nil)
			api := &fakeRecordAPI{
				getRecords: noRecords,
				deleteErr:  fmt.Errorf("%w: no such record", technitium.ErrRecordNotFound),
			}
			_, err := newReconciler(api).Reconcile(ctx, reconcile.Request{NamespacedName: key})
			Expect(err).NotTo(HaveOccurred())

			resource := &dnsv1alpha1.Record{}
			Expect(k8sClient.Get(ctx, key, resource)).To(Succeed())
			Expect(k8sClient.Delete(ctx, resource)).To(Succeed())

			_, err = newReconciler(api).Reconcile(ctx, reconcile.Request{NamespacedName: key})
			Expect(err).NotTo(HaveOccurred())

			err = k8sClient.Get(ctx, key, resource)
			Expect(apierrors.IsNotFound(err)).To(BeTrue())
		})

		It("keeps the finalizer when the server delete fails", func() {
			createRecordCR(nil)
			api := &fakeRecordAPI{getRecords: noRecords}
			_, err := newReconciler(api).Reconcile(ctx, reconcile.Request{NamespacedName: key})
			Expect(err).NotTo(HaveOccurred())

			resource := &dnsv1alpha1.Record{}
			Expect(k8sClient.Get(ctx, key, resource)).To(Succeed())
			Expect(k8sClient.Delete(ctx, resource)).To(Succeed())

			failing := &fakeRecordAPI{getRecords: noRecords, deleteErr: fmt.Errorf("server unreachable")}
			_, err = newReconciler(failing).Reconcile(ctx, reconcile.Request{NamespacedName: key})
			Expect(err).To(HaveOccurred())

			Expect(k8sClient.Get(ctx, key, resource)).To(Succeed())
			Expect(resource.Finalizers).To(ContainElement(recordFinalizer))
		})

		It("leaves the server record intact when the deletion policy is Orphan", func() {
			createRecordCR(func(rec *dnsv1alpha1.Record) { rec.Spec.DeletionPolicy = dnsv1alpha1.DeletionPolicyOrphan })
			api := &fakeRecordAPI{getRecords: noRecords}
			_, err := newReconciler(api).Reconcile(ctx, reconcile.Request{NamespacedName: key})
			Expect(err).NotTo(HaveOccurred())

			resource := &dnsv1alpha1.Record{}
			Expect(k8sClient.Get(ctx, key, resource)).To(Succeed())
			Expect(k8sClient.Delete(ctx, resource)).To(Succeed())

			_, err = newReconciler(api).Reconcile(ctx, reconcile.Request{NamespacedName: key})
			Expect(err).NotTo(HaveOccurred())
			Expect(api.deleteCalls).To(BeEmpty())

			err = k8sClient.Get(ctx, key, resource)
			Expect(apierrors.IsNotFound(err)).To(BeTrue())
		})

		It("treats an already-existing record on create as success", func() {
			createRecordCR(nil)
			api := &fakeRecordAPI{
				getRecords: noRecords,
				addErr:     fmt.Errorf("%w: www.example.com", technitium.ErrRecordAlreadyExists),
			}

			_, err := newReconciler(api).Reconcile(ctx, reconcile.Request{NamespacedName: key})
			Expect(err).NotTo(HaveOccurred())

			record := &dnsv1alpha1.Record{}
			Expect(k8sClient.Get(ctx, key, record)).To(Succeed())
			Expect(record.Status.RecordCreated).To(BeTrue())
		})

		It("makes no changes when an existing record already matches", func() {
			createRecordCR(nil)
			api := &fakeRecordAPI{
				getRecords: func(domain string) ([]technitium.Record, error) {
					return []technitium.Record{{
						Name: domain, Type: "A", TTL: 3600,
						RData: technitium.RecordData{IPAddress: "1.1.1.1"},
					}}, nil
				},
			}

			_, err := newReconciler(api).Reconcile(ctx, reconcile.Request{NamespacedName: key})
			Expect(err).NotTo(HaveOccurred())
			Expect(api.addCalls).To(BeEmpty())
			Expect(api.updateCalls).To(BeEmpty())
			Expect(api.deleteCalls).To(BeEmpty())
		})

		It("does not rewrite status on a repeat reconcile of a matching record", func() {
			createRecordCR(nil)
			api := &fakeRecordAPI{
				getRecords: func(domain string) ([]technitium.Record, error) {
					return []technitium.Record{{
						Name: domain, Type: "A", TTL: 3600,
						RData: technitium.RecordData{IPAddress: "1.1.1.1"},
					}}, nil
				},
			}
			reconciler := newReconciler(api)

			_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: key})
			Expect(err).NotTo(HaveOccurred())

			first := &dnsv1alpha1.Record{}
			Expect(k8sClient.Get(ctx, key, first)).To(Succeed())

			_, err = reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: key})
			Expect(err).NotTo(HaveOccurred())

			second := &dnsv1alpha1.Record{}
			Expect(k8sClient.Get(ctx, key, second)).To(Succeed())
			Expect(second.ResourceVersion).To(Equal(first.ResourceVersion))
		})

		It("corrects TTL drift on an existing record in place", func() {
			createRecordCR(nil)
			api := &fakeRecordAPI{
				getRecords: func(domain string) ([]technitium.Record, error) {
					return []technitium.Record{{
						Name: domain, Type: "A", TTL: 60,
						RData: technitium.RecordData{IPAddress: "1.1.1.1"},
					}}, nil
				},
			}

			_, err := newReconciler(api).Reconcile(ctx, reconcile.Request{NamespacedName: key})
			Expect(err).NotTo(HaveOccurred())
			Expect(api.addCalls).To(BeEmpty())
			Expect(api.deleteCalls).To(BeEmpty())
			Expect(api.updateCalls).To(HaveLen(1))
			Expect(api.updateCalls[0].TTL).To(Equal(int32(3600)))
			Expect(api.updateCalls[0].IPAddress).To(Equal("1.1.1.1"))
		})

		// serverHas returns a GetRecords stub serving recordType records, each
		// with the CR's default TTL so only the rdata differs between them.
		serverHas := func(recordType string, data ...technitium.RecordData) func(string) ([]technitium.Record, error) {
			return func(domain string) ([]technitium.Record, error) {
				records := make([]technitium.Record, 0, len(data))
				for _, d := range data {
					records = append(records, technitium.Record{Name: domain, Type: recordType, TTL: 3600, RData: d})
				}
				return records, nil
			}
		}

		// setApplied records data (and priority) as already written to the
		// server, as a previous successful reconcile would have.
		setApplied := func(data string, priority *int32) {
			record := &dnsv1alpha1.Record{}
			Expect(k8sClient.Get(ctx, key, record)).To(Succeed())
			record.Status.AppliedData = data
			record.Status.AppliedPriority = priority
			Expect(k8sClient.Status().Update(ctx, record)).To(Succeed())
		}

		It("adds its record alongside another record of the same name and type", func() {
			createRecordCR(nil)
			api := &fakeRecordAPI{getRecords: serverHas("A", technitium.RecordData{IPAddress: "2.2.2.2"})}

			_, err := newReconciler(api).Reconcile(ctx, reconcile.Request{NamespacedName: key})
			Expect(err).NotTo(HaveOccurred())
			Expect(api.addCalls).To(HaveLen(1))
			Expect(api.addCalls[0].IPAddress).To(Equal("1.1.1.1"))
			Expect(api.deleteCalls).To(BeEmpty())
			Expect(api.updateCalls).To(BeEmpty())
		})

		It("finds its own record among several of the same name and type", func() {
			createRecordCR(nil)
			api := &fakeRecordAPI{getRecords: serverHas("A",
				technitium.RecordData{IPAddress: "2.2.2.2"},
				technitium.RecordData{IPAddress: "1.1.1.1"},
			)}

			_, err := newReconciler(api).Reconcile(ctx, reconcile.Request{NamespacedName: key})
			Expect(err).NotTo(HaveOccurred())
			Expect(api.addCalls).To(BeEmpty())
			Expect(api.deleteCalls).To(BeEmpty())
			Expect(api.updateCalls).To(BeEmpty())
		})

		It("corrects TTL drift on its own record, not the first of the type", func() {
			createRecordCR(nil)
			api := &fakeRecordAPI{
				getRecords: func(domain string) ([]technitium.Record, error) {
					return []technitium.Record{
						{Name: domain, Type: "A", TTL: 3600, RData: technitium.RecordData{IPAddress: "2.2.2.2"}},
						{Name: domain, Type: "A", TTL: 60, RData: technitium.RecordData{IPAddress: "1.1.1.1"}},
					}, nil
				},
			}

			_, err := newReconciler(api).Reconcile(ctx, reconcile.Request{NamespacedName: key})
			Expect(err).NotTo(HaveOccurred())
			Expect(api.updateCalls).To(HaveLen(1))
			Expect(api.updateCalls[0].IPAddress).To(Equal("1.1.1.1"))
			Expect(api.addCalls).To(BeEmpty())
			Expect(api.deleteCalls).To(BeEmpty())
		})

		It("records the applied data in status", func() {
			createRecordCR(nil)
			api := &fakeRecordAPI{getRecords: noRecords}

			_, err := newReconciler(api).Reconcile(ctx, reconcile.Request{NamespacedName: key})
			Expect(err).NotTo(HaveOccurred())

			record := &dnsv1alpha1.Record{}
			Expect(k8sClient.Get(ctx, key, record)).To(Succeed())
			Expect(record.Status.AppliedData).To(Equal("1.1.1.1"))
			Expect(record.Status.AppliedPriority).To(BeNil())
		})

		It("adds the new data and then deletes the previously applied data on a spec change", func() {
			createRecordCR(nil)
			setApplied("9.9.9.9", nil)
			api := &fakeRecordAPI{getRecords: serverHas("A",
				technitium.RecordData{IPAddress: "2.2.2.2"},
				technitium.RecordData{IPAddress: "9.9.9.9"},
			)}

			_, err := newReconciler(api).Reconcile(ctx, reconcile.Request{NamespacedName: key})
			Expect(err).NotTo(HaveOccurred())
			Expect(api.addCalls).To(HaveLen(1))
			Expect(api.addCalls[0].IPAddress).To(Equal("1.1.1.1"))
			Expect(api.deleteCalls).To(HaveLen(1))
			Expect(api.deleteCalls[0].IPAddress).To(Equal("9.9.9.9"))

			record := &dnsv1alpha1.Record{}
			Expect(k8sClient.Get(ctx, key, record)).To(Succeed())
			Expect(record.Status.AppliedData).To(Equal("1.1.1.1"))
		})

		It("deletes the previously applied MX record when only the priority changes", func() {
			oldPref, newPref := int32(10), int32(20)
			createRecordCR(func(rec *dnsv1alpha1.Record) {
				rec.Spec.Type = dnsv1alpha1.RecordTypeMX
				rec.Spec.Data = "mail.example.com"
				rec.Spec.Priority = &newPref
			})
			setApplied("mail.example.com", &oldPref)
			api := &fakeRecordAPI{getRecords: serverHas("MX",
				technitium.RecordData{Exchange: "mail.example.com", Preference: oldPref},
			)}

			_, err := newReconciler(api).Reconcile(ctx, reconcile.Request{NamespacedName: key})
			Expect(err).NotTo(HaveOccurred())
			Expect(api.addCalls).To(HaveLen(1))
			Expect(*api.addCalls[0].Preference).To(Equal(newPref))
			Expect(api.deleteCalls).To(HaveLen(1))
			Expect(*api.deleteCalls[0].Preference).To(Equal(oldPref))

			record := &dnsv1alpha1.Record{}
			Expect(k8sClient.Get(ctx, key, record)).To(Succeed())
			Expect(*record.Status.AppliedPriority).To(Equal(newPref))
		})

		It("corrects CNAME drift by deleting and recreating the record", func() {
			createRecordCR(func(rec *dnsv1alpha1.Record) {
				rec.Spec.Type = dnsv1alpha1.RecordTypeCNAME
				rec.Spec.Data = "target.example.com"
			})
			api := &fakeRecordAPI{getRecords: serverHas("CNAME", technitium.RecordData{CName: "stale.example.com"})}

			_, err := newReconciler(api).Reconcile(ctx, reconcile.Request{NamespacedName: key})
			Expect(err).NotTo(HaveOccurred())
			Expect(api.deleteCalls).To(HaveLen(1))
			Expect(api.deleteCalls[0].CName).To(Equal("stale.example.com"))
			Expect(api.addCalls).To(HaveLen(1))
			Expect(api.addCalls[0].CName).To(Equal("target.example.com"))
			Expect(api.updateCalls).To(BeEmpty())
		})

		It("deletes the applied data, not the unreconciled spec data, on delete", func() {
			createRecordCR(nil)
			api := &fakeRecordAPI{getRecords: noRecords}
			_, err := newReconciler(api).Reconcile(ctx, reconcile.Request{NamespacedName: key})
			Expect(err).NotTo(HaveOccurred())

			// Change spec.data and delete before the change is reconciled: the
			// server still holds the originally applied 1.1.1.1.
			resource := &dnsv1alpha1.Record{}
			Expect(k8sClient.Get(ctx, key, resource)).To(Succeed())
			resource.Spec.Data = "3.3.3.3"
			Expect(k8sClient.Update(ctx, resource)).To(Succeed())
			Expect(k8sClient.Delete(ctx, resource)).To(Succeed())

			_, err = newReconciler(api).Reconcile(ctx, reconcile.Request{NamespacedName: key})
			Expect(err).NotTo(HaveOccurred())
			Expect(api.deleteCalls).To(HaveLen(1))
			Expect(api.deleteCalls[0].IPAddress).To(Equal("1.1.1.1"))
		})

		It("surfaces and records an error when the server call fails", func() {
			createRecordCR(nil)
			api := &fakeRecordAPI{
				getRecords: noRecords,
				addErr:     fmt.Errorf("server unreachable"),
			}

			_, err := newReconciler(api).Reconcile(ctx, reconcile.Request{NamespacedName: key})
			Expect(err).To(HaveOccurred())

			record := &dnsv1alpha1.Record{}
			Expect(k8sClient.Get(ctx, key, record)).To(Succeed())
			Expect(meta.IsStatusConditionFalse(record.Status.Conditions, conditionReady)).To(BeTrue())
			Expect(meta.IsStatusConditionTrue(record.Status.Conditions, conditionDegraded)).To(BeTrue())
			cond := meta.FindStatusCondition(record.Status.Conditions, conditionDegraded)
			Expect(cond).NotTo(BeNil())
			Expect(cond.Message).To(ContainSubstring("server unreachable"))
		})

		It("clears Degraded and returns to Ready once the server recovers", func() {
			createRecordCR(nil)

			failing := &fakeRecordAPI{getRecords: noRecords, addErr: fmt.Errorf("server unreachable")}
			_, err := newReconciler(failing).Reconcile(ctx, reconcile.Request{NamespacedName: key})
			Expect(err).To(HaveOccurred())

			healthy := &fakeRecordAPI{getRecords: noRecords}
			_, err = newReconciler(healthy).Reconcile(ctx, reconcile.Request{NamespacedName: key})
			Expect(err).NotTo(HaveOccurred())

			record := &dnsv1alpha1.Record{}
			Expect(k8sClient.Get(ctx, key, record)).To(Succeed())
			Expect(meta.IsStatusConditionTrue(record.Status.Conditions, conditionReady)).To(BeTrue())
			Expect(meta.IsStatusConditionFalse(record.Status.Conditions, conditionDegraded)).To(BeTrue())
		})
	})
})

var _ = Describe("addOptionsFromSpec", func() {
	It("maps type-specific rdata onto the matching field", func() {
		spec := dnsv1alpha1.RecordSpec{Zone: "example.com", Name: "www.example.com", Type: dnsv1alpha1.RecordTypeCNAME, Data: "example.com"}
		opts := addOptionsFromSpec(spec)
		Expect(opts.CName).To(Equal("example.com"))
		Expect(opts.IPAddress).To(BeEmpty())
	})
})
