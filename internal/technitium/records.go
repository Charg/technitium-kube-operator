/*
Copyright (c) 2026 Chris

Licensed under the MIT License. See the LICENSE file in the project root for
the full license text.
*/

package technitium

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strconv"
)

// RecordValue carries a record's primary rdata value, spread across one field
// per supported type. Only the field matching the record's type is read; the
// caller is responsible for populating the right one (see setRecordValueParams).
// It is embedded in AddRecordOptions, UpdateRecordOptions, and
// DeleteRecordOptions so the type-to-parameter mapping is written once instead
// of once per options struct.
type RecordValue struct {
	// IPAddress is the value for A and AAAA records.
	IPAddress string
	// CName is the value for CNAME records.
	CName string
	// Text is the value for TXT records.
	Text string
	// NameServer is the value for NS records.
	NameServer string
	// PtrName is the value for PTR records.
	PtrName string
	// Exchange is the mail exchange domain name for MX records.
	Exchange string
	// Preference is the preference value for MX records. Nil lets the server
	// use its own default.
	Preference *int32
}

// AddRecordOptions describes a record to add. Fields map to the query
// parameters of /api/zones/records/add.
type AddRecordOptions struct {
	// Zone is the authoritative zone to add the record into. Empty defers to
	// the closest authoritative zone for Domain, per the Technitium API.
	Zone string
	// Domain is the record's fully qualified domain name, required.
	Domain string
	// Type is the DNS resource record type, required.
	Type string
	// TTL is the record TTL in seconds. Nil defers to the server default.
	TTL *int32

	RecordValue
}

// UpdateRecordOptions describes a TTL correction to an existing record.
//
// Only TTL is supported here: Technitium's update endpoint identifies the
// record being edited by its current rdata and changes fields via a parallel
// "new" parameter per type (newIpAddress, newCName, newExchange+newPreference,
// ...). Modeling every type's current/new pair would roughly double
// RecordValue's surface for a path only used to correct rdata drift, and
// reconcileRecord already has a simpler way to do that: delete the record and
// add the desired one back, reusing the same builders the happy path already
// exercises. TTL is the one field worth updating in place, since it changes
// with no effect on how the record resolves.
type UpdateRecordOptions struct {
	// Zone is the authoritative zone the record lives in.
	Zone string
	// Domain is the record's fully qualified domain name, required.
	Domain string
	// Type is the DNS resource record type, required.
	Type string
	// TTL is the new TTL in seconds. Unlike Add/Delete, Technitium's update
	// endpoint defaults a missing ttl to 3600 rather than leaving it
	// unchanged, so callers must always set this explicitly.
	TTL int32

	// RecordValue identifies which existing record to update: Technitium
	// matches the update to the record whose current rdata equals these
	// values. Leaving the "new" parameters unset keeps the rdata unchanged.
	RecordValue
}

// DeleteRecordOptions identifies a record to remove. Fields map to the query
// parameters of /api/zones/records/delete.
type DeleteRecordOptions struct {
	// Zone is the authoritative zone the record lives in.
	Zone string
	// Domain is the record's fully qualified domain name, required.
	Domain string
	// Type is the DNS resource record type, required.
	Type string

	RecordValue
}

// RecordData is the rdata payload of a Record returned by
// /api/zones/records/get. Only the field matching Record.Type is populated.
// The JSON key names mirror the request parameter names Technitium uses for
// the same value (confirmed for ipAddress, cname, and nameServer against the
// Get Records example in APIDOCS.md; text/ptrName/exchange/preference follow
// the same naming convention used throughout the add/update/delete record
// parameters).
type RecordData struct {
	IPAddress  string `json:"ipAddress,omitempty"`
	CName      string `json:"cname,omitempty"`
	Text       string `json:"text,omitempty"`
	NameServer string `json:"nameServer,omitempty"`
	PtrName    string `json:"ptrName,omitempty"`
	Exchange   string `json:"exchange,omitempty"`
	Preference int32  `json:"preference,omitempty"`
}

// Record is a single entry from /api/zones/records/get.
type Record struct {
	Name     string     `json:"name"`
	Type     string     `json:"type"`
	TTL      int32      `json:"ttl"`
	Disabled bool       `json:"disabled"`
	RData    RecordData `json:"rData"`
}

// setRecordValueParams sets the query parameter(s) that carry a record's
// rdata, keyed by DNS record type. MX is the only supported type needing two
// parameters (exchange + preference); every other supported type needs
// exactly one. Returns an error for a type this client does not know how to
// build a request for, or one whose required value is missing.
func setRecordValueParams(params url.Values, recordType string, v RecordValue) error {
	switch recordType {
	case "A", "AAAA":
		if v.IPAddress == "" {
			return fmt.Errorf("technitium: ipAddress is required for %s records", recordType)
		}
		params.Set("ipAddress", v.IPAddress)
	case "CNAME":
		if v.CName == "" {
			return errors.New("technitium: cname is required for CNAME records")
		}
		params.Set("cname", v.CName)
	case "TXT":
		if v.Text == "" {
			return errors.New("technitium: text is required for TXT records")
		}
		params.Set("text", v.Text)
	case "NS":
		if v.NameServer == "" {
			return errors.New("technitium: nameServer is required for NS records")
		}
		params.Set("nameServer", v.NameServer)
	case "PTR":
		if v.PtrName == "" {
			return errors.New("technitium: ptrName is required for PTR records")
		}
		params.Set("ptrName", v.PtrName)
	case "MX":
		if v.Exchange == "" {
			return errors.New("technitium: exchange is required for MX records")
		}
		params.Set("exchange", v.Exchange)
		if v.Preference != nil {
			params.Set("preference", strconv.Itoa(int(*v.Preference)))
		}
	default:
		return fmt.Errorf("technitium: unsupported record type %q", recordType)
	}
	return nil
}

// recordParams builds the query carrying the required domain and type, or an
// error when either is empty. Every record-scoped endpoint starts from this.
func recordParams(zone, domain, recordType string) (url.Values, error) {
	if domain == "" {
		return nil, errors.New("technitium: domain is required")
	}
	if recordType == "" {
		return nil, errors.New("technitium: type is required")
	}

	params := url.Values{}
	if zone != "" {
		params.Set("zone", zone)
	}
	params.Set("domain", domain)
	params.Set("type", recordType)
	return params, nil
}

// AddRecord creates a record on the server. It returns ErrRecordAlreadyExists
// when an identical record is already present, letting callers treat creation
// as idempotent.
func (c *Client) AddRecord(ctx context.Context, opts AddRecordOptions) error {
	params, err := recordParams(opts.Zone, opts.Domain, opts.Type)
	if err != nil {
		return err
	}

	if opts.TTL != nil {
		params.Set("ttl", strconv.Itoa(int(*opts.TTL)))
	}
	if err := setRecordValueParams(params, opts.Type, opts.RecordValue); err != nil {
		return err
	}

	return c.do(ctx, "/api/zones/records/add", params, nil)
}

// GetRecords fetches the records at domain within zone. listZone is always
// false: a Record resource manages one record at one domain name, so listing
// the whole zone would return unrelated records (SOA, other names, DNSSEC
// metadata) the caller has no use for.
func (c *Client) GetRecords(ctx context.Context, zone, domain string) ([]Record, error) {
	if domain == "" {
		return nil, errors.New("technitium: domain is required")
	}

	params := url.Values{}
	if zone != "" {
		params.Set("zone", zone)
	}
	params.Set("domain", domain)
	params.Set("listZone", "false")

	var resp struct {
		Records []Record `json:"records"`
	}
	if err := c.do(ctx, "/api/zones/records/get", params, &resp); err != nil {
		return nil, err
	}
	return resp.Records, nil
}

// UpdateRecord corrects a record's TTL in place. See UpdateRecordOptions for
// why this client does not expose a general-purpose rdata update.
func (c *Client) UpdateRecord(ctx context.Context, opts UpdateRecordOptions) error {
	params, err := recordParams(opts.Zone, opts.Domain, opts.Type)
	if err != nil {
		return err
	}

	// Unlike Add/Delete, Technitium's update endpoint defaults a missing ttl to
	// 3600 rather than leaving the current value untouched, so it is always
	// sent explicitly here.
	params.Set("ttl", strconv.Itoa(int(opts.TTL)))
	if err := setRecordValueParams(params, opts.Type, opts.RecordValue); err != nil {
		return err
	}

	return c.do(ctx, "/api/zones/records/update", params, nil)
}

// DeleteRecord removes a record from the server. It returns ErrRecordNotFound
// when the record does not exist, letting callers treat deletion as
// idempotent.
func (c *Client) DeleteRecord(ctx context.Context, opts DeleteRecordOptions) error {
	params, err := recordParams(opts.Zone, opts.Domain, opts.Type)
	if err != nil {
		return err
	}

	if err := setRecordValueParams(params, opts.Type, opts.RecordValue); err != nil {
		return err
	}

	return c.do(ctx, "/api/zones/records/delete", params, nil)
}
