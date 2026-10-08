/*
Copyright (c) 2026 Chris

Licensed under the MIT License. See the LICENSE file in the project root for
the full license text.
*/

package controller

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	dnsv1alpha1 "github.com/charg/technitium-operator/api/v1alpha1"
)

const (
	// serviceRoleLabel distinguishes the per-replica Services from the
	// cluster's other Services so they can be listed for cleanup.
	serviceRoleLabel = "dns.packet.fail/service-role"
	// serviceRoleReplica is the serviceRoleLabel value on per-replica Services.
	serviceRoleReplica = "replica"
	// podNameLabel is set by the StatefulSet controller on every pod.
	podNameLabel = "statefulset.kubernetes.io/pod-name"
	// managedAnnotationsKey lists, comma-separated, the annotation keys the
	// operator last applied from spec, so a key later dropped from spec can
	// be removed without touching keys other controllers own.
	managedAnnotationsKey = "dns.packet.fail/managed-annotations"
)

// applyManagedAnnotations sets desired on obj and removes the keys it set on
// a previous pass that desired no longer has, leaving every other annotation
// alone. Replacing the map outright would strip annotations other
// controllers write onto the same object, such as MetalLB's
// ip-allocated-from-pool; the controller writing it back then retriggers this
// reconcile, and the two loop forever.
func applyManagedAnnotations(obj metav1.Object, desired map[string]string) {
	annotations := maps.Clone(obj.GetAnnotations())
	if annotations == nil {
		annotations = map[string]string{}
	}
	if previous := annotations[managedAnnotationsKey]; previous != "" {
		for key := range strings.SplitSeq(previous, ",") {
			if _, ok := desired[key]; !ok {
				delete(annotations, key)
			}
		}
	}
	delete(annotations, managedAnnotationsKey)

	maps.Copy(annotations, desired)
	if len(desired) > 0 {
		annotations[managedAnnotationsKey] = strings.Join(slices.Sorted(maps.Keys(desired)), ",")
	}
	if len(annotations) == 0 {
		annotations = nil
	}
	obj.SetAnnotations(annotations)
}

// replicaServiceName is the per-replica Service name for an ordinal.
func replicaServiceName(clusterName string, ordinal int32) string {
	return fmt.Sprintf("%s-%d", clusterName, ordinal)
}

// serviceTypeSupportsTrafficPolicy reports whether the apiserver accepts
// externalTrafficPolicy on a Service of this type. An empty type is the
// ClusterIP default.
func serviceTypeSupportsTrafficPolicy(t corev1.ServiceType) bool {
	return t == corev1.ServiceTypeLoadBalancer || t == corev1.ServiceTypeNodePort
}

// preserveNodePorts copies the nodePort the apiserver already allocated onto
// the desired ports with the same name and protocol, so rewriting the port
// list on every reconcile does not release and reallocate them.
func preserveNodePorts(desired, existing []corev1.ServicePort) []corev1.ServicePort {
	for i := range desired {
		for _, e := range existing {
			if e.Name == desired[i].Name && e.Protocol == desired[i].Protocol {
				desired[i].NodePort = e.NodePort
			}
		}
	}
	return desired
}

// desiredReplicaCount is spec.replicas, defaulting to one.
func desiredReplicaCount(tc *dnsv1alpha1.TechnitiumCluster) int32 {
	if tc.Spec.Replicas != nil {
		return *tc.Spec.Replicas
	}
	return 1
}

// replicaServiceAnnotations merges the override for ordinal over the common
// per-replica annotations. It returns nil when there are none.
func replicaServiceAnnotations(spec *dnsv1alpha1.TechnitiumClusterPerReplicaServiceSpec, ordinal int32) map[string]string {
	var merged map[string]string
	if len(spec.Annotations) > 0 {
		merged = maps.Clone(spec.Annotations)
	}
	for _, o := range spec.Ordinals {
		if o.Ordinal != ordinal || len(o.Annotations) == 0 {
			continue
		}
		if merged == nil {
			merged = map[string]string{}
		}
		maps.Copy(merged, o.Annotations)
	}
	return merged
}

// reconcileReplicaServices ensures one Service per StatefulSet ordinal when
// spec.service.perReplica is set, and deletes the owned per-replica Services
// that are no longer wanted: those of ordinals at or above spec.replicas, or
// all of them when perReplica is unset.
func (r *TechnitiumClusterReconciler) reconcileReplicaServices(ctx context.Context, tc *dnsv1alpha1.TechnitiumCluster) error {
	log := logf.FromContext(ctx)
	spec := tc.Spec.Service.PerReplica

	wanted := map[string]bool{}
	if spec != nil {
		svcType := spec.Type
		if svcType == "" {
			svcType = corev1.ServiceTypeLoadBalancer
		}
		policy := corev1.ServiceExternalTrafficPolicy("")
		if serviceTypeSupportsTrafficPolicy(svcType) {
			policy = spec.ExternalTrafficPolicy
			if policy == "" {
				policy = corev1.ServiceExternalTrafficPolicyLocal
			}
		}

		for ordinal := int32(0); ordinal < desiredReplicaCount(tc); ordinal++ {
			svc := &corev1.Service{
				Name:      replicaServiceName(tc.Name, ordinal),
				Namespace: r.OperatorNamespace}
			wanted[svc.Name] = true

			op, err := controllerutil.CreateOrUpdate(ctx, r.Client, svc, func() error {
				if err := controllerutil.SetControllerReference(tc, svc, r.Scheme); err != nil {
					return err
				}
				labels := commonLabels(tc.Name)
				labels[serviceRoleLabel] = serviceRoleReplica
				svc.Labels = labels
				applyManagedAnnotations(svc, replicaServiceAnnotations(spec, ordinal))
				svc.Spec.Type = svcType
				svc.Spec.ExternalTrafficPolicy = policy
				selector := instanceLabels(tc.Name)
				selector[podNameLabel] = svc.Name
				svc.Spec.Selector = selector
				svc.Spec.Ports = preserveNodePorts(dnsServicePorts(false), svc.Spec.Ports)
				return nil
			})
			if err != nil {
				return fmt.Errorf("reconciling Service %s: %w", svc.Name, err)
			}
			if op != controllerutil.OperationResultNone {
				log.Info("Reconciled per-replica Service", "name", svc.Name, "operation", op)
			}
		}
	}

	var existing corev1.ServiceList
	if err := r.List(ctx, &existing, client.InNamespace(r.OperatorNamespace),
		client.MatchingLabels{
			"app.kubernetes.io/instance": tc.Name,
			serviceRoleLabel:             serviceRoleReplica,
		}); err != nil {
		return fmt.Errorf("listing per-replica Services: %w", err)
	}
	for i := range existing.Items {
		svc := &existing.Items[i]
		// Only Services this cluster controls are ever deleted: a label match
		// alone could be someone else's Service.
		if wanted[svc.Name] || !metav1.IsControlledBy(svc, tc) || !isReplicaServiceName(tc.Name, svc.Name) {
			continue
		}
		if err := r.Delete(ctx, svc); client.IgnoreNotFound(err) != nil {
			return fmt.Errorf("deleting Service %s: %w", svc.Name, err)
		}
		log.Info("Deleted per-replica Service", "name", svc.Name)
	}
	return nil
}

// isReplicaServiceName reports whether name is "<cluster>-<ordinal>".
func isReplicaServiceName(clusterName, name string) bool {
	suffix, ok := strings.CutPrefix(name, clusterName+"-")
	if !ok {
		return false
	}
	n, err := strconv.Atoi(suffix)
	return err == nil && n >= 0
}
