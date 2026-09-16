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

// UsernamePasswordFromSecret extracts the username and password from a
// credentials Secret. It reports ok=false when no username is present, for
// example a token-only Secret. Joining a secondary node needs the primary's
// username and password, which a token cannot substitute for.
func UsernamePasswordFromSecret(secret *corev1.Secret) (username, password string, ok bool) {
	if secret == nil {
		return "", "", false
	}
	username = strings.TrimSpace(string(secret.Data[secretKeyUsername]))
	password = string(secret.Data[secretKeyPassword])
	return username, password, username != ""
}
