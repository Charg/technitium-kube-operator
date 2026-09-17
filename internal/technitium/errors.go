/*
Copyright (c) 2026 Chris

Licensed under the MIT License. See the LICENSE file in the project root for
the full license text.
*/

package technitium

import (
	"errors"
	"fmt"
	"strings"
)

// Sentinel errors let callers make reconciliation idempotent: creating a zone
// that already exists or deleting one that is gone are both no-ops rather than
// failures. Match them with errors.Is.
var (
	ErrZoneAlreadyExists = errors.New("technitium: zone already exists")
	ErrZoneNotFound      = errors.New("technitium: zone not found")
	ErrInvalidToken      = errors.New("technitium: invalid or expired token")
	// ErrClusterAlreadyInitialized means the node init/initJoin was called
	// against already has a cluster (as Primary or Secondary). Re-running
	// either call on such a node is the expected steady state once clustering
	// has converged, so callers treat it as success.
	ErrClusterAlreadyInitialized = errors.New("technitium: cluster already initialized")
	// ErrRecordAlreadyExists and ErrRecordNotFound are the record-scoped
	// counterparts of the zone sentinels above, used by AddRecord/DeleteRecord
	// so record reconciliation can be idempotent the same way zone
	// reconciliation is.
	ErrRecordAlreadyExists = errors.New("technitium: record already exists")
	ErrRecordNotFound      = errors.New("technitium: record not found")
)

// APIError carries a Technitium error response that does not map to a sentinel.
type APIError struct {
	// Status is the value of the response "status" field, for example "error".
	Status string
	// Message is the server-supplied errorMessage, empty if none was returned.
	Message string
	// HTTPStatus is the HTTP status code, set when the body was not a valid
	// Technitium envelope.
	HTTPStatus int
}

func (e *APIError) Error() string {
	switch {
	case e.Message != "":
		return fmt.Sprintf("technitium: API returned status %q: %s", e.Status, e.Message)
	case e.HTTPStatus != 0:
		return fmt.Sprintf("technitium: unexpected HTTP %d response", e.HTTPStatus)
	default:
		return fmt.Sprintf("technitium: API returned status %q", e.Status)
	}
}

// classifyStatus maps a decoded response envelope to an error. Technitium does
// not use distinct status codes for "already exists" and "not found"; both come
// back as status "error" with a human-readable errorMessage, so the message
// text is the only signal available to distinguish them. The same phrasing is
// used for both zones and records ("Zone already exists" vs "Record already
// exists"), so the message is checked a second time for "record" to pick the
// sentinel pair that matches the resource actually being classified.
func classifyStatus(status, message string) error {
	switch status {
	case "ok":
		return nil
	case "invalid-token":
		return ErrInvalidToken
	}

	lower := strings.ToLower(message)
	isRecord := strings.Contains(lower, "record")
	switch {
	case strings.Contains(lower, "already initialized"):
		return fmt.Errorf("%w: %s", ErrClusterAlreadyInitialized, message)
	case strings.Contains(lower, "already exists"):
		if isRecord {
			return fmt.Errorf("%w: %s", ErrRecordAlreadyExists, message)
		}
		return fmt.Errorf("%w: %s", ErrZoneAlreadyExists, message)
	case strings.Contains(lower, "no such zone"),
		strings.Contains(lower, "no such record"),
		strings.Contains(lower, "not found"),
		strings.Contains(lower, "does not exist"):
		if isRecord {
			return fmt.Errorf("%w: %s", ErrRecordNotFound, message)
		}
		return fmt.Errorf("%w: %s", ErrZoneNotFound, message)
	default:
		return &APIError{Status: status, Message: message}
	}
}
