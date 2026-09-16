/*
Copyright (c) 2026 Chris

Licensed under the MIT License. See the LICENSE file in the project root for
the full license text.
*/

// Package technitium is a typed HTTP client for the Technitium DNS Server API.
// It is a pure transport layer with no Kubernetes or controller dependencies:
// callers translate their own domain objects into the request options exposed
// here.
//
// API reference: https://github.com/TechnitiumSoftware/DnsServer/blob/master/APIDOCS.md
package technitium

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const defaultTimeout = 30 * time.Second

// Client talks to a single Technitium DNS Server over its HTTP API. It is safe
// for concurrent use once constructed, except that Login mutates the stored
// token.
type Client struct {
	baseURL    string
	httpClient *http.Client
	token      string
	username   string
	password   string

	timeout            time.Duration
	insecureSkipVerify bool
}

// Option configures a Client. Options are applied in order by NewClient.
type Option func(*Client)

// WithToken authenticates every request with a pre-created API token. Use this
// when the token is provisioned out of band; otherwise supply credentials with
// WithCredentials and call Login.
func WithToken(token string) Option {
	return func(c *Client) { c.token = token }
}

// WithCredentials stores the username and password used by Login to exchange
// for an API token.
func WithCredentials(username, password string) Option {
	return func(c *Client) {
		c.username = username
		c.password = password
	}
}

// WithTimeout sets the per-request timeout. Ignored when WithHTTPClient supplies
// a client. Defaults to 30 seconds.
func WithTimeout(d time.Duration) Option {
	return func(c *Client) { c.timeout = d }
}

// WithInsecureSkipVerify disables TLS certificate verification. Ignored when
// WithHTTPClient supplies a client. Intended for Technitium servers presenting
// self-signed certificates.
func WithInsecureSkipVerify(skip bool) Option {
	return func(c *Client) { c.insecureSkipVerify = skip }
}

// WithHTTPClient supplies a preconfigured HTTP client, taking full control of
// transport, timeout, and TLS settings.
func WithHTTPClient(h *http.Client) Option {
	return func(c *Client) { c.httpClient = h }
}

// NewClient builds a Client for the given base URL, for example
// "https://dns.internal:5380". Options override the defaults.
func NewClient(baseURL string, opts ...Option) (*Client, error) {
	if baseURL == "" {
		return nil, errors.New("technitium: baseURL is required")
	}

	c := &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		timeout: defaultTimeout,
	}
	for _, opt := range opts {
		opt(c)
	}

	if c.httpClient == nil {
		transport := http.DefaultTransport.(*http.Transport).Clone()
		if c.insecureSkipVerify {
			transport.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
		}
		c.httpClient = &http.Client{Timeout: c.timeout, Transport: transport}
	}

	return c, nil
}

// Login exchanges the stored username and password for an API token, stores it
// for subsequent calls, and returns it. Requires WithCredentials.
func (c *Client) Login(ctx context.Context) (string, error) {
	if c.username == "" {
		return "", errors.New("technitium: username is required to log in")
	}

	params := url.Values{}
	params.Set("user", c.username)
	params.Set("pass", c.password)

	body, err := c.doRaw(ctx, "/api/user/login", params)
	if err != nil {
		return "", err
	}

	var out struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return "", fmt.Errorf("technitium: decoding login response failed: %w", err)
	}
	if out.Token == "" {
		return "", errors.New("technitium: login response contained no token")
	}

	c.token = out.Token
	return out.Token, nil
}

// doRaw issues a GET against path with params, validates the response envelope
// status, and returns the raw body. The stored token is appended to every
// request.
func (c *Client) doRaw(ctx context.Context, path string, params url.Values) ([]byte, error) {
	if params == nil {
		params = url.Values{}
	}
	if c.token != "" {
		params.Set("token", c.token)
	}

	reqURL := c.baseURL + path + "?" + params.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, fmt.Errorf("technitium: building request for %s failed: %w", path, err)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("technitium: request to %s failed: %w", path, err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("technitium: reading response from %s failed: %w", path, err)
	}

	var env struct {
		Status       string `json:"status"`
		ErrorMessage string `json:"errorMessage"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		// A non-envelope body (for example an HTML error page from a proxy)
		// carries no status field to classify, so surface the HTTP code.
		return nil, &APIError{HTTPStatus: resp.StatusCode}
	}

	if err := classifyStatus(env.Status, env.ErrorMessage); err != nil {
		return nil, err
	}

	return body, nil
}

// do issues a request and, when out is non-nil, decodes the envelope "response"
// object into it. Endpoints that return only a status leave out untouched.
func (c *Client) do(ctx context.Context, path string, params url.Values, out any) error {
	body, err := c.doRaw(ctx, path, params)
	if err != nil {
		return err
	}
	if out == nil {
		return nil
	}

	var wrapper struct {
		Response json.RawMessage `json:"response"`
	}
	if err := json.Unmarshal(body, &wrapper); err != nil {
		return fmt.Errorf("technitium: decoding response from %s failed: %w", path, err)
	}
	if len(wrapper.Response) == 0 {
		return nil
	}
	if err := json.Unmarshal(wrapper.Response, out); err != nil {
		return fmt.Errorf("technitium: decoding response payload from %s failed: %w", path, err)
	}
	return nil
}
