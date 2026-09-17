/*
Copyright (c) 2026 Chris

Licensed under the MIT License. See the LICENSE file in the project root for
the full license text.
*/

package controller

import (
	"context"
	"errors"
	"fmt"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	dnsv1alpha1 "github.com/charg/technitium-operator/api/v1alpha1"
	"github.com/charg/technitium-operator/internal/technitium"
)

var _ = Describe("TechnitiumCluster Controller teardown", func() {
	Context("When a TechnitiumCluster is deleted", func() {
		ctx := context.Background()

		var (
			namespace     string
			resourceName  string
			key           types.NamespacedName
			nsCounter     int
			resourceCount int

			// The primary (ordinal 0) is the only node finalizeCluster ever
			// dials, so a single fakeNodeClient's canned responses and call
			// counters cover every teardown test in this file.
			primaryState         *technitium.ClusterState
			primaryStateErr      error
			removeSecondaryCalls int
			removeSecondaryIDs   []int
			removeSecondaryErr   error
			deleteSecondaryCalls int
			deleteSecondaryIDs   []int
			deleteSecondaryErr   error
			deletePrimaryCalls   int
			deletePrimaryErr     error
		)

		newNamespace := func() string {
			nsCounter++
			name := fmt.Sprintf("tc-teardown-ns-%d", nsCounter)
			ns := &corev1.Namespace{Name: name}
			Expect(k8sClient.Create(ctx, ns)).To(Succeed())
			return name
		}

		newReconciler := func() *TechnitiumClusterReconciler {
			return &TechnitiumClusterReconciler{
				Client:            k8sClient,
				Scheme:            k8sClient.Scheme(),
				OperatorNamespace: namespace,
				NewBootstrapClient: func(endpoint, username, password string) (bootstrapAPI, error) {
					return &fakeBootstrapClient{calls: new(int), token: "minted-token"}, nil
				},
				NewNodeClient: func(endpoint, username, password string) (nodeAPI, error) {
					return &fakeNodeClient{
						state:                primaryState,
						err:                  primaryStateErr,
						removeSecondaryCalls: &removeSecondaryCalls,
						removeSecondaryIDs:   &removeSecondaryIDs,
						removeSecondaryErr:   removeSecondaryErr,
						deleteSecondaryCalls: &deleteSecondaryCalls,
						deleteSecondaryIDs:   &deleteSecondaryIDs,
						deleteSecondaryErr:   deleteSecondaryErr,
						deletePrimaryCalls:   &deletePrimaryCalls,
						deletePrimaryErr:     deletePrimaryErr,
					}, nil
				},
			}
		}

		storageSize := resource.MustParse("1Gi")

		createClusterCR := func(mutate func(*dnsv1alpha1.TechnitiumCluster)) *dnsv1alpha1.TechnitiumCluster {
			resourceCount++
			resourceName = fmt.Sprintf("teardown-cluster-%d", resourceCount)
			key = types.NamespacedName{Name: resourceName}

			cr := &dnsv1alpha1.TechnitiumCluster{
				Name: resourceName,
				Spec: dnsv1alpha1.TechnitiumClusterSpec{
					Image:   "technitium/dns-server:15.4.0",
					Storage: dnsv1alpha1.TechnitiumClusterStorageSpec{Size: storageSize},
				},
			}
			if mutate != nil {
				mutate(cr)
			}
			Expect(k8sClient.Create(ctx, cr)).To(Succeed())
			return cr
		}

		// createDataPVC creates a PVC carrying the same instance labels the
		// StatefulSet's volumeClaimTemplate would stamp onto its per-ordinal
		// PVCs, so reconcilePVCRetention's label-based list finds it exactly
		// as it would find a real "data-<name>-<ordinal>" PVC.
		createDataPVC := func(name string) {
			pvc := &corev1.PersistentVolumeClaim{
				Name:      name,
				Namespace: namespace,
				Labels:    instanceLabels(resourceName),
				Spec: corev1.PersistentVolumeClaimSpec{
					AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
					Resources: corev1.VolumeResourceRequirements{
						Requests: corev1.ResourceList{corev1.ResourceStorage: storageSize},
					},
				},
			}
			Expect(k8sClient.Create(ctx, pvc)).To(Succeed())
		}

		// deleteAndFinalize sets the CR's deletion timestamp (Delete on an
		// object that still carries the finalizer marks it for deletion
		// without removing it) and then runs the Reconcile pass that is
		// expected to observe that timestamp and drive finalizeCluster.
		deleteAndFinalize := func(r *TechnitiumClusterReconciler) {
			var cr dnsv1alpha1.TechnitiumCluster
			Expect(k8sClient.Get(ctx, key, &cr)).To(Succeed())
			Expect(k8sClient.Delete(ctx, &cr)).To(Succeed())

			_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: key})
			Expect(err).NotTo(HaveOccurred())
		}

		expectClusterGone := func() {
			var cr dnsv1alpha1.TechnitiumCluster
			err := k8sClient.Get(ctx, key, &cr)
			Expect(apierrors.IsNotFound(err)).To(BeTrue(), "expected the TechnitiumCluster to be gone once the finalizer clears, got: %v", err)
		}

		BeforeEach(func() {
			namespace = newNamespace()
			primaryState = &technitium.ClusterState{ClusterInitialized: false}
			primaryStateErr = nil
			removeSecondaryCalls = 0
			removeSecondaryIDs = nil
			removeSecondaryErr = nil
			deleteSecondaryCalls = 0
			deleteSecondaryIDs = nil
			deleteSecondaryErr = nil
			deletePrimaryCalls = 0
			deletePrimaryErr = nil
		})

		AfterEach(func() {
			// Best-effort: most tests already drive the CR to deletion
			// themselves; this only mops up a test that failed before
			// reaching that point.
			cr := &dnsv1alpha1.TechnitiumCluster{}
			if err := k8sClient.Get(ctx, key, cr); err == nil {
				_ = k8sClient.Delete(ctx, cr)
			}
		})

		It("removes each secondary and deletes the primary cluster state, then clears the finalizer", func() {
			createClusterCR(nil)
			r := newReconciler()
			_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: key})
			Expect(err).NotTo(HaveOccurred())

			primaryState = &technitium.ClusterState{
				ClusterInitialized: true,
				ClusterDomain:      "cluster.local",
				Nodes: []technitium.ClusterNode{
					{ID: 1, Name: resourceName + "-0.cluster.local", Type: "Primary", State: "Self"},
					{ID: 7, Name: resourceName + "-1.cluster.local", Type: "Secondary", State: "Connected"},
				},
			}

			deleteAndFinalize(r)

			Expect(removeSecondaryCalls).To(Equal(1))
			Expect(removeSecondaryIDs).To(ConsistOf(7))
			Expect(deleteSecondaryCalls).To(Equal(1))
			Expect(deleteSecondaryIDs).To(ConsistOf(7))
			Expect(deletePrimaryCalls).To(Equal(1))

			expectClusterGone()
		})

		It("clears the finalizer without wedging when every teardown call fails", func() {
			createClusterCR(nil)
			r := newReconciler()
			_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: key})
			Expect(err).NotTo(HaveOccurred())

			// The primary itself answers (it is reachable), but every
			// teardown call against it fails: this exercises the
			// best-effort path where the workload is coming apart under the
			// finalizer's feet, not just a fully unreachable instance.
			primaryState = &technitium.ClusterState{
				ClusterInitialized: true,
				Nodes: []technitium.ClusterNode{
					{ID: 1, Name: resourceName + "-0.cluster.local", Type: "Primary", State: "Self"},
					{ID: 9, Name: resourceName + "-1.cluster.local", Type: "Secondary", State: "Connected"},
				},
			}
			removeSecondaryErr = errors.New("secondary pod already gone")
			deleteSecondaryErr = errors.New("secondary pod already gone")
			deletePrimaryErr = errors.New("primary unreachable")

			deleteAndFinalize(r)

			Expect(removeSecondaryCalls).To(Equal(1))
			Expect(deleteSecondaryCalls).To(Equal(1))
			Expect(deletePrimaryCalls).To(Equal(1))

			expectClusterGone()
		})

		It("clears the finalizer without wedging when the primary is entirely unreachable", func() {
			createClusterCR(nil)
			r := newReconciler()
			_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: key})
			Expect(err).NotTo(HaveOccurred())

			primaryStateErr = errors.New("dial tcp: connection refused")

			deleteAndFinalize(r)

			// GetClusterState itself failed, so there is no clusterNodes
			// list to act on: none of the per-secondary calls ever run.
			Expect(removeSecondaryCalls).To(Equal(0))
			Expect(deleteSecondaryCalls).To(Equal(0))
			Expect(deletePrimaryCalls).To(Equal(0))

			expectClusterGone()
		})

		It("deletes the data PVCs when retentionPolicy is Delete", func() {
			createClusterCR(func(cr *dnsv1alpha1.TechnitiumCluster) {
				cr.Spec.Storage.RetentionPolicy = dnsv1alpha1.PVCRetentionPolicyDelete
			})
			r := newReconciler()
			_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: key})
			Expect(err).NotTo(HaveOccurred())

			pvcName := "data-" + resourceName + "-0"
			createDataPVC(pvcName)

			deleteAndFinalize(r)

			// A real cluster's kubernetes.io/pvc-protection admission
			// finalizer keeps the object around (as it would any bound PVC)
			// until nothing references it any more; envtest stamps that same
			// finalizer without a controller ever clearing it. A set
			// DeletionTimestamp is therefore the observable proof that
			// reconcilePVCRetention issued the Delete, not the object's
			// absence.
			var pvc corev1.PersistentVolumeClaim
			Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: namespace, Name: pvcName}, &pvc)).To(Succeed())
			Expect(pvc.DeletionTimestamp).NotTo(BeNil())
		})

		It("retains the data PVCs by default", func() {
			createClusterCR(nil)
			r := newReconciler()
			_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: key})
			Expect(err).NotTo(HaveOccurred())

			pvcName := "data-" + resourceName + "-0"
			createDataPVC(pvcName)

			deleteAndFinalize(r)

			var pvc corev1.PersistentVolumeClaim
			Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: namespace, Name: pvcName}, &pvc)).To(Succeed())
		})

		It("skips the in-Technitium teardown under an Orphan deletion policy but still clears the finalizer", func() {
			createClusterCR(func(cr *dnsv1alpha1.TechnitiumCluster) {
				cr.Spec.DeletionPolicy = dnsv1alpha1.DeletionPolicyOrphan
			})
			r := newReconciler()
			_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: key})
			Expect(err).NotTo(HaveOccurred())

			// If Orphan were not honored, this state (a cluster with a
			// Secondary to remove) would drive the same non-zero call
			// counts the Delete-policy test above asserts on.
			primaryState = &technitium.ClusterState{
				ClusterInitialized: true,
				Nodes: []technitium.ClusterNode{
					{ID: 1, Name: resourceName + "-0.cluster.local", Type: "Primary", State: "Self"},
					{ID: 3, Name: resourceName + "-1.cluster.local", Type: "Secondary", State: "Connected"},
				},
			}

			deleteAndFinalize(r)

			Expect(removeSecondaryCalls).To(Equal(0))
			Expect(deleteSecondaryCalls).To(Equal(0))
			Expect(deletePrimaryCalls).To(Equal(0))

			expectClusterGone()
		})
	})
})
