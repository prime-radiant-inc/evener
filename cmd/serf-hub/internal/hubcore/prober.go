package hubcore

import (
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"time"

	"primeradiant.com/serf/appwire"
	"primeradiant.com/serf/rendezvous"
)

// StatusProber checks daemon liveness by issuing GET <addr>/status and
// parsing the session_id field from the response.
type StatusProber struct {
	Timeout time.Duration
	client  *http.Client
}

// statusInfo is a partial mirror of server.StatusInfo containing the fields
// the hub needs for session and in-process child lifecycle projection.
type statusInfo struct {
	SessionID         string `json:"session_id"`
	State             string `json:"state"`
	PendingAsk        bool   `json:"pending_ask"`
	PendingEscalation bool   `json:"pending_escalation"`
	Detailed          *struct {
		Jobs []struct {
			JobType       string `json:"job_type"`
			Type          string `json:"type"`
			Status        string `json:"status"`
			TranscriptRef string `json:"transcript_ref"`
		} `json:"jobs"`
	} `json:"detailed"`
}

// Probe implements Prober.
func (p *StatusProber) Probe(entry rendezvous.Entry) ProbeResult {
	timeout := p.Timeout
	if timeout == 0 {
		timeout = 500 * time.Millisecond
	}
	client := p.client
	if client == nil {
		client = &http.Client{Timeout: timeout}
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+entry.Address+"/status", nil)
	if err != nil {
		return ProbeResult{}
	}
	SetDaemonAuthorization(req.Header, entry.HubToken)
	resp, err := client.Do(req)
	if err != nil {
		return ProbeResult{}
	}
	defer resp.Body.Close() //nolint:errcheck // response body close on read path; error is not actionable
	if resp.StatusCode != http.StatusOK {
		return ProbeResult{}
	}
	var s statusInfo
	if err := json.NewDecoder(resp.Body).Decode(&s); err != nil {
		return ProbeResult{}
	}
	seen := make(map[string]bool)
	var runningSubagentIDs []string
	if s.Detailed != nil {
		for _, job := range s.Detailed.Jobs {
			jobType := job.JobType
			if jobType == "" {
				jobType = job.Type
			}
			if jobType != "delegate" || job.Status != "running" {
				continue
			}
			ref, err := appwire.ParseRef(job.TranscriptRef)
			if err != nil || ref.SourceID != "local" || ref.ThreadID == "" || seen[ref.ThreadID] {
				continue
			}
			seen[ref.ThreadID] = true
			runningSubagentIDs = append(runningSubagentIDs, ref.ThreadID)
		}
	}
	sort.Strings(runningSubagentIDs)
	return ProbeResult{
		SessionID:          s.SessionID,
		Status:             s.State,
		PendingAsk:         s.PendingAsk,
		PendingEscalation:  s.PendingEscalation,
		RunningSubagentIDs: runningSubagentIDs,
		OK:                 true,
	}
}
