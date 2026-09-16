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

	// ErrClusterAlreadyInitialized is returned when a node is told to initialize
	// or join a cluster it is already a member of. Technitium reports both the
	// "already a primary" and "already joined" cases with the same message, so a
	// single sentinel covers idempotent init and initJoin.
	ErrClusterAlreadyInitialized = errors.New("technitium: cluster already initialized")
	// ErrClusterNotInitialized is returned when an operation requires an
	// initialized cluster that is not yet formed, for example a secondary trying
	// to join a primary that has not run init.
	ErrClusterNotInitialized = errors.New("technitium: cluster not initialized")
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
// text is the only signal available to distinguish them.
func classifyStatus(status, message string) error {
	switch status {
	case "ok":
		return nil
	case "invalid-token":
		return ErrInvalidToken
	}

	lower := strings.ToLower(message)
	switch {
	case strings.Contains(lower, "already initialized"):
		return fmt.Errorf("%w: %s", ErrClusterAlreadyInitialized, message)
	case strings.Contains(lower, "cluster is not initialized"),
		strings.Contains(lower, "does not have a cluster initialized"):
		return fmt.Errorf("%w: %s", ErrClusterNotInitialized, message)
	case strings.Contains(lower, "already exists"):
		return fmt.Errorf("%w: %s", ErrZoneAlreadyExists, message)
	case strings.Contains(lower, "no such zone"),
		strings.Contains(lower, "not found"),
		strings.Contains(lower, "does not exist"):
		return fmt.Errorf("%w: %s", ErrZoneNotFound, message)
	default:
		return &APIError{Status: status, Message: message}
	}
}
