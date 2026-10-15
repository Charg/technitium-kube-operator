/*
Copyright (c) 2026 Chris

Licensed under the MIT License. See the LICENSE file in the project root for
the full license text.
*/

package externaldns

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	dnsv1alpha1 "github.com/charg/technitium-operator/api/v1alpha1"
)

const namespaceFile = "/var/run/secrets/kubernetes.io/serviceaccount/namespace"

type stringList []string

func (s *stringList) String() string     { return strings.Join(*s, ",") }
func (s *stringList) Set(v string) error { *s = append(*s, v); return nil }

// Config is the parsed command line of the external-dns-webhook subcommand.
type Config struct {
	ServerRef    string
	Zones        []string
	Namespace    string
	OwnerID      string
	Listen       string
	HealthListen string
}

func defaultNamespace() string {
	if ns := os.Getenv("POD_NAMESPACE"); ns != "" {
		return ns
	}
	if b, err := os.ReadFile(namespaceFile); err == nil {
		return strings.TrimSpace(string(b))
	}
	return ""
}

// ParseFlags parses the subcommand arguments (without the subcommand name).
// opts may be nil.
func ParseFlags(args []string, opts *zap.Options) (*Config, error) {
	fs := flag.NewFlagSet("external-dns-webhook", flag.ContinueOnError)
	cfg := &Config{}
	var zones stringList
	fs.StringVar(&cfg.ServerRef, "server-ref", "", "Name of the TechnitiumCluster Records are created for (required).")
	fs.Var(&zones, "zone", "Zone the webhook manages; repeat for several zones (at least one).")
	fs.StringVar(&cfg.Namespace, "namespace", defaultNamespace(),
		"Namespace Records are created in. Defaults to POD_NAMESPACE, then the ServiceAccount namespace.")
	fs.StringVar(&cfg.OwnerID, "owner-id", "default", "Identifier marking Records owned by this webhook.")
	fs.StringVar(&cfg.Listen, "listen", "127.0.0.1:8888", "Address of the webhook API ExternalDNS calls.")
	fs.StringVar(&cfg.HealthListen, "health-listen", ":8080", "Address serving GET /healthz.")
	if opts != nil {
		opts.BindFlags(fs)
	}
	if err := fs.Parse(args); err != nil {
		return nil, err
	}
	cfg.Zones = zones
	switch {
	case cfg.ServerRef == "":
		return nil, errors.New("--server-ref is required")
	case len(cfg.Zones) == 0:
		return nil, errors.New("at least one --zone is required")
	case cfg.Namespace == "":
		return nil, errors.New("--namespace is required when it cannot be detected")
	}
	for i, z := range cfg.Zones {
		cfg.Zones[i] = normalizeName(z)
	}
	return cfg, nil
}

// Run starts the webhook and blocks until ctx is cancelled.
func Run(ctx context.Context, args []string) error {
	opts := zap.Options{Development: true}
	cfg, err := ParseFlags(args, &opts)
	if err != nil {
		return err
	}
	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))
	log := ctrl.Log.WithName("external-dns-webhook")

	scheme := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(dnsv1alpha1.AddToScheme(scheme))
	c, err := client.New(ctrl.GetConfigOrDie(), client.Options{Scheme: scheme})
	if err != nil {
		return fmt.Errorf("building Kubernetes client: %w", err)
	}

	srv := &Server{Client: c, T: Translator{
		Namespace: cfg.Namespace, ServerRef: cfg.ServerRef, OwnerID: cfg.OwnerID, Zones: cfg.Zones,
	}}
	health := http.NewServeMux()
	health.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })

	servers := []*http.Server{
		{Addr: cfg.Listen, Handler: srv.Handler(), ReadHeaderTimeout: 10 * time.Second},
		{Addr: cfg.HealthListen, Handler: health, ReadHeaderTimeout: 10 * time.Second},
	}
	errCh := make(chan error, len(servers))
	for _, s := range servers {
		go func() {
			log.Info("Listening", "address", s.Addr)
			errCh <- s.ListenAndServe()
		}()
	}
	select {
	case err = <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
	case <-ctx.Done():
	}
	log.Info("Shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for _, s := range servers {
		_ = s.Shutdown(shutdownCtx)
	}
	return err
}
