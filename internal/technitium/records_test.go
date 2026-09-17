/*
Copyright (c) 2026 Chris

Licensed under the MIT License. See the LICENSE file in the project root for
the full license text.
*/

package technitium

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"testing"
)

const (
	testDomain             = "www.example.com"
	recordNotFoundBody     = `{"status":"error","errorMessage":"No such record was found: www.example.com"}`
	recordAlreadyExistsMsg = `{"status":"error","errorMessage":"Record already exists: www.example.com"}`
)

func ptrInt32(v int32) *int32 { return &v }

func TestAddRecord(t *testing.T) {
	tests := []struct {
		name      string
		opts      AddRecordOptions
		body      string
		wantErrIs error
		wantQuery url.Values
	}{
		{
			name: "A record",
			opts: AddRecordOptions{
				Zone: testZone, Domain: testDomain, Type: "A", TTL: ptrInt32(300),
				RecordValue: RecordValue{IPAddress: "1.1.1.1"},
			},
			body: statusOK,
			wantQuery: url.Values{
				paramZone: {testZone}, "domain": {testDomain}, paramType: {"A"},
				"ttl": {"300"}, "ipAddress": {"1.1.1.1"},
			},
		},
		{
			name: "TXT record",
			opts: AddRecordOptions{
				Zone: testZone, Domain: testDomain, Type: "TXT",
				RecordValue: RecordValue{Text: "v=spf1 -all"},
			},
			body: statusOK,
			wantQuery: url.Values{
				paramZone: {testZone}, "domain": {testDomain}, paramType: {"TXT"}, "text": {"v=spf1 -all"},
			},
		},
		{
			name: "MX record with preference",
			opts: AddRecordOptions{
				Zone: testZone, Domain: testDomain, Type: "MX",
				RecordValue: RecordValue{Exchange: "mail.example.com", Preference: ptrInt32(10)},
			},
			body: statusOK,
			wantQuery: url.Values{
				paramZone: {testZone}, "domain": {testDomain}, paramType: {"MX"},
				"exchange": {"mail.example.com"}, "preference": {"10"},
			},
		},
		{
			name: "CNAME record",
			opts: AddRecordOptions{
				Zone: testZone, Domain: "alias.example.com", Type: "CNAME",
				RecordValue: RecordValue{CName: "example.com"},
			},
			body:      statusOK,
			wantQuery: url.Values{paramType: {"CNAME"}, "cname": {"example.com"}},
		},
		{
			name: "NS record",
			opts: AddRecordOptions{
				Zone: testZone, Domain: testDomain, Type: "NS",
				RecordValue: RecordValue{NameServer: "ns1.example.com"},
			},
			body:      statusOK,
			wantQuery: url.Values{paramType: {"NS"}, "nameServer": {"ns1.example.com"}},
		},
		{
			name: "PTR record",
			opts: AddRecordOptions{
				Zone: "1.168.192.in-addr.arpa", Domain: "10.1.168.192.in-addr.arpa", Type: "PTR",
				RecordValue: RecordValue{PtrName: "host.example.com"},
			},
			body:      statusOK,
			wantQuery: url.Values{paramType: {"PTR"}, "ptrName": {"host.example.com"}},
		},
		{
			name:      "already exists is a sentinel",
			opts:      AddRecordOptions{Zone: testZone, Domain: testDomain, Type: "A", RecordValue: RecordValue{IPAddress: "1.1.1.1"}},
			body:      recordAlreadyExistsMsg,
			wantErrIs: ErrRecordAlreadyExists,
		},
		{
			name:      "generic error",
			opts:      AddRecordOptions{Zone: testZone, Domain: testDomain, Type: "A", RecordValue: RecordValue{IPAddress: "1.1.1.1"}},
			body:      `{"status":"error","errorMessage":"Access was denied."}`,
			wantErrIs: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := newTestClient(t, "t", func(w http.ResponseWriter, r *http.Request) {
				assertQuery(t, r, tt.wantQuery)
				_, _ = w.Write([]byte(tt.body))
			})

			err := c.AddRecord(context.Background(), tt.opts)
			assertErr(t, err, tt.wantErrIs, tt.body)
		})
	}
}

func TestAddRecordRequiresDomainAndType(t *testing.T) {
	c, err := NewClient("https://dns.internal", WithToken("t"))
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	if err := c.AddRecord(context.Background(), AddRecordOptions{Type: "A", RecordValue: RecordValue{IPAddress: "1.1.1.1"}}); err == nil {
		t.Fatal("expected error for empty domain")
	}
	if err := c.AddRecord(context.Background(), AddRecordOptions{Domain: testDomain, RecordValue: RecordValue{IPAddress: "1.1.1.1"}}); err == nil {
		t.Fatal("expected error for empty type")
	}
}

func TestAddRecordRequiresMatchingRData(t *testing.T) {
	c, err := NewClient("https://dns.internal", WithToken("t"))
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	// A record with no ipAddress: setRecordValueParams should reject this before
	// any request is sent, rather than let the server reject an incomplete call.
	if err := c.AddRecord(context.Background(), AddRecordOptions{Domain: testDomain, Type: "A"}); err == nil {
		t.Fatal("expected error for missing ipAddress")
	}
}

func TestGetRecords(t *testing.T) {
	tests := []struct {
		name      string
		body      string
		wantErrIs error
		wantLen   int
	}{
		{
			name: nameSuccess,
			body: `{"status":"ok","response":{"records":[` +
				`{"name":"www.example.com","type":"A","ttl":3600,"rData":{"ipAddress":"1.1.1.1"}},` +
				`{"name":"www.example.com","type":"TXT","ttl":3600,"rData":{"text":"hello"}}` +
				`]}}`,
			wantLen: 2,
		},
		{
			name:      "missing record",
			body:      recordNotFoundBody,
			wantErrIs: ErrRecordNotFound,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := newTestClient(t, "t", func(w http.ResponseWriter, r *http.Request) {
				assertQuery(t, r, url.Values{"domain": {testDomain}, "listZone": {"false"}})
				_, _ = w.Write([]byte(tt.body))
			})

			records, err := c.GetRecords(context.Background(), testZone, testDomain)
			assertErr(t, err, tt.wantErrIs, tt.body)
			if tt.wantErrIs != nil {
				return
			}
			if len(records) != tt.wantLen {
				t.Fatalf("got %d records, want %d", len(records), tt.wantLen)
			}
		})
	}

	t.Run("parses rdata per type", func(t *testing.T) {
		c := newTestClient(t, "t", func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(`{"status":"ok","response":{"records":[` +
				`{"name":"www.example.com","type":"A","ttl":300,"rData":{"ipAddress":"2.2.2.2"}}` +
				`]}}`))
		})

		records, err := c.GetRecords(context.Background(), testZone, testDomain)
		if err != nil {
			t.Fatalf("GetRecords: %v", err)
		}
		if len(records) != 1 {
			t.Fatalf("got %d records, want 1", len(records))
		}
		if records[0].TTL != 300 || records[0].RData.IPAddress != "2.2.2.2" {
			t.Errorf("unexpected record: %+v", records[0])
		}
	})
}

func TestGetRecordsRequiresDomain(t *testing.T) {
	c, err := NewClient("https://dns.internal", WithToken("t"))
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if _, err := c.GetRecords(context.Background(), testZone, ""); err == nil {
		t.Fatal("expected error for empty domain")
	}
}

func TestUpdateRecord(t *testing.T) {
	tests := []struct {
		name      string
		opts      UpdateRecordOptions
		body      string
		wantErrIs error
		wantQuery url.Values
	}{
		{
			name: "corrects ttl",
			opts: UpdateRecordOptions{
				Zone: testZone, Domain: testDomain, Type: "A", TTL: 60,
				RecordValue: RecordValue{IPAddress: "1.1.1.1"},
			},
			body: statusOK,
			wantQuery: url.Values{
				paramZone: {testZone}, "domain": {testDomain}, paramType: {"A"},
				"ttl": {"60"}, "ipAddress": {"1.1.1.1"},
			},
		},
		{
			name: "missing record",
			opts: UpdateRecordOptions{
				Domain: testDomain, Type: "A", TTL: 60, RecordValue: RecordValue{IPAddress: "1.1.1.1"},
			},
			body:      recordNotFoundBody,
			wantErrIs: ErrRecordNotFound,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := newTestClient(t, "t", func(w http.ResponseWriter, r *http.Request) {
				assertQuery(t, r, tt.wantQuery)
				_, _ = w.Write([]byte(tt.body))
			})

			err := c.UpdateRecord(context.Background(), tt.opts)
			assertErr(t, err, tt.wantErrIs, tt.body)
		})
	}
}

func TestDeleteRecord(t *testing.T) {
	tests := []struct {
		name      string
		opts      DeleteRecordOptions
		body      string
		wantErrIs error
		wantQuery url.Values
	}{
		{
			name: nameSuccess,
			opts: DeleteRecordOptions{
				Zone: testZone, Domain: testDomain, Type: "A", RecordValue: RecordValue{IPAddress: "1.1.1.1"},
			},
			body: statusOK,
			wantQuery: url.Values{
				paramZone: {testZone}, "domain": {testDomain}, paramType: {"A"}, "ipAddress": {"1.1.1.1"},
			},
		},
		{
			name: "missing record is a sentinel, tolerated as idempotent by callers",
			opts: DeleteRecordOptions{
				Domain: testDomain, Type: "MX", RecordValue: RecordValue{Exchange: "mail.example.com"},
			},
			body:      recordNotFoundBody,
			wantErrIs: ErrRecordNotFound,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := newTestClient(t, "t", func(w http.ResponseWriter, r *http.Request) {
				assertQuery(t, r, tt.wantQuery)
				_, _ = w.Write([]byte(tt.body))
			})

			err := c.DeleteRecord(context.Background(), tt.opts)
			assertErr(t, err, tt.wantErrIs, tt.body)
		})
	}
}

func TestClassifyStatusDistinguishesRecordFromZone(t *testing.T) {
	// classifyStatus is shared transport-layer code; this pins down that a
	// "record" message never gets mapped to a zone sentinel and vice versa,
	// since both resources reuse the same "already exists"/"not found" phrasing.
	if err := classifyStatus("error", "Record already exists: www.example.com"); !errors.Is(err, ErrRecordAlreadyExists) {
		t.Errorf("err = %v, want ErrRecordAlreadyExists", err)
	}
	if err := classifyStatus("error", "Zone already exists: example.com"); !errors.Is(err, ErrZoneAlreadyExists) {
		t.Errorf("err = %v, want ErrZoneAlreadyExists", err)
	}
	if err := classifyStatus("error", "No such record was found: www.example.com"); !errors.Is(err, ErrRecordNotFound) {
		t.Errorf("err = %v, want ErrRecordNotFound", err)
	}
}
