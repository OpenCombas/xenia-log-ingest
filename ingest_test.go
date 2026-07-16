package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestParseBuild(t *testing.T) {
	// Real Xenia format: timestamped/prefixed, "Build: <id> on <date>".
	line := "1784168179342 i> 00000150 Build: ibac/opencombas_v13_party@2dcd6b4cc on Jul 15 2026"
	if got := parseBuild(line); got != "ibac/opencombas_v13_party@2dcd6b4cc" {
		t.Errorf("parseBuild = %q, want the branch@commit id", got)
	}
	if got := parseBuild("some other line"); got != "" {
		t.Errorf("parseBuild non-build = %q, want empty", got)
	}
	// Must not false-positive on cvar lines that merely contain "build".
	if got := parseBuild("kernel_build_version = 1888"); got != "" {
		t.Errorf("parseBuild false-positive = %q, want empty", got)
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
	// The build line is NOT first (a cvar line precedes it) and is prefixed with a real epoch-ms, like a
	// Xenia log. The cvar line has no timestamp; the Build line's leading 1784168956276 is its own time.
	body := "cvar_x = false\n1784168956276 i> 001 Build: test-1 on Jul 15 2026\nalpha\nbravo\n"

	n, err := streamToLoki(strings.NewReader(body), cfg, loki, meta, 1000, 1024*1024)
	if err != nil {
		t.Fatalf("streamToLoki: %v", err)
	}
	if n != 4 {
		t.Errorf("lines = %d, want 4", n)
	}
	if meta.Build != "test-1" {
		t.Errorf("build = %q, want test-1 (scanned off a non-first, prefixed line)", meta.Build)
	}
	// BatchLines=2 over 4 lines -> two pushes (2 then 2). The build is found on line 1 before the first
	// flush, so even the preceding cvar line (line 0, in the same batch) carries it via the shared map.
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
	if total != 4 {
		t.Errorf("total values = %d, want 4", total)
	}
	// The cvar line (no own ts) inherits baseNano (1000); the Build line carries its own epoch-ms in ns.
	wantBuildTs := "1784168956276000000"
	if pushes[0].Streams[0].Values[0][0] != "1000" || pushes[0].Streams[0].Values[1][0] != wantBuildTs {
		t.Errorf("timestamps = %v/%v, want 1000/%s", pushes[0].Streams[0].Values[0][0], pushes[0].Streams[0].Values[1][0], wantBuildTs)
	}
}

func TestParseLineTimestamp(t *testing.T) {
	if ns, ok := parseLineTimestamp("1784168956276 i> 00000150 hello"); !ok || ns != int64(1784168956276)*1_000_000 {
		t.Errorf("parseLineTimestamp = %d,%v; want %d,true", ns, ok, int64(1784168956276)*1_000_000)
	}
	if _, ok := parseLineTimestamp("d3d12_break_on_error = false"); ok {
		t.Errorf("cvar line should have no timestamp")
	}
	if _, ok := parseLineTimestamp("1888 kernel"); ok {
		t.Errorf("small leading number must not parse as an epoch-ms")
	}
	if _, ok := parseLineTimestamp("noleadingspace"); ok {
		t.Errorf("no space -> no timestamp")
	}
}
