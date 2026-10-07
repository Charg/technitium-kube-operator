/*
Copyright (c) 2026 Chris

Licensed under the MIT License. See the LICENSE file in the project root for
the full license text.
*/

package controller

import (
	"context"
	"fmt"
	"sync"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	dnsv1alpha1 "github.com/charg/technitium-operator/api/v1alpha1"
	"github.com/charg/technitium-operator/internal/config"
	"github.com/charg/technitium-operator/internal/technitium"
)

// cachedServerClient is a built client together with the endpoint and admin
// Secret resourceVersion it was built from; a change to either rebuilds it.
type cachedServerClient struct {
	endpoint              string
	secretResourceVersion string
	api                   *technitium.Client
}

// serverClientCache resolves a serverRef to a Technitium client and holds one
// built client per TechnitiumCluster name, so a routine reconcile does not
// re-login on every pass. The zero value is ready to use.
type serverClientCache struct {
	mu      sync.Mutex
	entries map[string]cachedServerClient
}

// resolve looks up the TechnitiumCluster serverRef names and its admin Secret
// and returns a client for the cluster's Primary. Writes always go to the
// Primary: the client Service load-balances across every replica, and a
// Secondary only holds what it syncs from the Primary.
//
// A missing TechnitiumCluster is wrapped rather than replaced, so
// apierrors.IsNotFound still recognizes it; finalizers depend on that to tell
// "server is gone" apart from any other resolve failure.
func (c *serverClientCache) resolve(ctx context.Context, reader client.Reader, operatorNamespace string, serverRef dnsv1alpha1.SecretReference) (*technitium.Client, error) {
	var tc dnsv1alpha1.TechnitiumCluster
	if err := reader.Get(ctx, client.ObjectKey{Name: serverRef.Name}, &tc); err != nil {
		return nil, fmt.Errorf("getting TechnitiumCluster %q: %w", serverRef.Name, err)
	}

	if tc.Status.PrimaryEndpoint == "" {
		return nil, fmt.Errorf("TechnitiumCluster %q is not ready yet: no primary endpoint reported", serverRef.Name)
	}

	secretNamespace := operatorNamespace
	secretName := adminSecretName(tc.Name)
	if tc.Spec.AdminSecretRef != nil {
		secretName = tc.Spec.AdminSecretRef.Name
		if tc.Spec.AdminSecretRef.Namespace != "" {
			secretNamespace = tc.Spec.AdminSecretRef.Namespace
		}
	}
	secretKey := client.ObjectKey{Namespace: secretNamespace, Name: secretName}

	var secret corev1.Secret
	if err := reader.Get(ctx, secretKey, &secret); err != nil {
		return nil, fmt.Errorf("getting admin secret %s for TechnitiumCluster %q: %w", secretKey, serverRef.Name, err)
	}

	if len(secret.Data[adminSecretTokenKey]) == 0 {
		return nil, fmt.Errorf("TechnitiumCluster %q is not bootstrapped yet: admin secret %s has no token",
			serverRef.Name, secretKey)
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	if entry, ok := c.entries[tc.Name]; ok && entry.endpoint == tc.Status.PrimaryEndpoint &&
		entry.secretResourceVersion == secret.ResourceVersion {
		return entry.api, nil
	}

	opts, err := config.ClientOptionsFromSecret(&secret)
	if err != nil {
		return nil, err
	}

	api, err := technitium.NewClient(tc.Status.PrimaryEndpoint, opts...)
	if err != nil {
		return nil, fmt.Errorf("building Technitium client for %q: %w", serverRef.Name, err)
	}

	if c.entries == nil {
		c.entries = make(map[string]cachedServerClient)
	}
	c.entries[tc.Name] = cachedServerClient{
		endpoint:              tc.Status.PrimaryEndpoint,
		secretResourceVersion: secret.ResourceVersion,
		api:                   api,
	}
	return api, nil
}
