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
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	dnsv1alpha1 "github.com/charg/technitium-operator/api/v1alpha1"
	"github.com/charg/technitium-operator/internal/technitium"
)

// fakeClusterAPI stands in for a single Technitium node. It mutates its own
// state on init/join so a repeat reconcile observes an already-initialized node,
// exercising idempotency.
type fakeClusterAPI struct {
	domain    string
	role      string
	initState bool

	stateErr error
	initErr  error
	joinErr  error

	stateCalls int
	initCalls  int
	joinCalls  int
}

func (f *fakeClusterAPI) snapshot() *technitium.ClusterState {
	state := &technitium.ClusterState{Initialized: f.initState}
	if f.initState {
		state.Domain = f.domain
		state.Nodes = []technitium.ClusterNodeInfo{{
			Name:  "node",
			Type:  f.role,
			State: "Self",
		}}
	}
	return state
}

func (f *fakeClusterAPI) ClusterState(context.Context) (*technitium.ClusterState, error) {
	f.stateCalls++
	if f.stateErr != nil {
		return nil, f.stateErr
	}
	return f.snapshot(), nil
}

func (f *fakeClusterAPI) ClusterInit(_ context.Context, opts technitium.ClusterInitOptions) (*technitium.ClusterState, error) {
	f.initCalls++
	if f.initErr != nil {
		// An "already initialized" failure means the node is in fact a member, so
		// reflect that in state for the re-read the reconciler performs.
		if errors.Is(f.initErr, technitium.ErrClusterAlreadyInitialized) {
			f.initState = true
			f.role = technitium.ClusterNodeTypePrimary
			f.domain = opts.ClusterDomain
		}
		return nil, f.initErr
	}
	f.initState = true
	f.role = technitium.ClusterNodeTypePrimary
	f.domain = opts.ClusterDomain
	return f.snapshot(), nil
}

func (f *fakeClusterAPI) ClusterInitJoin(_ context.Context, _ technitium.ClusterInitJoinOptions) (*technitium.ClusterState, error) {
	f.joinCalls++
	if f.joinErr != nil {
		if errors.Is(f.joinErr, technitium.ErrClusterAlreadyInitialized) {
			f.initState = true
			f.role = technitium.ClusterNodeTypeSecondary
		}
		return nil, f.joinErr
	}
	f.initState = true
	f.role = technitium.ClusterNodeTypeSecondary
	return f.snapshot(), nil
}

var _ = Describe("Cluster Controller", func() {
	const (
		resourceName  = "test-cluster"
		clusterDomain = "cluster.example.com"
		primaryURL    = "https://ns1.example.com:5380"
		secondaryURL  = "https://ns2.example.com:5380"
		namespace     = "default"
	)

	ctx := context.Background()
	key := types.NamespacedName{Name: resourceName}

	// nodes maps a node endpoint to its fake, letting the factory hand back the
	// same fake across reconciles so state persists.
	var nodes map[string]*fakeClusterAPI

	newReconciler := func() *ClusterReconciler {
		return &ClusterReconciler{
			Client:            k8sClient,
			Scheme:            k8sClient.Scheme(),
			OperatorNamespace: namespace,
			NewNodeClient: func(_ context.Context, endpoint string, _ ...technitium.Option) (ClusterAPI, error) {
				api, ok := nodes[endpoint]
				if !ok {
					return nil, fmt.Errorf("no fake node for %s", endpoint)
				}
				return api, nil
			},
		}
	}

	createSecret := func(name string, data map[string][]byte) {
		secret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
			Data:       data,
		}
		Expect(k8sClient.Create(ctx, secret)).To(Succeed())
	}

	nodeSpec := func(name, endpoint, secretName string) dnsv1alpha1.ClusterNodeSpec {
		return dnsv1alpha1.ClusterNodeSpec{
			Name:                 name,
			Endpoint:             endpoint,
			IPAddresses:          []string{"10.0.0.1"},
			CredentialsSecretRef: dnsv1alpha1.SecretReference{Name: secretName, Namespace: namespace},
		}
	}

	createCluster := func(mutate func(*dnsv1alpha1.Cluster)) {
		resource := &dnsv1alpha1.Cluster{
			ObjectMeta: metav1.ObjectMeta{Name: resourceName},
			Spec: dnsv1alpha1.ClusterSpec{
				ClusterDomain: clusterDomain,
				Primary:       nodeSpec("ns1", primaryURL, "primary-creds"),
			},
		}
		if mutate != nil {
			mutate(resource)
		}
		Expect(k8sClient.Create(ctx, resource)).To(Succeed())
	}

	BeforeEach(func() {
		nodes = map[string]*fakeClusterAPI{
			primaryURL:   {},
			secondaryURL: {},
		}
		createSecret("primary-creds", map[string][]byte{"username": []byte("admin"), "password": []byte("secret")})
		createSecret("secondary-creds", map[string][]byte{"username": []byte("admin"), "password": []byte("secret")})
	})

	AfterEach(func() {
		cluster := &dnsv1alpha1.Cluster{}
		if err := k8sClient.Get(ctx, key, cluster); err == nil {
			Expect(k8sClient.Delete(ctx, cluster)).To(Succeed())
		}
		for _, name := range []string{"primary-creds", "secondary-creds", "token-creds"} {
			secret := &corev1.Secret{}
			if err := k8sClient.Get(ctx, types.NamespacedName{Name: name, Namespace: namespace}, secret); err == nil {
				Expect(k8sClient.Delete(ctx, secret)).To(Succeed())
			}
		}
	})

	It("initializes the primary and joins the secondary", func() {
		createCluster(func(c *dnsv1alpha1.Cluster) {
			c.Spec.Secondaries = []dnsv1alpha1.ClusterNodeSpec{nodeSpec("ns2", secondaryURL, "secondary-creds")}
		})

		result, err := newReconciler().Reconcile(ctx, reconcile.Request{NamespacedName: key})
		Expect(err).NotTo(HaveOccurred())
		Expect(result.RequeueAfter).To(Equal(clusterDriftReconcileInterval))

		Expect(nodes[primaryURL].initCalls).To(Equal(1))
		Expect(nodes[secondaryURL].joinCalls).To(Equal(1))

		cluster := &dnsv1alpha1.Cluster{}
		Expect(k8sClient.Get(ctx, key, cluster)).To(Succeed())
		Expect(meta.IsStatusConditionTrue(cluster.Status.Conditions, clusterConditionReady)).To(BeTrue())
		Expect(cluster.Status.ClusterDomain).To(Equal(clusterDomain))
		Expect(cluster.Status.Nodes).To(HaveLen(2))
		Expect(cluster.Status.Nodes[0].Role).To(Equal(technitium.ClusterNodeTypePrimary))
		Expect(cluster.Status.Nodes[0].Member).To(BeTrue())
		Expect(cluster.Status.Nodes[1].Role).To(Equal(technitium.ClusterNodeTypeSecondary))
		Expect(cluster.Status.Nodes[1].Member).To(BeTrue())
	})

	It("does not re-init or re-join an already formed cluster", func() {
		createCluster(func(c *dnsv1alpha1.Cluster) {
			c.Spec.Secondaries = []dnsv1alpha1.ClusterNodeSpec{nodeSpec("ns2", secondaryURL, "secondary-creds")}
		})
		reconciler := newReconciler()

		_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: key})
		Expect(err).NotTo(HaveOccurred())

		_, err = reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: key})
		Expect(err).NotTo(HaveOccurred())

		// Init and join must each have happened exactly once across both passes.
		Expect(nodes[primaryURL].initCalls).To(Equal(1))
		Expect(nodes[secondaryURL].joinCalls).To(Equal(1))
	})

	It("treats an already-initialized node as success", func() {
		// The node reports uninitialized on the first read, then reports it is
		// already initialized when init is attempted (a concurrent init raced us).
		nodes[primaryURL].initErr = fmt.Errorf("%w: already", technitium.ErrClusterAlreadyInitialized)
		createCluster(nil)

		_, err := newReconciler().Reconcile(ctx, reconcile.Request{NamespacedName: key})
		Expect(err).NotTo(HaveOccurred())

		cluster := &dnsv1alpha1.Cluster{}
		Expect(k8sClient.Get(ctx, key, cluster)).To(Succeed())
		Expect(cluster.Status.Nodes[0].Member).To(BeTrue())
		Expect(meta.IsStatusConditionTrue(cluster.Status.Conditions, clusterConditionReady)).To(BeTrue())
	})

	It("requeues without joining when the primary is unreachable", func() {
		nodes[primaryURL].stateErr = fmt.Errorf("connection refused")
		createCluster(func(c *dnsv1alpha1.Cluster) {
			c.Spec.Secondaries = []dnsv1alpha1.ClusterNodeSpec{nodeSpec("ns2", secondaryURL, "secondary-creds")}
		})

		result, err := newReconciler().Reconcile(ctx, reconcile.Request{NamespacedName: key})
		Expect(err).NotTo(HaveOccurred())
		Expect(result.RequeueAfter).To(Equal(clusterRetryInterval))
		Expect(nodes[secondaryURL].joinCalls).To(BeZero())

		cluster := &dnsv1alpha1.Cluster{}
		Expect(k8sClient.Get(ctx, key, cluster)).To(Succeed())
		Expect(meta.IsStatusConditionFalse(cluster.Status.Conditions, clusterConditionReady)).To(BeTrue())
		Expect(cluster.Status.Nodes[0].Member).To(BeFalse())
	})

	It("converges other nodes when one secondary is unreachable", func() {
		nodes[secondaryURL].stateErr = fmt.Errorf("connection refused")
		createCluster(func(c *dnsv1alpha1.Cluster) {
			c.Spec.Secondaries = []dnsv1alpha1.ClusterNodeSpec{nodeSpec("ns2", secondaryURL, "secondary-creds")}
		})

		result, err := newReconciler().Reconcile(ctx, reconcile.Request{NamespacedName: key})
		Expect(err).NotTo(HaveOccurred())
		Expect(result.RequeueAfter).To(Equal(clusterRetryInterval))

		// The primary still initialized even though the secondary failed.
		Expect(nodes[primaryURL].initCalls).To(Equal(1))

		cluster := &dnsv1alpha1.Cluster{}
		Expect(k8sClient.Get(ctx, key, cluster)).To(Succeed())
		Expect(cluster.Status.Nodes[0].Member).To(BeTrue())
		Expect(cluster.Status.Nodes[1].Member).To(BeFalse())
		Expect(cluster.Status.Nodes[1].Message).NotTo(BeEmpty())
		Expect(meta.IsStatusConditionFalse(cluster.Status.Conditions, clusterConditionReady)).To(BeTrue())
	})

	It("reconciles a primary-only cluster", func() {
		createCluster(nil)

		result, err := newReconciler().Reconcile(ctx, reconcile.Request{NamespacedName: key})
		Expect(err).NotTo(HaveOccurred())
		Expect(result.RequeueAfter).To(Equal(clusterDriftReconcileInterval))

		cluster := &dnsv1alpha1.Cluster{}
		Expect(k8sClient.Get(ctx, key, cluster)).To(Succeed())
		Expect(cluster.Status.Nodes).To(HaveLen(1))
		Expect(meta.IsStatusConditionTrue(cluster.Status.Conditions, clusterConditionReady)).To(BeTrue())
	})

	It("fails when the primary credentials lack a username and password", func() {
		createSecret("token-creds", map[string][]byte{"token": []byte("abc123")})
		createCluster(func(c *dnsv1alpha1.Cluster) {
			c.Spec.Primary = nodeSpec("ns1", primaryURL, "token-creds")
			c.Spec.Secondaries = []dnsv1alpha1.ClusterNodeSpec{nodeSpec("ns2", secondaryURL, "secondary-creds")}
		})

		_, err := newReconciler().Reconcile(ctx, reconcile.Request{NamespacedName: key})
		Expect(err).To(HaveOccurred())

		cluster := &dnsv1alpha1.Cluster{}
		Expect(k8sClient.Get(ctx, key, cluster)).To(Succeed())
		Expect(meta.IsStatusConditionTrue(cluster.Status.Conditions, clusterConditionDegraded)).To(BeTrue())
	})
})
