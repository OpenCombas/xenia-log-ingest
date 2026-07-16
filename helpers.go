package main

import (
	"crypto/subtle"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
)

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envInt(key string, def int) int {
	if v, err := strconv.Atoi(os.Getenv(key)); err == nil && v > 0 {
		return v
	}
	return def
}

func envBool(key string, def bool) bool {
	switch strings.ToLower(os.Getenv(key)) {
	case "true", "1", "yes":
		return true
	case "false", "0", "no":
		return false
	default:
		return def
	}
}

// bearerOK constant-time compares the request's Authorization bearer against the expected ingest token.
func bearerOK(r *http.Request, expected string) bool {
	if expected == "" {
		return false
	}
	got := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	return subtle.ConstantTimeCompare([]byte(got), []byte(expected)) == 1
}

// urlDecode best-effort URL-decodes a header value (the proxy forwards X-Xenia-Gamertag/Note URL-encoded);
// returns the raw value if it doesn't decode.
func urlDecode(v string) string {
	if v == "" {
		return ""
	}
	if dec, err := url.QueryUnescape(v); err == nil {
		return dec
	}
	return v
}

// parseLineTimestamp reads a Xenia log line's leading epoch-MILLISECOND timestamp and returns it in
// nanoseconds. Xenia lines look like "1784168956276 i> 00000150 <msg>"; the startup cvar dump and any
// continuation lines have no leading timestamp -> (0, false). The leading token must be a plausible epoch-ms
// (~2001..2096) so a stray small number or a word never false-matches.
func parseLineTimestamp(line string) (int64, bool) {
	i := strings.IndexByte(line, ' ')
	if i <= 0 {
		return 0, false
	}
	ms, err := strconv.ParseInt(line[:i], 10, 64)
	if err != nil {
		return 0, false
	}
	if ms < 1_000_000_000_000 || ms > 4_000_000_000_000 { // ~2001-09 .. 2096-10 in epoch ms
		return 0, false
	}
	return ms * 1_000_000, true
}

// parseBuild extracts the build id from a Xenia log line carrying the "Build: " marker, e.g.
//
//	1784168179342 i> 00000150 Build: ibac/opencombas_v13_party@2dcd6b4cc on Jul 15 2026
//
// -> "ibac/opencombas_v13_party@2dcd6b4cc" (the trailing " on <date>" is dropped). The build line is NOT
// the log's first line and carries Xenia's "<ts> <lvl>> <thread> " prefix, so we search for the marker
// anywhere in the line, not as a strict prefix. Returns "" if the line has no marker.
func parseBuild(line string) string {
	const marker = "Build: "
	i := strings.Index(line, marker)
	if i < 0 {
		return ""
	}
	b := strings.TrimSpace(line[i+len(marker):])
	if j := strings.Index(b, " on "); j >= 0 { // drop the " on <date>" suffix, keep the branch@commit id
		b = strings.TrimSpace(b[:j])
	}
	return b
}
