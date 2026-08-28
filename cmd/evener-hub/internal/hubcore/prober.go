package hubcore

import (
	"context"
	"net/http"
	"sort"
	"strings"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/rendezvous"
)

// StatusProber checks daemon liveness through its typed AppWire thread
// snapshots.
type StatusProber struct {
	Timeout time.Duration
	client  *http.Client
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
	header := http.Header{}
	SetDaemonAuthorization(header, entry.HubToken)
	transport, err := appwire.DialWebSocketWithHeaders(ctx, entry.Endpoint, client, header)
	if err != nil {
		return ProbeResult{}
	}
	defer transport.Close() //nolint:errcheck // probe cleanup; error is not actionable
	appClient := appwire.NewClient(transport)
	appClient.Start(ctx)
	if _, err := appClient.Initialize(ctx, appwire.InitializeParams{ClientInfo: appwire.ClientInfo{Name: "evener-hub"}}); err != nil {
		return ProbeResult{}
	}
	rootResponse, err := appClient.ThreadRead(ctx, appwire.ThreadReadParams{})
	if err != nil {
		return ProbeResult{}
	}
	listResponse, err := appClient.ThreadList(ctx, appwire.ThreadListParams{IncludeSubagents: true})
	if err != nil {
		return ProbeResult{}
	}
	root := rootResponse.Thread
	rootID := statusThreadID(root)
	if strings.TrimSpace(root.ID) == "" || rootID == "" {
		return ProbeResult{}
	}

	seen := make(map[string]bool)
	rootListed := false
	var runningSubagentIDs []string
	var runningSubagentStates map[string]string
	for _, thread := range listResponse.Data {
		if thread.ID == root.ID && statusThreadID(thread) == rootID {
			rootListed = true
			continue
		}
		if thread.Status.Type == appwire.ThreadStatusClosed {
			continue
		}
		id := statusThreadID(thread)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		runningSubagentIDs = append(runningSubagentIDs, id)
		if state := strings.TrimSpace(thread.Status.Type); state != "" {
			if runningSubagentStates == nil {
				runningSubagentStates = make(map[string]string)
			}
			runningSubagentStates[id] = state
		}
	}
	if !rootListed {
		return ProbeResult{}
	}
	sort.Strings(runningSubagentIDs)

	return ProbeResult{
		SessionID:             rootID,
		Status:                root.Status.Type,
		PendingAsk:            root.Evener.AskPending,
		PendingEscalation:     len(root.Evener.PendingEscalations) > 0,
		RunningSubagentIDs:    runningSubagentIDs,
		RunningSubagentStates: runningSubagentStates,
		RunningJobs:           runningNonAgentJobs(root.Evener.Diagnostics),
		OK:                    true,
	}
}

func statusThreadID(thread appwire.Thread) string {
	if sessionID := strings.TrimSpace(thread.SessionID); sessionID != "" {
		return sessionID
	}
	return strings.TrimSpace(thread.ID)
}

func runningNonAgentJobs(diagnostics *appwire.EvenerDiagnostics) []appwire.EvenerJobInfo {
	if diagnostics == nil {
		return nil
	}
	var jobs []appwire.EvenerJobInfo
	for _, job := range diagnostics.Jobs {
		if strings.TrimSpace(job.JobType) == "delegate" || terminalJobStatus(job.Status) {
			continue
		}
		jobs = append(jobs, job)
	}
	return jobs
}

func terminalJobStatus(status string) bool {
	switch strings.TrimSpace(status) {
	case "completed", "failed", "cancelled", "stopped", "exhausted":
		return true
	default:
		return false
	}
}
