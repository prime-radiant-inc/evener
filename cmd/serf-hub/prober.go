package main

import (
	"encoding/json"
	"net/http"
	"time"

	"primeradiant.com/serf/rendezvous"
)

// StatusProber checks daemon liveness by issuing GET <addr>/status and
// parsing the session_id field from the response.
type StatusProber struct {
	Timeout time.Duration
}

// statusInfo is a partial mirror of server.StatusInfo (we only need session_id and state).
type statusInfo struct {
	SessionID string `json:"session_id"`
	State     string `json:"state"`
}

// Probe implements Prober.
func (p *StatusProber) Probe(entry rendezvous.Entry) (sessionID, status string, ok bool) {
	timeout := p.Timeout
	if timeout == 0 {
		timeout = 500 * time.Millisecond
	}
	client := &http.Client{Timeout: timeout}
	req, err := http.NewRequest(http.MethodGet, "http://"+entry.Address+"/status", nil)
	if err != nil {
		return "", "", false
	}
	setDaemonAuthorization(req.Header, entry.HubToken)
	resp, err := client.Do(req)
	if err != nil {
		return "", "", false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", "", false
	}
	var s statusInfo
	if err := json.NewDecoder(resp.Body).Decode(&s); err != nil {
		return "", "", false
	}
	return s.SessionID, s.State, true
}
