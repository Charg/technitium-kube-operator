/*
Copyright (c) 2026 Chris

Licensed under the MIT License. See the LICENSE file in the project root for
the full license text.
*/

package technitium

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
)

// CreateToken mints a non-expiring API token for the client's stored
// credentials, authenticating with user and pass directly rather than a prior
// session token. The token is not stored on the client: unlike Login, this is
// a one-shot mint for a caller (the cluster bootstrap flow) that persists the
// token elsewhere and never needs it held here.
func (c *Client) CreateToken(ctx context.Context, tokenName string) (string, error) {
	if c.username == "" {
		return "", errors.New("technitium: username is required to create a token")
	}

	params := url.Values{}
	params.Set("user", c.username)
	params.Set("pass", c.password)
	params.Set("tokenName", tokenName)

	body, err := c.doRaw(ctx, "/api/user/createToken", params)
	if err != nil {
		return "", err
	}

	var out struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return "", fmt.Errorf("technitium: decoding createToken response failed: %w", err)
	}
	if out.Token == "" {
		return "", errors.New("technitium: createToken response contained no token")
	}

	return out.Token, nil
}

// ChangePassword sets a new admin password using the client's stored session
// token. It is the documented fallback for applying the operator-generated
// password when the env-var file approach is unavailable.
func (c *Client) ChangePassword(ctx context.Context, newPassword string) error {
	params := url.Values{}
	params.Set("pass", newPassword)

	return c.do(ctx, "/api/user/changePassword", params, nil)
}
