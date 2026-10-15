/*
Copyright (c) 2026 Chris

Licensed under the MIT License. See the LICENSE file in the project root for
the full license text.
*/

package externaldns

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	dnsv1alpha1 "github.com/charg/technitium-operator/api/v1alpha1"
)

const maxBodyBytes = 16 << 20

// Server serves the ExternalDNS webhook API on top of a Kubernetes client.
type Server struct {
	Client client.Client
	T      Translator
}

// Handler returns the webhook API mux.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", s.negotiate)
	mux.HandleFunc("GET /records", s.records)
	mux.HandleFunc("POST /records", s.applyChanges)
	mux.HandleFunc("POST /adjustendpoints", s.adjust)
	return mux
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", MediaType)
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(v)
}

func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes)).Decode(v); err != nil {
		code := http.StatusBadRequest
		if _, ok := errors.AsType[*http.MaxBytesError](err); ok {
			code = http.StatusRequestEntityTooLarge
		}
		http.Error(w, err.Error(), code)
		return false
	}
	return true
}

func (s *Server) negotiate(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, DomainFilter{Include: s.T.Zones})
}

func (s *Server) records(w http.ResponseWriter, r *http.Request) {
	var list dnsv1alpha1.RecordList
	if err := s.Client.List(r.Context(), &list, client.InNamespace(s.T.Namespace),
		client.MatchingLabels(s.T.Selector())); err != nil {
		logf.FromContext(r.Context()).Error(err, "Failed to list Records")
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, Endpoints(list.Items))
}

func (s *Server) adjust(w http.ResponseWriter, r *http.Request) {
	var in []*Endpoint
	if !decode(w, r, &in) {
		return
	}
	writeJSON(w, s.T.Adjust(in))
}

func (s *Server) applyChanges(w http.ResponseWriter, r *http.Request) {
	var c Changes
	if !decode(w, r, &c) {
		return
	}
	plan, err := s.T.BuildPlan(&c)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := s.apply(r.Context(), plan); err != nil {
		logf.FromContext(r.Context()).Error(err, "Failed to apply changes")
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// apply runs creates, then patches, then deletes, so an updated RRset never
// has a window with fewer records than before.
func (s *Server) apply(ctx context.Context, p *Plan) error {
	for _, rec := range p.Create {
		err := s.Client.Create(ctx, rec)
		if apierrors.IsAlreadyExists(err) {
			err = s.patch(ctx, rec)
		}
		if err != nil {
			return fmt.Errorf("creating Record %s: %w", rec.Name, err)
		}
	}
	for _, rec := range p.Patch {
		if err := s.patch(ctx, rec); err != nil {
			return fmt.Errorf("patching Record %s: %w", rec.Name, err)
		}
	}
	for _, name := range p.Delete {
		rec := &dnsv1alpha1.Record{}
		rec.Name, rec.Namespace = name, s.T.Namespace
		if err := s.Client.Delete(ctx, rec); err != nil && !apierrors.IsNotFound(err) {
			return fmt.Errorf("deleting Record %s: %w", name, err)
		}
	}
	return nil
}

func (s *Server) patch(ctx context.Context, want *dnsv1alpha1.Record) error {
	cur := &dnsv1alpha1.Record{}
	if err := s.Client.Get(ctx, client.ObjectKeyFromObject(want), cur); err != nil {
		return err
	}
	base := cur.DeepCopy()
	cur.Spec = want.Spec
	cur.Labels = want.Labels
	cur.Annotations = want.Annotations
	return s.Client.Patch(ctx, cur, client.MergeFrom(base))
}
