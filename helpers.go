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

// parseBuild extracts the build string from a log's first line ("Build: ..."); "" if it isn't one.
func parseBuild(line string) string {
	const p = "Build:"
	if strings.HasPrefix(line, p) {
		return strings.TrimSpace(line[len(p):])
	}
	return ""
}
