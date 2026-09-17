/*
Copyright (c) 2026 Chris

Licensed under the MIT License. See the LICENSE file in the project root for
the full license text.
*/

package technitium

import (
	"context"
	"net/url"
	"strconv"
	"strings"
)

// DNSSettings is the subset of /api/settings/get the operator reconciles. The
// server returns a much larger object (transport ports, proxy, QPM limits,
// DNSSEC, ...); only the forwarding, recursion, cache, logging, and blocking
// fields the ServerSettings and Blocklist CRDs manage are decoded here. Array
// fields (forwarders, recursionNetworkACL, blockListUrls) come back as JSON
// string arrays even though /api/settings/set takes them comma separated.
type DNSSettings struct {
	Forwarders          []string `json:"forwarders"`
	ForwarderProtocol   string   `json:"forwarderProtocol"`
	Recursion           string   `json:"recursion"`
	RecursionNetworkACL []string `json:"recursionNetworkACL"`

	ServeStale            bool  `json:"serveStale"`
	ServeStaleTTL         int32 `json:"serveStaleTtl"`
	CacheMaximumRecordTTL int32 `json:"cacheMaximumRecordTtl"`
	CacheMinimumRecordTTL int32 `json:"cacheMinimumRecordTtl"`

	EnableLogging  bool  `json:"enableLogging"`
	LogQueries     bool  `json:"logQueries"`
	UseLocalTime   bool  `json:"useLocalTime"`
	MaxLogFileDays int32 `json:"maxLogFileDays"`

	EnableBlocking               bool     `json:"enableBlocking"`
	BlockingType                 string   `json:"blockingType"`
	BlockListURLs                []string `json:"blockListUrls"`
	BlockListUpdateIntervalHours int32    `json:"blockListUpdateIntervalHours"`
}

// SetDNSSettingsOptions describes a mutation to the server's DNS settings. Every
// field is a pointer so a nil leaves that setting untouched: /api/settings/set
// only changes the parameters actually present in the request. It carries the
// fields for every settings-backed CRD (ServerSettings owns forwarding,
// recursion, cache, and logging; Blocklist owns the blocking fields) so the
// transport layer maps each setting to its query parameter exactly once.
type SetDNSSettingsOptions struct {
	// Forwarders replaces the forwarder list. A non-nil pointer to an empty
	// slice removes all forwarders (sent as the literal "false" the API expects
	// for removal), returning the server to self recursion.
	Forwarders          *[]string
	ForwarderProtocol   *string
	Recursion           *string
	RecursionNetworkACL *[]string

	ServeStale            *bool
	ServeStaleTTL         *int32
	CacheMaximumRecordTTL *int32
	CacheMinimumRecordTTL *int32

	EnableLogging  *bool
	LogQueries     *bool
	UseLocalTime   *bool
	MaxLogFileDays *int32

	// BlockListURLs replaces the block list URL set; a non-nil pointer to an
	// empty slice clears it (see setList for how removal is wired).
	EnableBlocking               *bool
	BlockingType                 *string
	BlockListURLs                *[]string
	BlockListUpdateIntervalHours *int32
}

// GetDNSSettings fetches the server's current DNS settings.
func (c *Client) GetDNSSettings(ctx context.Context) (*DNSSettings, error) {
	var settings DNSSettings
	if err := c.do(ctx, "/api/settings/get", nil, &settings); err != nil {
		return nil, err
	}
	return &settings, nil
}

// SetDNSSettings applies the non-nil fields of opts. Calling it with no fields
// set is a no-op request the server still accepts.
func (c *Client) SetDNSSettings(ctx context.Context, opts SetDNSSettingsOptions) error {
	params := url.Values{}

	setList := func(key string, v *[]string) {
		if v == nil {
			return
		}
		// The API removes an existing list when the parameter is the literal
		// "false" rather than an empty value, which it would otherwise ignore.
		if len(*v) == 0 {
			params.Set(key, "false")
			return
		}
		params.Set(key, strings.Join(*v, ","))
	}
	setStr := func(key string, v *string) {
		if v != nil {
			params.Set(key, *v)
		}
	}
	setBool := func(key string, v *bool) {
		if v != nil {
			params.Set(key, strconv.FormatBool(*v))
		}
	}
	setInt := func(key string, v *int32) {
		if v != nil {
			params.Set(key, strconv.Itoa(int(*v)))
		}
	}

	setList("forwarders", opts.Forwarders)
	setStr("forwarderProtocol", opts.ForwarderProtocol)
	setStr("recursion", opts.Recursion)
	setList("recursionNetworkACL", opts.RecursionNetworkACL)

	setBool("serveStale", opts.ServeStale)
	setInt("serveStaleTtl", opts.ServeStaleTTL)
	setInt("cacheMaximumRecordTtl", opts.CacheMaximumRecordTTL)
	setInt("cacheMinimumRecordTtl", opts.CacheMinimumRecordTTL)

	setBool("enableLogging", opts.EnableLogging)
	setBool("logQueries", opts.LogQueries)
	setBool("useLocalTime", opts.UseLocalTime)
	setInt("maxLogFileDays", opts.MaxLogFileDays)

	setBool("enableBlocking", opts.EnableBlocking)
	setStr("blockingType", opts.BlockingType)
	setList("blockListUrls", opts.BlockListURLs)
	setInt("blockListUpdateIntervalHours", opts.BlockListUpdateIntervalHours)

	return c.do(ctx, "/api/settings/set", params, nil)
}

// ForceUpdateBlockLists triggers an immediate download and rebuild of the block
// list zone from the configured blockListUrls, instead of waiting for the next
// scheduled update. Technitium syncs this across cluster nodes automatically.
func (c *Client) ForceUpdateBlockLists(ctx context.Context) error {
	return c.do(ctx, "/api/settings/forceUpdateBlockLists", nil, nil)
}
