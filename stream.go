package main

import (
	"bufio"
	"fmt"
	"io"
	"strconv"
)

// maxLineBytes caps a single log line so a pathological log can't blow up memory (default bufio is 64KB).
const maxLineBytes = 4 * 1024 * 1024

// streamToLoki scans the (already gunzipped) log line-by-line and pushes it to Loki in batches, never
// holding the whole log in memory. The first line is checked for "Build: ..." to enrich the report's
// structured metadata. Returns the number of lines forwarded. Only {job} is a Loki label; every
// per-report identifier (report_id, xuid, gamertag, build, truncated, note) rides in each value's
// structured-metadata element (or, when STRUCTURED_METADATA=false, is prefixed onto the line for old Loki).
func streamToLoki(r io.Reader, cfg Config, loki *lokiClient, meta *reportMeta, baseNano int64, maxBytes int64) (int, error) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), maxLineBytes)

	labels := map[string]string{"job": cfg.JobLabel}
	var batch [][]any
	var totalBytes int64
	lineIdx := 0
	firstLine := true
	buildFound := false
	var smd map[string]string
	// Timestamps come from each line's own leading epoch-ms (parseLineTimestamp), NOT the received time.
	// Lines without one (the cvar dump, continuation lines) inherit the last real timestamp; baseNano
	// (X-Xenia-Time) seeds it for the header lines that precede the first timestamped line.
	lastNano := baseNano

	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		err := loki.push(labels, batch)
		batch = batch[:0]
		return err
	}

	for sc.Scan() {
		line := sc.Text()
		totalBytes += int64(len(line)) + 1
		if totalBytes > maxBytes {
			return lineIdx, fmt.Errorf("exceeded max uncompressed %d MB", cfg.MaxUncompMB)
		}

		if firstLine {
			firstLine = false
			smd = meta.asStructuredMetadata()
		}

		// The Xenia "Build:" line sits a few lines in (after the cvar dump), so scan — not just line 0 —
		// until we find it. Mutating the SHARED smd map back-fills every entry already appended to the
		// not-yet-flushed batch (they all reference this one map), so the build lands on the whole report as
		// long as the line appears within the first batch (it does — it's near the top).
		if !buildFound && cfg.StructuredMetadata {
			if b := parseBuild(line); b != "" {
				meta.Build = b
				smd["build"] = b
				buildFound = true
			}
		}

		tsNano, ok := parseLineTimestamp(line)
		if !ok {
			tsNano = lastNano // no own timestamp -> inherit the last real one (Loki 3.0 accepts equal/out-of-order)
		}
		lastNano = tsNano
		ts := strconv.FormatInt(tsNano, 10)
		if cfg.StructuredMetadata {
			batch = append(batch, []any{ts, line, smd})
		} else {
			// Old-Loki fallback: no structured metadata -> keep the report id findable by prefixing it.
			batch = append(batch, []any{ts, "report_id=" + meta.ReportID + " " + line})
		}

		lineIdx++
		if len(batch) >= cfg.BatchLines {
			if err := flush(); err != nil {
				return lineIdx, err
			}
		}
	}
	if err := sc.Err(); err != nil {
		return lineIdx, err
	}
	if err := flush(); err != nil {
		return lineIdx, err
	}
	return lineIdx, nil
}
