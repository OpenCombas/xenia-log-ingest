package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

// lokiClient pushes batches to a Loki push endpoint.
type lokiClient struct {
	pushURL string
	http    *http.Client
}

// lokiStream is one Loki stream: a label set + a list of [tsNano, line, (structuredMetadata)] values.
type lokiStream struct {
	Stream map[string]string `json:"stream"`
	Values [][]any           `json:"values"`
}

type lokiPush struct {
	Streams []lokiStream `json:"streams"`
}

// push sends one batch of values under a single label set. Labels MUST stay low-cardinality (we send only
// {job}); per-report identifiers ride in each value's structured-metadata element instead.
func (c *lokiClient) push(labels map[string]string, values [][]any) error {
	buf, err := json.Marshal(lokiPush{Streams: []lokiStream{{Stream: labels, Values: values}}})
	if err != nil {
		return err
	}
	req, err := http.NewRequest(http.MethodPost, c.pushURL, bytes.NewReader(buf))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("loki push %d: %s", resp.StatusCode, string(b))
	}
	return nil
}
