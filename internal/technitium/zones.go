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
	"context"
	"errors"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// listPageSize is the page size requested from /api/zones/list. Technitium
// paginates zone listings; a large page keeps the number of round trips low.
const listPageSize = 250

// CreateZoneOptions describes a zone to create. Fields map to the query
// parameters of /api/zones/create. Type values (Primary, Secondary, Stub,
// Forwarder, Catalog) and Protocol values (Udp, Tcp, Tls, Https, Quic) are
// passed through verbatim; the caller is responsible for supplying values the
// server accepts.
type CreateZoneOptions struct {
	// Zone is the fully qualified zone name, required.
	Zone string
	// Type is the zone category. Empty defers to the server default (Primary).
	Type string
	// Catalog is the name of a catalog zone this zone joins as a member.
	Catalog string
	// Forwarder is the upstream resolver address for a Forwarder zone, or the
	// special value "this-server".
	Forwarder string
	// Protocol is the transport used to reach the Forwarder.
	Protocol string
	// PrimaryNameServerAddresses lists the primary name server addresses used by
	// Secondary and Stub zones.
	PrimaryNameServerAddresses []string
}

// ZoneOptionsUpdate describes a mutation to an existing zone. Nil fields are
// left unchanged. Fields map to the query parameters of /api/zones/options/set.
type ZoneOptionsUpdate struct {
	// Catalog reassigns catalog membership. A pointer to the empty string
	// removes the zone from its catalog.
	Catalog *string
	// Disabled enables or disables the zone.
	Disabled *bool
}

// ZoneInfo is a single entry from /api/zones/list.
type ZoneInfo struct {
	Name         string    `json:"name"`
	Type         string    `json:"type"`
	Internal     bool      `json:"internal"`
	DNSSECStatus string    `json:"dnssecStatus"`
	SOASerial    uint32    `json:"soaSerial"`
	LastModified time.Time `json:"lastModified"`
	Disabled     bool      `json:"disabled"`
}

// ZoneOptions is the subset of /api/zones/options/get relevant to reconciling a
// zone.
type ZoneOptions struct {
	Name         string `json:"name"`
	Type         string `json:"type"`
	Internal     bool   `json:"internal"`
	DNSSECStatus string `json:"dnssecStatus"`
	Disabled     bool   `json:"disabled"`
	Catalog      string `json:"catalog"`
}

// zoneParams builds a query carrying the required zone name, or an error when
// the name is empty. Every zone-scoped endpoint starts from this.
func zoneParams(zone string) (url.Values, error) {
	if zone == "" {
		return nil, errors.New("technitium: zone name is required")
	}
	params := url.Values{}
	params.Set("zone", zone)
	return params, nil
}

// CreateZone creates a zone on the server. It returns ErrZoneAlreadyExists when
// the zone is already present, letting callers treat creation as idempotent.
func (c *Client) CreateZone(ctx context.Context, opts CreateZoneOptions) error {
	params, err := zoneParams(opts.Zone)
	if err != nil {
		return err
	}

	if opts.Type != "" {
		params.Set("type", opts.Type)
	}
	if opts.Catalog != "" {
		params.Set("catalog", opts.Catalog)
	}
	if opts.Forwarder != "" {
		params.Set("forwarder", opts.Forwarder)
	}
	if opts.Protocol != "" {
		params.Set("protocol", opts.Protocol)
	}
	if len(opts.PrimaryNameServerAddresses) > 0 {
		params.Set("primaryNameServerAddresses", strings.Join(opts.PrimaryNameServerAddresses, ","))
	}

	return c.do(ctx, "/api/zones/create", params, nil)
}

// DeleteZone removes a zone from the server. It returns ErrZoneNotFound when the
// zone does not exist, letting callers treat deletion as idempotent.
func (c *Client) DeleteZone(ctx context.Context, zone string) error {
	params, err := zoneParams(zone)
	if err != nil {
		return err
	}

	return c.do(ctx, "/api/zones/delete", params, nil)
}

// GetZoneOptions fetches the options of a single zone. It returns
// ErrZoneNotFound when the zone does not exist.
func (c *Client) GetZoneOptions(ctx context.Context, zone string) (*ZoneOptions, error) {
	params, err := zoneParams(zone)
	if err != nil {
		return nil, err
	}

	var opts ZoneOptions
	if err := c.do(ctx, "/api/zones/options/get", params, &opts); err != nil {
		return nil, err
	}
	return &opts, nil
}

// SetZoneOptions updates an existing zone. It returns ErrZoneNotFound when the
// zone does not exist. Calling it with no fields set is a no-op request that
// still validates the zone exists.
func (c *Client) SetZoneOptions(ctx context.Context, zone string, opts ZoneOptionsUpdate) error {
	params, err := zoneParams(zone)
	if err != nil {
		return err
	}

	if opts.Catalog != nil {
		params.Set("catalog", *opts.Catalog)
	}
	if opts.Disabled != nil {
		params.Set("disabled", strconv.FormatBool(*opts.Disabled))
	}

	return c.do(ctx, "/api/zones/options/set", params, nil)
}

// ListZones returns every zone on the server, following pagination.
func (c *Client) ListZones(ctx context.Context) ([]ZoneInfo, error) {
	var all []ZoneInfo

	for page := 1; ; page++ {
		params := url.Values{}
		params.Set("pageNumber", strconv.Itoa(page))
		params.Set("zonesPerPage", strconv.Itoa(listPageSize))

		var resp struct {
			PageNumber int        `json:"pageNumber"`
			TotalPages int        `json:"totalPages"`
			TotalZones int        `json:"totalZones"`
			Zones      []ZoneInfo `json:"zones"`
		}
		if err := c.do(ctx, "/api/zones/list", params, &resp); err != nil {
			return nil, err
		}

		all = append(all, resp.Zones...)
		if len(resp.Zones) == 0 || resp.PageNumber >= resp.TotalPages {
			break
		}
	}

	return all, nil
}
