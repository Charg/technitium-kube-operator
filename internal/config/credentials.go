/*
Copyright (c) 2026 Chris

Licensed under the MIT License. See the LICENSE file in the project root for
the full license text.
*/

package config

import (
	"errors"
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"

	"github.com/charg/technitium-operator/internal/technitium"
)

const (
	secretKeyToken    = "token"
	secretKeyUsername = "username"
	secretKeyPassword = "password"
)

// ClientOptionsFromSecret derives the Technitium client authentication options
// from a credentials Secret. It accepts either a pre-created API token under the
// "token" key, or a "username"/"password" pair exchanged for a token at login.
// A token takes precedence when both are present.
func ClientOptionsFromSecret(secret *corev1.Secret) ([]technitium.Option, error) {
	if secret == nil {
		return nil, errors.New("technitium: credentials secret is nil")
	}

	// Trim surrounding whitespace: `kubectl create secret --from-literal` and
	// piped `--from-file` both routinely leave a trailing newline.
	token := strings.TrimSpace(string(secret.Data[secretKeyToken]))
	username := strings.TrimSpace(string(secret.Data[secretKeyUsername]))
	password := string(secret.Data[secretKeyPassword])

	switch {
	case token != "":
		return []technitium.Option{technitium.WithToken(token)}, nil
	case username != "":
		return []technitium.Option{technitium.WithCredentials(username, password)}, nil
	default:
		return nil, fmt.Errorf("technitium: credentials secret %q must contain either a %q key, or %q and %q keys",
			secret.Name, secretKeyToken, secretKeyUsername, secretKeyPassword)
	}
}

// UsernamePasswordFromSecret reads the local admin username/password out of a
// credentials Secret. Unlike ClientOptionsFromSecret, it never accepts a
// token: cluster init/initJoin authenticate with the plain admin login
// itself, there being no notion of a per-node API token before a node has
// even joined a cluster.
func UsernamePasswordFromSecret(secret *corev1.Secret) (username, password string, err error) {
	if secret == nil {
		return "", "", errors.New("technitium: credentials secret is nil")
	}

	// Trim the username the same way ClientOptionsFromSecret does; the
	// password is left untrimmed since a trailing character there could be
	// part of the actual admin password rather than shell/file artifact.
	username = strings.TrimSpace(string(secret.Data[secretKeyUsername]))
	password = string(secret.Data[secretKeyPassword])

	if username == "" {
		return "", "", fmt.Errorf("technitium: credentials secret %q has no %q key", secret.Name, secretKeyUsername)
	}
	if password == "" {
		return "", "", fmt.Errorf("technitium: credentials secret %q has no %q key", secret.Name, secretKeyPassword)
	}
	return username, password, nil
}
