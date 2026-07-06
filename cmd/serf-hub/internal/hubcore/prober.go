package hubcore

import (
	"context"
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

// statusInfo is a partial mirror of server.StatusInfo (we only need
// session_id, state, and pending_ask).
type statusInfo struct {
	SessionID  string `json:"session_id"`
	State      string `json:"state"`
	PendingAsk bool   `json:"pending_ask"`
}

// Probe implements Prober.
func (p *StatusProber) Probe(entry rendezvous.Entry) (sessionID, status string, pendingAsk, ok bool) {
	timeout := p.Timeout
	if timeout == 0 {
		timeout = 500 * time.Millisecond
	}
	client := &http.Client{Timeout: timeout}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+entry.Address+"/status", nil)
	if err != nil {
		return "", "", false, false
	}
	SetDaemonAuthorization(req.Header, entry.HubToken)
	resp, err := client.Do(req)
	if err != nil {
		return "", "", false, false
	}
	defer resp.Body.Close() //nolint:errcheck // response body close on read path; error is not actionable
	if resp.StatusCode != http.StatusOK {
		return "", "", false, false
	}
	var s statusInfo
	if err := json.NewDecoder(resp.Body).Decode(&s); err != nil {
		return "", "", false, false
	}
	return s.SessionID, s.State, s.PendingAsk, true
}
