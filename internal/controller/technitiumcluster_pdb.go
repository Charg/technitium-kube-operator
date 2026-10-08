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

	policyv1 "k8s.io/api/policy/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	dnsv1alpha1 "github.com/charg/technitium-operator/api/v1alpha1"
)

// errPDBNotOwned marks a PodDisruptionBudget that already exists under the
// name the operator wants but is not controlled by this TechnitiumCluster.
// markDegraded gives it its own condition reason.
var errPDBNotOwned = errors.New("pod disruption budget exists but is not owned by this TechnitiumCluster")

// reasonPDBConflict is the Degraded reason for errPDBNotOwned.
const reasonPDBConflict = "PodDisruptionBudgetConflict"

func podDisruptionBudgetName(clusterName string) string { return clusterName }

// podDisruptionBudgetEnabled reports whether the operator should manage a
// PDB. The zero-value spec means enabled, so the controller does not depend on
// API defaulting having run.
func podDisruptionBudgetEnabled(tc *dnsv1alpha1.TechnitiumCluster) bool {
	e := tc.Spec.PodDisruptionBudget.Enabled
	return e == nil || *e
}

// podDisruptionBudgetMaxUnavailable is the configured maxUnavailable, or 1
// when unset.
func podDisruptionBudgetMaxUnavailable(tc *dnsv1alpha1.TechnitiumCluster) intstr.IntOrString {
	if m := tc.Spec.PodDisruptionBudget.MaxUnavailable; m != nil {
		return *m
	}
	return intstr.FromInt32(1)
}

// reconcilePodDisruptionBudget keeps a PDB in line with the spec: present for
// an enabled multi-replica cluster, absent otherwise. A same-named PDB that
// this cluster does not control is never adopted, modified or deleted: adopting
// it would silently take over a user's budget, and the conflict is surfaced as
// an error so the cluster reports Degraded instead.
func (r *TechnitiumClusterReconciler) reconcilePodDisruptionBudget(ctx context.Context, tc *dnsv1alpha1.TechnitiumCluster) error {
	log := logf.FromContext(ctx)
	key := types.NamespacedName{Namespace: r.OperatorNamespace, Name: podDisruptionBudgetName(tc.Name)}

	replicas := int32(1)
	if tc.Spec.Replicas != nil {
		replicas = *tc.Spec.Replicas
	}

	var existing policyv1.PodDisruptionBudget
	err := r.Get(ctx, key, &existing)
	found := err == nil
	if err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	owned := found && metav1.IsControlledBy(&existing, tc)

	if !podDisruptionBudgetEnabled(tc) || replicas <= 1 {
		if !found {
			return nil
		}
		if !owned {
			log.Info("Left unowned PodDisruptionBudget in place", "name", key.Name, "namespace", key.Namespace)
			return nil
		}
		if err := r.Delete(ctx, &existing); client.IgnoreNotFound(err) != nil {
			return err
		}
		log.Info("Deleted PodDisruptionBudget", "name", key.Name, "namespace", key.Namespace)
		return nil
	}

	if found && !owned {
		return fmt.Errorf("%w: %s/%s; remove it or set spec.podDisruptionBudget.enabled to false",
			errPDBNotOwned, key.Namespace, key.Name)
	}

	pdb := &policyv1.PodDisruptionBudget{Name: key.Name, Namespace: key.Namespace}
	op, err := controllerutil.CreateOrUpdate(ctx, r.Client, pdb, func() error {
		if err := controllerutil.SetControllerReference(tc, pdb, r.Scheme); err != nil {
			return err
		}
		pdb.Labels = commonLabels(tc.Name)
		maxUnavailable := podDisruptionBudgetMaxUnavailable(tc)
		pdb.Spec.MaxUnavailable = &maxUnavailable
		pdb.Spec.MinAvailable = nil
		pdb.Spec.Selector = &metav1.LabelSelector{MatchLabels: instanceLabels(tc.Name)}
		return nil
	})
	if err != nil {
		return err
	}
	if op != controllerutil.OperationResultNone {
		log.Info("Reconciled PodDisruptionBudget", "name", key.Name, "namespace", key.Namespace, "operation", op)
	}
	return nil
}
