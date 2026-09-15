/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
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
