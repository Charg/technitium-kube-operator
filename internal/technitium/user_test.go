/*
Copyright (c) 2026 Chris

Licensed under the MIT License. See the LICENSE file in the project root for
the full license text.
*/

package technitium

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCreateToken(t *testing.T) {
	tests := []struct {
		name       string
		username   string
		body       string
		wantErr    bool
		wantToken  string
		wantNoCall bool
	}{
		{
			name:      nameSuccess,
			username:  testUser,
			body:      `{"status":"ok","username":"admin","tokenName":"technitium-operator","token":"tok-abc"}`,
			wantToken: "tok-abc",
		},
		{
			name:     "bad credentials",
			username: testUser,
			body:     `{"status":"error","errorMessage":"Invalid username or password."}`,
			wantErr:  true,
		},
		{
			name:     "empty token in ok response",
			username: testUser,
			body:     statusOK,
			wantErr:  true,
		},
		{
			name:       "missing username",
			username:   "",
			wantErr:    true,
			wantNoCall: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			called := false
			var gotUser, gotPass, gotTokenName string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				called = true
				gotUser = r.URL.Query().Get("user")
				gotPass = r.URL.Query().Get("pass")
				gotTokenName = r.URL.Query().Get("tokenName")
				_, _ = w.Write([]byte(tt.body))
			}))
			defer srv.Close()

			c, err := NewClient(srv.URL, WithCredentials(tt.username, "secret"))
			if err != nil {
				t.Fatalf("NewClient: %v", err)
			}

			token, err := c.CreateToken(context.Background(), "technitium-operator")
			if (err != nil) != tt.wantErr {
				t.Fatalf("CreateToken err = %v, wantErr %v", err, tt.wantErr)
			}
			if token != tt.wantToken {
				t.Errorf("token = %q, want %q", token, tt.wantToken)
			}
			if tt.wantNoCall {
				if called {
					t.Error("expected no HTTP call")
				}
				return
			}
			if gotUser != tt.username {
				t.Errorf("user param = %q, want %q", gotUser, tt.username)
			}
			if gotPass != "secret" {
				t.Errorf("pass param = %q, want secret", gotPass)
			}
			if gotTokenName != "technitium-operator" {
				t.Errorf("tokenName param = %q, want technitium-operator", gotTokenName)
			}
		})
	}
}

func TestChangePassword(t *testing.T) {
	tests := []struct {
		name    string
		body    string
		wantErr bool
	}{
		{name: nameSuccess, body: statusOK},
		{
			name:    "server rejects new password",
			body:    `{"status":"error","errorMessage":"Password does not meet complexity requirements."}`,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var gotToken, gotPass string
			c := newTestClient(t, "sess-tok", func(w http.ResponseWriter, r *http.Request) {
				gotToken = r.URL.Query().Get("token")
				gotPass = r.URL.Query().Get("pass")
				_, _ = w.Write([]byte(tt.body))
			})

			err := c.ChangePassword(context.Background(), "new-password")
			if (err != nil) != tt.wantErr {
				t.Fatalf("ChangePassword err = %v, wantErr %v", err, tt.wantErr)
			}
			if gotToken != "sess-tok" {
				t.Errorf("token param = %q, want sess-tok", gotToken)
			}
			if gotPass != "new-password" {
				t.Errorf("pass param = %q, want new-password", gotPass)
			}
		})
	}
}
