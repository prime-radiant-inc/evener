package main

import (
	"encoding/json"
	"net/http"
	"time"
)

// StatusProber checks daemon liveness by issuing GET <addr>/status and
// parsing the session_id field from the response.
type StatusProber struct {
	Timeout time.Duration
}

// statusInfo is a partial mirror of server.StatusInfo (we only need session_id).
type statusInfo struct {
	SessionID string `json:"session_id"`
}

// Probe implements Prober.
func (p *StatusProber) Probe(addr string) (string, bool) {
	timeout := p.Timeout
	if timeout == 0 {
		timeout = 500 * time.Millisecond
	}
	client := &http.Client{Timeout: timeout}
	resp, err := client.Get("http://" + addr + "/status")
	if err != nil {
		return "", false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", false
	}
	var s statusInfo
	if err := json.NewDecoder(resp.Body).Decode(&s); err != nil {
		return "", false
	}
	return s.SessionID, true
}
