/*
Copyright (c) 2026 Chris

Licensed under the MIT License. See the LICENSE file in the project root for
the full license text.
*/

package technitium

import (
	"context"
	"errors"
	"net/url"
)

// The allowed and blocked zones are the server's manual overrides layered on
// top of the downloaded block lists: a domain in the allowed zone is never
// blocked, and a domain in the blocked zone is always blocked. Both are managed
// per domain through /api/allowed/* and /api/blocked/*, which take a single
// `domain` parameter. Add and delete are treated as idempotent by callers, the
// same way record and zone mutations are, so reconciliation can converge
// without first listing existing entries.

// domainParams builds the query carrying the required domain, or an error when
// it is empty. Every allowed and blocked zone endpoint starts from this.
func domainParams(domain string) (url.Values, error) {
	if domain == "" {
		return nil, errors.New("technitium: domain is required")
	}
	params := url.Values{}
	params.Set("domain", domain)
	return params, nil
}

// AddAllowedZone adds a domain to the allowed zone, exempting it from blocking.
func (c *Client) AddAllowedZone(ctx context.Context, domain string) error {
	params, err := domainParams(domain)
	if err != nil {
		return err
	}
	return c.do(ctx, "/api/allowed/add", params, nil)
}

// DeleteAllowedZone removes a domain from the allowed zone.
func (c *Client) DeleteAllowedZone(ctx context.Context, domain string) error {
	params, err := domainParams(domain)
	if err != nil {
		return err
	}
	return c.do(ctx, "/api/allowed/delete", params, nil)
}

// AddBlockedZone adds a domain to the blocked zone, blocking it unconditionally.
func (c *Client) AddBlockedZone(ctx context.Context, domain string) error {
	params, err := domainParams(domain)
	if err != nil {
		return err
	}
	return c.do(ctx, "/api/blocked/add", params, nil)
}

// DeleteBlockedZone removes a domain from the blocked zone.
func (c *Client) DeleteBlockedZone(ctx context.Context, domain string) error {
	params, err := domainParams(domain)
	if err != nil {
		return err
	}
	return c.do(ctx, "/api/blocked/delete", params, nil)
}
