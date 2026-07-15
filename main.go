// Command xenia-log-ingest is the Part-B backend behind the Xenia-WebServices /logs proxy: it receives an
// authenticated gzip log stream, gunzips + parses it line-by-line, and pushes the lines to Loki. It runs on
// a separate host, reachable only via the bearer token the proxy holds. See README.md and the
// project_log_ingestion design.
package main

import (
	"compress/gzip"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Config is loaded from the environment.
type Config struct {
	ListenAddr         string // LISTEN_ADDR (default ":8090")
	LokiURL            string // LOKI_URL, e.g. http://loki:3100 (required)
	IngestToken        string // INGEST_TOKEN — must match the proxy's LOG_BACKEND_TOKEN (required)
	JobLabel           string // JOB_LABEL (default "xenia-logs") — the ONLY Loki label (keep cardinality low)
	GrafanaBase        string // GRAFANA_BASE_URL (optional) — used to build the {url} in the response
	BatchLines         int    // BATCH_LINES (default 2000) — lines per Loki push
	MaxUncompMB        int    // MAX_UNCOMPRESSED_MB (default 1024) — backstop; the proxy already caps compressed
	StructuredMetadata bool   // STRUCTURED_METADATA (default true) — Loki 3.0+ per-line metadata; false = old-Loki fallback
	lokiPushURL        string // derived
}

func loadConfig() Config {
	c := Config{
		ListenAddr:         envOr("LISTEN_ADDR", ":8090"),
		LokiURL:            strings.TrimRight(envOr("LOKI_URL", ""), "/"),
		IngestToken:        envOr("INGEST_TOKEN", ""),
		JobLabel:           envOr("JOB_LABEL", "xenia-logs"),
		GrafanaBase:        strings.TrimRight(envOr("GRAFANA_BASE_URL", ""), "/"),
		BatchLines:         envInt("BATCH_LINES", 2000),
		MaxUncompMB:        envInt("MAX_UNCOMPRESSED_MB", 1024),
		StructuredMetadata: envBool("STRUCTURED_METADATA", true),
	}
	c.lokiPushURL = c.LokiURL + "/loki/api/v1/push"
	return c
}

func main() {
	cfg := loadConfig()
	if cfg.LokiURL == "" || cfg.IngestToken == "" {
		log.Fatal("[ingest] LOKI_URL and INGEST_TOKEN are required")
	}
	loki := &lokiClient{pushURL: cfg.lokiPushURL, http: &http.Client{Timeout: 30 * time.Second}}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", handleHealth)
	mux.HandleFunc("POST /ingest", ingestHandler(cfg, loki))

	log.Printf("[ingest] listening on %s -> loki %s (job=%s)", cfg.ListenAddr, cfg.LokiURL, cfg.JobLabel)
	if err := http.ListenAndServe(cfg.ListenAddr, mux); err != nil {
		log.Fatalf("[ingest] server error: %v", err)
	}
}

// handleHealth is an unauthenticated liveness probe (the proxy can use it for its 503 gate later).
func handleHealth(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok"))
}

// reportMeta is the per-report structured metadata forwarded from the proxy's X-Xenia-* headers.
type reportMeta struct {
	ReportID  string
	XUID      string
	Gamertag  string
	Note      string
	Build     string
	Truncated string
	LogBytes  string
}

// asLabelMap renders the metadata as Loki structured-metadata (attached per entry). Kept OUT of stream
// labels on purpose (xuid/report_id are high-cardinality; only `job` is a label). Empty fields are dropped.
func (m reportMeta) asStructuredMetadata() map[string]string {
	out := map[string]string{"report_id": m.ReportID}
	add := func(k, v string) {
		if v != "" {
			out[k] = v
		}
	}
	add("xuid", m.XUID)
	add("gamertag", m.Gamertag)
	add("build", m.Build)
	add("truncated", m.Truncated)
	add("note", m.Note)
	return out
}

func ingestHandler(cfg Config, loki *lokiClient) http.HandlerFunc {
	maxBytes := int64(cfg.MaxUncompMB) * 1024 * 1024
	return func(w http.ResponseWriter, r *http.Request) {
		if !bearerOK(r, cfg.IngestToken) {
			http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
			return
		}
		gz, err := gzip.NewReader(r.Body)
		if err != nil {
			http.Error(w, `{"error":"bad_gzip"}`, http.StatusBadRequest)
			return
		}
		defer gz.Close()

		meta := reportMeta{
			ReportID:  newReportID(),
			XUID:      urlDecode(r.Header.Get("X-Xenia-Xuid")),
			Gamertag:  urlDecode(r.Header.Get("X-Xenia-Gamertag")),
			Note:      urlDecode(r.Header.Get("X-Xenia-Note")),
			Truncated: r.Header.Get("X-Xenia-Truncated"),
			LogBytes:  r.Header.Get("X-Xenia-Log-Bytes"),
		}
		baseNano := baseTimestampNano(r.Header.Get("X-Xenia-Time"))

		lines, err := streamToLoki(gz, cfg, loki, &meta, baseNano, maxBytes)
		if err != nil {
			log.Printf("[ingest] report %s failed after %d lines: %v", meta.ReportID, lines, err)
			http.Error(w, `{"error":"ingest_failed"}`, http.StatusBadGateway)
			return
		}

		// The Grafana deep-link is OPERATOR-facing only: logged here, NEVER returned to the tester. The
		// response carries only the report id, so a tester can't reach the internal log viewer (and one
		// tester can't reach another's logs). Testers reference their upload by id; the operator opens it.
		if u := grafanaURL(cfg, meta.ReportID); u != "" {
			log.Printf("[ingest] report %s: %d lines (xuid=%s build=%q) -> %s", meta.ReportID, lines, meta.XUID, meta.Build, u)
		} else {
			log.Printf("[ingest] report %s: %d lines (xuid=%s build=%q)", meta.ReportID, lines, meta.XUID, meta.Build)
		}
		resp := map[string]any{"id": meta.ReportID, "lines": lines}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(resp)
	}
}

// grafanaURL builds a best-effort Grafana Explore deep-link filtering to this report, for the OPERATOR
// (logged server-side — never sent to the tester). Empty when GRAFANA_BASE_URL is unset. Adjust the Explore
// link shape per your Grafana/datasource (see README).
func grafanaURL(cfg Config, reportID string) string {
	if cfg.GrafanaBase == "" {
		return ""
	}
	q := `{job="` + cfg.JobLabel + `"} | report_id="` + reportID + `"`
	left := `["now-24h","now","Loki",{"expr":` + strconv.Quote(q) + `}]`
	return cfg.GrafanaBase + "/explore?left=" + url.QueryEscape(left)
}

// newReportID returns a short random hex id for one uploaded report.
func newReportID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// baseTimestampNano derives the base nanosecond timestamp from the client's X-Xenia-Time (epoch ms), or
// now. Each parsed line is stamped base+lineIndex so order is preserved and timestamps are unique.
func baseTimestampNano(xeniaTimeMs string) int64 {
	if ms, err := strconv.ParseInt(strings.TrimSpace(xeniaTimeMs), 10, 64); err == nil && ms > 0 {
		return ms * 1_000_000
	}
	return time.Now().UnixNano()
}
