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
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	dnsv1alpha1 "github.com/charg/technitium-operator/api/v1alpha1"
	"github.com/charg/technitium-operator/internal/technitium"
)

var _ = Describe("TechnitiumCluster Controller Service options", func() {
	ctx := context.Background()

	var (
		namespace     string
		resourceName  string
		key           types.NamespacedName
		nsCounter     int
		resourceCount int
	)

	// newReconciler wires fake clients so a multi-replica reconcile never
	// dials the per-pod endpoints envtest cannot serve.
	newReconciler := func() *TechnitiumClusterReconciler {
		return &TechnitiumClusterReconciler{
			Client:            k8sClient,
			Scheme:            k8sClient.Scheme(),
			OperatorNamespace: namespace,
			NewBootstrapClient: func(endpoint, username, password string) (bootstrapAPI, error) {
				return &fakeBootstrapClient{calls: new(int), token: "minted-token"}, nil
			},
			NewNodeClient: func(endpoint, username, password string) (nodeAPI, error) {
				return &fakeNodeClient{state: &technitium.ClusterState{
					Version:         "13.4.0",
					DNSServerDomain: resourceName,
				}}, nil
			},
		}
	}

	createCluster := func(replicas int32, svc dnsv1alpha1.TechnitiumClusterServiceSpec) *dnsv1alpha1.TechnitiumCluster {
		resourceCount++
		resourceName = fmt.Sprintf("svc-cluster-%d", resourceCount)
		key = types.NamespacedName{Name: resourceName}
		cr := &dnsv1alpha1.TechnitiumCluster{
			Name: resourceName,
			Spec: dnsv1alpha1.TechnitiumClusterSpec{
				Storage:  dnsv1alpha1.TechnitiumClusterStorageSpec{Size: resource.MustParse("1Gi")},
				Replicas: ptr.To(replicas),
				Service:  svc,
			},
		}
		Expect(k8sClient.Create(ctx, cr)).To(Succeed())
		return cr
	}

	reconcileOnce := func() {
		_, err := newReconciler().Reconcile(ctx, reconcile.Request{NamespacedName: key})
		Expect(err).NotTo(HaveOccurred())
	}

	getService := func(name string) (*corev1.Service, error) {
		svc := &corev1.Service{}
		err := k8sClient.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, svc)
		return svc, err
	}

	mustGetService := func(name string) *corev1.Service {
		svc, err := getService(name)
		Expect(err).NotTo(HaveOccurred())
		return svc
	}

	portNumbers := func(svc *corev1.Service) []int32 {
		ports := make([]int32, 0, len(svc.Spec.Ports))
		for _, p := range svc.Spec.Ports {
			ports = append(ports, p.Port)
		}
		return ports
	}

	updateService := func(mutate func(*dnsv1alpha1.TechnitiumClusterServiceSpec)) {
		var cr dnsv1alpha1.TechnitiumCluster
		Expect(k8sClient.Get(ctx, key, &cr)).To(Succeed())
		mutate(&cr.Spec.Service)
		Expect(k8sClient.Update(ctx, &cr)).To(Succeed())
	}

	BeforeEach(func() {
		nsCounter++
		namespace = fmt.Sprintf("tc-services-ns-%d", nsCounter)
		Expect(k8sClient.Create(ctx, &corev1.Namespace{Name: namespace})).To(Succeed())
	})

	AfterEach(func() {
		cr := &dnsv1alpha1.TechnitiumCluster{}
		if err := k8sClient.Get(ctx, key, cr); err == nil {
			_ = k8sClient.Delete(ctx, cr)
		}
	})

	Context("client Service", func() {
		It("applies externalTrafficPolicy, and clears it when the type goes back to ClusterIP", func() {
			createCluster(1, dnsv1alpha1.TechnitiumClusterServiceSpec{
				Type:                  corev1.ServiceTypeLoadBalancer,
				ExternalTrafficPolicy: corev1.ServiceExternalTrafficPolicyLocal,
			})
			reconcileOnce()

			svc := mustGetService(resourceName)
			Expect(svc.Spec.Type).To(Equal(corev1.ServiceTypeLoadBalancer))
			Expect(svc.Spec.ExternalTrafficPolicy).To(Equal(corev1.ServiceExternalTrafficPolicyLocal))

			// The apiserver rejects a policy on ClusterIP, so the update
			// only succeeds if the controller clears it.
			updateService(func(s *dnsv1alpha1.TechnitiumClusterServiceSpec) {
				s.Type = corev1.ServiceTypeClusterIP
				s.ExternalTrafficPolicy = ""
			})
			reconcileOnce()

			svc = mustGetService(resourceName)
			Expect(svc.Spec.Type).To(Equal(corev1.ServiceTypeClusterIP))
			Expect(svc.Spec.ExternalTrafficPolicy).To(BeEmpty())
		})

		It("drops the API port from the client Service only when exposeAPI is false", func() {
			createCluster(1, dnsv1alpha1.TechnitiumClusterServiceSpec{ExposeAPI: ptr.To(false)})
			reconcileOnce()

			Expect(portNumbers(mustGetService(resourceName))).To(Equal([]int32{53, 53}))
			// The operator calls the API through the headless Service.
			Expect(portNumbers(mustGetService(headlessServiceName(resourceName)))).To(ConsistOf(int32(53), int32(53), int32(5380)))

			var cr dnsv1alpha1.TechnitiumCluster
			Expect(k8sClient.Get(ctx, key, &cr)).To(Succeed())
			Expect(cr.Status.Endpoint).To(Equal(fmt.Sprintf("%s.%s.svc:53", resourceName, namespace)))
			Expect(cr.Status.PrimaryEndpoint).To(HavePrefix("http://"))

			updateService(func(s *dnsv1alpha1.TechnitiumClusterServiceSpec) { s.ExposeAPI = ptr.To(true) })
			reconcileOnce()
			Expect(portNumbers(mustGetService(resourceName))).To(Equal([]int32{53, 53, 5380}))
			Expect(k8sClient.Get(ctx, key, &cr)).To(Succeed())
			Expect(cr.Status.Endpoint).To(Equal(fmt.Sprintf("http://%s.%s.svc:5380", resourceName, namespace)))
		})

		It("rejects externalTrafficPolicy on a ClusterIP Service", func() {
			err := k8sClient.Create(ctx, &dnsv1alpha1.TechnitiumCluster{
				Name: "svc-cel-reject-client",
				Spec: dnsv1alpha1.TechnitiumClusterSpec{
					Storage: dnsv1alpha1.TechnitiumClusterStorageSpec{Size: resource.MustParse("1Gi")},
					Service: dnsv1alpha1.TechnitiumClusterServiceSpec{
						ExternalTrafficPolicy: corev1.ServiceExternalTrafficPolicyLocal,
					},
				},
			})
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("externalTrafficPolicy may only be set when type is LoadBalancer or NodePort"))
		})

		It("rejects externalTrafficPolicy on a ClusterIP per-replica Service", func() {
			err := k8sClient.Create(ctx, &dnsv1alpha1.TechnitiumCluster{
				Name: "svc-cel-reject-replica",
				Spec: dnsv1alpha1.TechnitiumClusterSpec{
					Storage: dnsv1alpha1.TechnitiumClusterStorageSpec{Size: resource.MustParse("1Gi")},
					Service: dnsv1alpha1.TechnitiumClusterServiceSpec{
						PerReplica: &dnsv1alpha1.TechnitiumClusterPerReplicaServiceSpec{
							Type:                  corev1.ServiceTypeClusterIP,
							ExternalTrafficPolicy: corev1.ServiceExternalTrafficPolicyLocal,
						},
					},
				},
			})
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("externalTrafficPolicy may only be set when type is LoadBalancer or NodePort"))
		})
	})

	Context("per-replica Services", func() {
		It("creates one DNS-only Service per ordinal with the common annotations and ordinal overrides merged", func() {
			createCluster(3, dnsv1alpha1.TechnitiumClusterServiceSpec{
				PerReplica: &dnsv1alpha1.TechnitiumClusterPerReplicaServiceSpec{
					Annotations: map[string]string{"common": "yes", "metallb.io/address-pool": "lan"},
					Ordinals: []dnsv1alpha1.TechnitiumClusterReplicaServiceOverride{
						{Ordinal: 0, Annotations: map[string]string{"metallb.io/loadBalancerIPs": "192.168.1.50"}},
						{Ordinal: 1, Annotations: map[string]string{"metallb.io/loadBalancerIPs": "192.168.1.51", "common": "override"}},
					},
				},
			})
			reconcileOnce()

			for ordinal := range 3 {
				name := fmt.Sprintf("%s-%d", resourceName, ordinal)
				svc := mustGetService(name)
				Expect(svc.Spec.Type).To(Equal(corev1.ServiceTypeLoadBalancer))
				// Defaulted by the controller when the spec leaves it unset.
				Expect(svc.Spec.ExternalTrafficPolicy).To(Equal(corev1.ServiceExternalTrafficPolicyLocal))
				Expect(svc.Spec.Selector).To(Equal(map[string]string{
					"app.kubernetes.io/name":             "technitium",
					"app.kubernetes.io/instance":         resourceName,
					"statefulset.kubernetes.io/pod-name": name,
				}))
				Expect(svc.Labels).To(HaveKeyWithValue("dns.packet.fail/service-role", "replica"))
				Expect(svc.Labels).To(HaveKeyWithValue("app.kubernetes.io/managed-by", "technitium-operator"))
				Expect(portNumbers(svc)).To(Equal([]int32{53, 53}))
				Expect(svc.Spec.Ports[0].Protocol).To(Equal(corev1.ProtocolUDP))
				Expect(svc.Spec.Ports[1].Protocol).To(Equal(corev1.ProtocolTCP))
				Expect(svc.OwnerReferences).To(HaveLen(1))
				Expect(*svc.OwnerReferences[0].Controller).To(BeTrue())
			}

			Expect(mustGetService(resourceName + "-0").Annotations).To(Equal(map[string]string{
				"common": "yes", "metallb.io/address-pool": "lan", "metallb.io/loadBalancerIPs": "192.168.1.50",
			}))
			Expect(mustGetService(resourceName + "-1").Annotations).To(Equal(map[string]string{
				"common": "override", "metallb.io/address-pool": "lan", "metallb.io/loadBalancerIPs": "192.168.1.51",
			}))
			Expect(mustGetService(resourceName + "-2").Annotations).To(Equal(map[string]string{
				"common": "yes", "metallb.io/address-pool": "lan",
			}))
		})

		It("honours an explicit type and policy, and leaves the policy unset for ClusterIP", func() {
			createCluster(1, dnsv1alpha1.TechnitiumClusterServiceSpec{
				PerReplica: &dnsv1alpha1.TechnitiumClusterPerReplicaServiceSpec{
					Type:                  corev1.ServiceTypeNodePort,
					ExternalTrafficPolicy: corev1.ServiceExternalTrafficPolicyCluster,
				},
			})
			reconcileOnce()

			svc := mustGetService(resourceName + "-0")
			Expect(svc.Spec.Type).To(Equal(corev1.ServiceTypeNodePort))
			Expect(svc.Spec.ExternalTrafficPolicy).To(Equal(corev1.ServiceExternalTrafficPolicyCluster))

			updateService(func(s *dnsv1alpha1.TechnitiumClusterServiceSpec) {
				s.PerReplica = &dnsv1alpha1.TechnitiumClusterPerReplicaServiceSpec{Type: corev1.ServiceTypeClusterIP}
			})
			reconcileOnce()

			svc = mustGetService(resourceName + "-0")
			Expect(svc.Spec.Type).To(Equal(corev1.ServiceTypeClusterIP))
			Expect(svc.Spec.ExternalTrafficPolicy).To(BeEmpty())
		})

		It("deletes the Services of removed ordinals on scale-down", func() {
			createCluster(3, dnsv1alpha1.TechnitiumClusterServiceSpec{
				PerReplica: &dnsv1alpha1.TechnitiumClusterPerReplicaServiceSpec{},
			})
			reconcileOnce()
			mustGetService(resourceName + "-2")

			var cr dnsv1alpha1.TechnitiumCluster
			Expect(k8sClient.Get(ctx, key, &cr)).To(Succeed())
			cr.Spec.Replicas = ptr.To(int32(2))
			Expect(k8sClient.Update(ctx, &cr)).To(Succeed())
			reconcileOnce()

			_, err := getService(resourceName + "-2")
			Expect(apierrors.IsNotFound(err)).To(BeTrue())
			mustGetService(resourceName + "-0")
			mustGetService(resourceName + "-1")
		})

		It("deletes every per-replica Service when perReplica is unset, but never an unowned one", func() {
			createCluster(2, dnsv1alpha1.TechnitiumClusterServiceSpec{
				PerReplica: &dnsv1alpha1.TechnitiumClusterPerReplicaServiceSpec{},
			})
			// Same labels and a replica-style name, but no owner reference:
			// the label match alone must not get it deleted.
			unowned := &corev1.Service{
				Namespace: namespace,
				Name:      resourceName + "-7",
				Labels: map[string]string{
					"app.kubernetes.io/instance":   resourceName,
					"dns.packet.fail/service-role": "replica",
				},
				Spec: corev1.ServiceSpec{Ports: []corev1.ServicePort{{Name: "dns", Port: 53, Protocol: corev1.ProtocolUDP}}},
			}
			Expect(k8sClient.Create(ctx, unowned)).To(Succeed())
			reconcileOnce()
			mustGetService(resourceName + "-0")
			mustGetService(resourceName + "-1")

			updateService(func(s *dnsv1alpha1.TechnitiumClusterServiceSpec) { s.PerReplica = nil })
			reconcileOnce()

			for _, name := range []string{resourceName + "-0", resourceName + "-1"} {
				_, err := getService(name)
				Expect(apierrors.IsNotFound(err)).To(BeTrue())
			}
			mustGetService(resourceName + "-7")
			// The client and headless Services are untouched.
			mustGetService(resourceName)
			mustGetService(headlessServiceName(resourceName))
		})
	})
})
