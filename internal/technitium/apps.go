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

// DNSAppInfo is one entry from /api/apps/list: a DNS App currently installed
// on the server.
//
// WHY: the exact response shape (an "apps" array of objects carrying "name"
// and "version") and the downloadAndInstall/downloadAndUpdate/uninstall/config
// endpoints below are the best-known contract for Technitium's app management
// API, reconstructed from its documented parameter naming conventions rather
// than confirmed against a live server. The e2e spec (which does exercise a
// real instance) is the actual signal on whether these names are right.
type DNSAppInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// appNameParams builds the query carrying the required app name, or an error
// when it is empty. Every DNS Apps endpoint starts from this.
func appNameParams(name string) (url.Values, error) {
	if name == "" {
		return nil, errors.New("technitium: app name is required")
	}
	params := url.Values{}
	params.Set("name", name)
	return params, nil
}

// ListApps fetches every DNS App currently installed on the server.
func (c *Client) ListApps(ctx context.Context) ([]DNSAppInfo, error) {
	var resp struct {
		Apps []DNSAppInfo `json:"apps"`
	}
	if err := c.do(ctx, "/api/apps/list", nil, &resp); err != nil {
		return nil, err
	}
	return resp.Apps, nil
}

// DownloadAndInstallApp downloads the app package at downloadURL and installs
// it under name. It returns ErrAppAlreadyInstalled when an app with that name
// is already installed, letting callers treat installation as idempotent.
func (c *Client) DownloadAndInstallApp(ctx context.Context, name, downloadURL string) error {
	params, err := appNameParams(name)
	if err != nil {
		return err
	}
	if downloadURL == "" {
		return errors.New("technitium: url is required")
	}
	params.Set("url", downloadURL)

	return c.do(ctx, "/api/apps/downloadAndInstall", params, nil)
}

// DownloadAndUpdateApp downloads the app package at downloadURL and updates
// the already-installed app named name to it.
func (c *Client) DownloadAndUpdateApp(ctx context.Context, name, downloadURL string) error {
	params, err := appNameParams(name)
	if err != nil {
		return err
	}
	if downloadURL == "" {
		return errors.New("technitium: url is required")
	}
	params.Set("url", downloadURL)

	return c.do(ctx, "/api/apps/downloadAndUpdate", params, nil)
}

// UninstallApp removes the named app from the server. It returns
// ErrAppNotInstalled when no app with that name is installed, letting callers
// treat removal as idempotent.
func (c *Client) UninstallApp(ctx context.Context, name string) error {
	params, err := appNameParams(name)
	if err != nil {
		return err
	}
	return c.do(ctx, "/api/apps/uninstall", params, nil)
}

// GetAppConfig fetches the named app's current configuration blob, opaque to
// this client.
func (c *Client) GetAppConfig(ctx context.Context, name string) (string, error) {
	params, err := appNameParams(name)
	if err != nil {
		return "", err
	}

	var resp struct {
		Config string `json:"config"`
	}
	if err := c.do(ctx, "/api/apps/config/get", params, &resp); err != nil {
		return "", err
	}
	return resp.Config, nil
}

// SetAppConfig replaces the named app's configuration blob with config,
// passed through verbatim. An invalid config is the app's own to reject; the
// server surfaces that as an ordinary error response rather than a distinct
// status, so it comes back here as an *APIError like any other rejected call.
func (c *Client) SetAppConfig(ctx context.Context, name, config string) error {
	params, err := appNameParams(name)
	if err != nil {
		return err
	}
	params.Set("config", config)

	return c.do(ctx, "/api/apps/config/set", params, nil)
}
