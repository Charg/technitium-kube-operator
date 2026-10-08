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
	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	dnsv1alpha1 "github.com/charg/technitium-operator/api/v1alpha1"
)

// Reconcile never sees the StatefulSet become ready under envtest, so none of
// these tests reach bootstrap or clustering: they only exercise the PDB logic.
var _ = Describe("TechnitiumCluster Controller PodDisruptionBudget", func() {
	Context("When a TechnitiumCluster is reconciled", func() {
		ctx := context.Background()

		var (
			namespace     string
			resourceName  string
			key           types.NamespacedName
			nsCounter     int
			resourceCount int
		)

		newNamespace := func() string {
			nsCounter++
			name := fmt.Sprintf("tc-pdb-ns-%d", nsCounter)
			ns := &corev1.Namespace{Name: name}
			Expect(k8sClient.Create(ctx, ns)).To(Succeed())
			return name
		}

		newReconciler := func() *TechnitiumClusterReconciler {
			return &TechnitiumClusterReconciler{
				Client:            k8sClient,
				Scheme:            k8sClient.Scheme(),
				OperatorNamespace: namespace,
			}
		}

		createClusterCR := func(mutate func(*dnsv1alpha1.TechnitiumCluster)) {
			cr := &dnsv1alpha1.TechnitiumCluster{
				Name: resourceName,
				Spec: dnsv1alpha1.TechnitiumClusterSpec{
					Image:   "technitium/dns-server:15.4.0",
					Storage: dnsv1alpha1.TechnitiumClusterStorageSpec{Size: resource.MustParse("1Gi")},
				},
			}
			if mutate != nil {
				mutate(cr)
			}
			Expect(k8sClient.Create(ctx, cr)).To(Succeed())
		}

		updateCluster := func(mutate func(*dnsv1alpha1.TechnitiumCluster)) {
			var cr dnsv1alpha1.TechnitiumCluster
			Expect(k8sClient.Get(ctx, key, &cr)).To(Succeed())
			mutate(&cr)
			Expect(k8sClient.Update(ctx, &cr)).To(Succeed())
		}

		reconcileOnce := func(r *TechnitiumClusterReconciler) error {
			_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: key})
			return err
		}

		getPDB := func() (*policyv1.PodDisruptionBudget, error) {
			var pdb policyv1.PodDisruptionBudget
			err := k8sClient.Get(ctx, types.NamespacedName{Namespace: namespace, Name: resourceName}, &pdb)
			return &pdb, err
		}

		expectNoPDB := func() {
			_, err := getPDB()
			ExpectWithOffset(1, apierrors.IsNotFound(err)).To(BeTrue(), "expected no PodDisruptionBudget, got: %v", err)
		}

		BeforeEach(func() {
			namespace = newNamespace()
			resourceCount++
			resourceName = fmt.Sprintf("pdb-cluster-%d", resourceCount)
			key = types.NamespacedName{Name: resourceName}
		})

		AfterEach(func() {
			cr := &dnsv1alpha1.TechnitiumCluster{}
			if err := k8sClient.Get(ctx, key, cr); err == nil {
				_ = k8sClient.Delete(ctx, cr)
			}
		})

		It("creates no PodDisruptionBudget for a single replica", func() {
			createClusterCR(nil)
			Expect(reconcileOnce(newReconciler())).To(Succeed())
			expectNoPDB()
		})

		It("creates an owned PodDisruptionBudget with maxUnavailable 1 for multiple replicas", func() {
			createClusterCR(func(cr *dnsv1alpha1.TechnitiumCluster) { cr.Spec.Replicas = ptr.To(int32(3)) })
			Expect(reconcileOnce(newReconciler())).To(Succeed())

			pdb, err := getPDB()
			Expect(err).NotTo(HaveOccurred())
			Expect(pdb.Spec.MaxUnavailable).NotTo(BeNil())
			Expect(*pdb.Spec.MaxUnavailable).To(Equal(intstr.FromInt32(1)))
			Expect(pdb.Spec.MinAvailable).To(BeNil())
			Expect(pdb.Spec.Selector.MatchLabels).To(Equal(instanceLabels(resourceName)))
			Expect(pdb.Labels).To(Equal(commonLabels(resourceName)))
			ref := metav1.GetControllerOf(pdb)
			Expect(ref).NotTo(BeNil())
			Expect(ref.Kind).To(Equal("TechnitiumCluster"))
			Expect(ref.Name).To(Equal(resourceName))
		})

		It("honours a custom maxUnavailable and updates it in place", func() {
			createClusterCR(func(cr *dnsv1alpha1.TechnitiumCluster) {
				cr.Spec.Replicas = ptr.To(int32(3))
				cr.Spec.PodDisruptionBudget.MaxUnavailable = ptr.To(intstr.FromString("50%"))
			})
			r := newReconciler()
			Expect(reconcileOnce(r)).To(Succeed())
			pdb, err := getPDB()
			Expect(err).NotTo(HaveOccurred())
			Expect(*pdb.Spec.MaxUnavailable).To(Equal(intstr.FromString("50%")))

			updateCluster(func(cr *dnsv1alpha1.TechnitiumCluster) {
				cr.Spec.PodDisruptionBudget.MaxUnavailable = ptr.To(intstr.FromInt32(2))
			})
			Expect(reconcileOnce(r)).To(Succeed())
			pdb, err = getPDB()
			Expect(err).NotTo(HaveOccurred())
			Expect(*pdb.Spec.MaxUnavailable).To(Equal(intstr.FromInt32(2)))
		})

		It("deletes the PodDisruptionBudget when scaled down to one replica", func() {
			createClusterCR(func(cr *dnsv1alpha1.TechnitiumCluster) { cr.Spec.Replicas = ptr.To(int32(3)) })
			r := newReconciler()
			Expect(reconcileOnce(r)).To(Succeed())
			_, err := getPDB()
			Expect(err).NotTo(HaveOccurred())

			updateCluster(func(cr *dnsv1alpha1.TechnitiumCluster) { cr.Spec.Replicas = ptr.To(int32(1)) })
			Expect(reconcileOnce(r)).To(Succeed())
			expectNoPDB()
		})

		It("does not create a PodDisruptionBudget when disabled, and deletes one it created earlier", func() {
			createClusterCR(func(cr *dnsv1alpha1.TechnitiumCluster) {
				cr.Spec.Replicas = ptr.To(int32(3))
				cr.Spec.PodDisruptionBudget.Enabled = ptr.To(false)
			})
			r := newReconciler()
			Expect(reconcileOnce(r)).To(Succeed())
			expectNoPDB()

			updateCluster(func(cr *dnsv1alpha1.TechnitiumCluster) { cr.Spec.PodDisruptionBudget.Enabled = ptr.To(true) })
			Expect(reconcileOnce(r)).To(Succeed())
			_, err := getPDB()
			Expect(err).NotTo(HaveOccurred())

			updateCluster(func(cr *dnsv1alpha1.TechnitiumCluster) { cr.Spec.PodDisruptionBudget.Enabled = ptr.To(false) })
			Expect(reconcileOnce(r)).To(Succeed())
			expectNoPDB()
		})

		Context("with a same-named PodDisruptionBudget the cluster does not own", func() {
			createUnowned := func() {
				pdb := &policyv1.PodDisruptionBudget{
					Name:      resourceName,
					Namespace: namespace,
					Spec: policyv1.PodDisruptionBudgetSpec{
						MinAvailable: ptr.To(intstr.FromInt32(2)),
						Selector:     &metav1.LabelSelector{MatchLabels: map[string]string{"hand": "made"}},
					},
				}
				Expect(k8sClient.Create(ctx, pdb)).To(Succeed())
			}

			expectUntouched := func() {
				pdb, err := getPDB()
				Expect(err).NotTo(HaveOccurred())
				Expect(pdb.OwnerReferences).To(BeEmpty())
				Expect(pdb.Spec.MinAvailable).NotTo(BeNil())
				Expect(pdb.Spec.MaxUnavailable).To(BeNil())
				Expect(pdb.Spec.Selector.MatchLabels).To(Equal(map[string]string{"hand": "made"}))
			}

			It("refuses to adopt it and reports Degraded when enabled", func() {
				createUnowned()
				createClusterCR(func(cr *dnsv1alpha1.TechnitiumCluster) { cr.Spec.Replicas = ptr.To(int32(3)) })

				err := reconcileOnce(newReconciler())
				Expect(err).To(MatchError(errPDBNotOwned))
				expectUntouched()

				var cr dnsv1alpha1.TechnitiumCluster
				Expect(k8sClient.Get(ctx, key, &cr)).To(Succeed())
				cond := meta.FindStatusCondition(cr.Status.Conditions, dnsv1alpha1.TechnitiumClusterConditionDegraded)
				Expect(cond).NotTo(BeNil())
				Expect(cond.Status).To(Equal(metav1.ConditionTrue))
				Expect(cond.Reason).To(Equal(reasonPDBConflict))
			})

			It("never deletes it when disabled or scaled to one replica", func() {
				createUnowned()
				createClusterCR(func(cr *dnsv1alpha1.TechnitiumCluster) {
					cr.Spec.Replicas = ptr.To(int32(3))
					cr.Spec.PodDisruptionBudget.Enabled = ptr.To(false)
				})
				r := newReconciler()
				Expect(reconcileOnce(r)).To(Succeed())
				expectUntouched()

				updateCluster(func(cr *dnsv1alpha1.TechnitiumCluster) {
					cr.Spec.Replicas = ptr.To(int32(1))
					cr.Spec.PodDisruptionBudget.Enabled = ptr.To(true)
				})
				Expect(reconcileOnce(r)).To(Succeed())
				expectUntouched()
			})
		})
	})
})
