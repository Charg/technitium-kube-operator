/*
Copyright (c) 2026 Chris

Licensed under the MIT License. See the LICENSE file in the project root for
the full license text.
*/

// Package config loads and validates the operator's connection settings for a
// Technitium DNS Server: the server endpoint and the Kubernetes Secret holding
// its credentials. Credentials never live in a custom resource.
package config

import (
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"
)

const (
	envURL             = "TECHNITIUM_URL"
	envSecretName      = "TECHNITIUM_CREDENTIALS_SECRET"
	envSecretNamespace = "TECHNITIUM_CREDENTIALS_NAMESPACE"
	envInsecure        = "TECHNITIUM_INSECURE_SKIP_VERIFY"
	// envPodNamespace is the conventional downward-API variable naming the pod's
	// own namespace. It is the default location of the credentials Secret.
	envPodNamespace = "POD_NAMESPACE"
)

// Config holds the operator's connection settings for reaching a Technitium DNS
// Server and locating the Secret that carries its credentials.
type Config struct {
	// TechnitiumURL is the base URL of the server, for example
	// https://dns.internal:5380.
	TechnitiumURL string
	// SecretName is the name of the credentials Secret.
	SecretName string
	// SecretNamespace is the namespace of the credentials Secret. It defaults to
	// the operator's own namespace.
	SecretNamespace string
	// InsecureSkipVerify disables TLS verification for servers presenting
	// self-signed certificates.
	InsecureSkipVerify bool
}

// BindFlags registers the connection flags on fs, seeding each default from its
// environment variable so that a command-line flag overrides the environment.
func (c *Config) BindFlags(fs *flag.FlagSet) {
	fs.StringVar(&c.TechnitiumURL, "technitium-url", os.Getenv(envURL),
		"Base URL of the Technitium DNS Server, for example https://dns.internal:5380 (env "+envURL+").")
	fs.StringVar(&c.SecretName, "technitium-credentials-secret", os.Getenv(envSecretName),
		"Name of the Secret holding Technitium credentials (env "+envSecretName+").")
	fs.StringVar(&c.SecretNamespace, "technitium-credentials-namespace", secretNamespaceDefault(),
		"Namespace of the credentials Secret. Defaults to the operator's own namespace "+
			"(env "+envSecretNamespace+", falling back to "+envPodNamespace+").")
	fs.BoolVar(&c.InsecureSkipVerify, "technitium-insecure-skip-verify", boolEnv(envInsecure),
		"Skip TLS certificate verification when connecting to Technitium (env "+envInsecure+").")
}

// Validate reports every missing required setting at once, so an operator fixes
// the whole configuration in one pass rather than one variable per restart.
func (c *Config) Validate() error {
	var missing []string
	if c.TechnitiumURL == "" {
		missing = append(missing, "server URL (--technitium-url / "+envURL+")")
	}
	if c.SecretName == "" {
		missing = append(missing, "credentials Secret name (--technitium-credentials-secret / "+envSecretName+")")
	}
	if c.SecretNamespace == "" {
		missing = append(missing, "credentials Secret namespace "+
			"(--technitium-credentials-namespace / "+envSecretNamespace+" / "+envPodNamespace+")")
	}
	if len(missing) > 0 {
		return fmt.Errorf("technitium connection is not configured: missing %s", strings.Join(missing, ", "))
	}
	return nil
}

// secretNamespaceDefault resolves the default Secret namespace from the explicit
// override first, then the pod's own namespace.
func secretNamespaceDefault() string {
	if ns := os.Getenv(envSecretNamespace); ns != "" {
		return ns
	}
	return os.Getenv(envPodNamespace)
}

// boolEnv parses a boolean environment variable, treating unset or unparseable
// values as false.
func boolEnv(key string) bool {
	v, err := strconv.ParseBool(os.Getenv(key))
	if err != nil {
		return false
	}
	return v
}
