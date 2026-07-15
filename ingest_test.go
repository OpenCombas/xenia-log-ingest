package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestParseBuild(t *testing.T) {
	if got := parseBuild("Build: 2026-05-17-preview"); got != "2026-05-17-preview" {
		t.Errorf("parseBuild = %q, want %q", got, "2026-05-17-preview")
	}
	if got := parseBuild("some other line"); got != "" {
		t.Errorf("parseBuild non-build = %q, want empty", got)
	}
}

func TestStreamToLoki(t *testing.T) {
	var pushes []lokiPush
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var p lokiPush
		_ = json.NewDecoder(r.Body).Decode(&p)
		pushes = append(pushes, p)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	cfg := Config{JobLabel: "xenia-logs", BatchLines: 2, StructuredMetadata: true, MaxUncompMB: 1024}
	loki := &lokiClient{pushURL: srv.URL, http: srv.Client()}
	// Synthetic log (no real gamertags/log data — open-source project). Note: streamToLoki takes the
	// ALREADY-gunzipped reader; the handler owns gunzip.
	meta := &reportMeta{ReportID: "rid1", XUID: "000900000000AA01", Gamertag: "Tester-1"}
	body := "Build: test-1\nline A\nline B\n"

	n, err := streamToLoki(strings.NewReader(body), cfg, loki, meta, 1000, 1024*1024)
	if err != nil {
		t.Fatalf("streamToLoki: %v", err)
	}
	if n != 3 {
		t.Errorf("lines = %d, want 3", n)
	}
	if meta.Build != "test-1" {
		t.Errorf("build = %q, want test-1", meta.Build)
	}
	// BatchLines=2 over 3 lines -> two pushes (2 then 1).
	if len(pushes) != 2 {
		t.Fatalf("pushes = %d, want 2", len(pushes))
	}

	total := 0
	for _, p := range pushes {
		if len(p.Streams) != 1 || p.Streams[0].Stream["job"] != "xenia-logs" {
			t.Fatalf("stream labels = %v, want only job=xenia-logs", p.Streams)
		}
		for _, v := range p.Streams[0].Values {
			total++
			if len(v) != 3 {
				t.Fatalf("value has %d parts, want 3 (ts, line, structured-metadata)", len(v))
			}
			smd, ok := v[2].(map[string]any)
			if !ok || smd["report_id"] != "rid1" || smd["xuid"] != "000900000000AA01" || smd["build"] != "test-1" {
				t.Errorf("structured metadata = %v, want report_id/xuid/build set", v[2])
			}
		}
	}
	if total != 3 {
		t.Errorf("total values = %d, want 3", total)
	}
	// timestamps are base+index as strings, strictly increasing.
	if pushes[0].Streams[0].Values[0][0] != "1000" || pushes[0].Streams[0].Values[1][0] != "1001" {
		t.Errorf("timestamps = %v/%v, want 1000/1001", pushes[0].Streams[0].Values[0][0], pushes[0].Streams[0].Values[1][0])
	}
}
