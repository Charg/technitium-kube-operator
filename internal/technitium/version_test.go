/*
Copyright (c) 2026 Chris

Licensed under the MIT License. See the LICENSE file in the project root for
the full license text.
*/

package technitium

import "testing"

func TestParseImageVersion(t *testing.T) {
	tests := []struct {
		image string
		want  Version
		ok    bool
	}{
		{"technitium/dns-server:15.6.1", Version{15, 6, 1}, true},
		{"technitium/dns-server:15.5", Version{15, 5, 0}, true},
		{"technitium/dns-server:15.4.0", Version{15, 4, 0}, true},
		{"technitium/dns-server:v15.6.1", Version{15, 6, 1}, true},
		{"technitium/dns-server:15.6.1-something", Version{15, 6, 1}, true},
		{"registry.example.com:5000/tech/dns-server:15.5.2", Version{15, 5, 2}, true},
		{"registry.example.com:5000/tech/dns-server:15.5@sha256:abcd", Version{15, 5, 0}, true},
		{"technitium/dns-server", Version{}, false},
		{"registry.example.com:5000/dns-server", Version{}, false},
		{"technitium/dns-server:latest", Version{}, false},
		{"technitium/dns-server@sha256:abcd", Version{}, false},
		{"technitium/dns-server:custom-build", Version{}, false},
		{"technitium/dns-server:1.2.3.4", Version{}, false},
		{"", Version{}, false},
	}
	for _, tt := range tests {
		got, ok := ParseImageVersion(tt.image)
		if ok != tt.ok || got != tt.want {
			t.Errorf("ParseImageVersion(%q) = %v, %v; want %v, %v", tt.image, got, ok, tt.want, tt.ok)
		}
	}
}

func TestParseVersionErrors(t *testing.T) {
	for _, s := range []string{"", "latest", ".5", "15..1", "1.2.3.4"} {
		if _, err := ParseVersion(s); err == nil {
			t.Errorf("ParseVersion(%q) succeeded, want error", s)
		}
	}
}

func TestVersionCompare(t *testing.T) {
	tests := []struct {
		a, b Version
		want int
	}{
		{Version{15, 5, 0}, Version{15, 5, 0}, 0},
		{Version{15, 4, 0}, Version{15, 5, 0}, -1},
		{Version{15, 6, 1}, Version{15, 5, 0}, 1},
		{Version{16, 0, 0}, Version{15, 9, 9}, 1},
		{Version{14, 9, 9}, Version{15, 0, 0}, -1},
		{Version{15, 5, 1}, Version{15, 5, 0}, 1},
	}
	for _, tt := range tests {
		if got := tt.a.Compare(tt.b); got != tt.want {
			t.Errorf("%v.Compare(%v) = %d, want %d", tt.a, tt.b, got, tt.want)
		}
	}
	if s := (Version{15, 6, 1}).String(); s != "15.6.1" {
		t.Errorf("String() = %q", s)
	}
}
