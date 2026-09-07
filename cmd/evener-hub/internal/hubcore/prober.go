package hubcore

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
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

// hubConnectionLogf is the appwire.Client connection-lifecycle sink (see
// appwire.Client.SetLogf) for probe connections: the hub is a plain daemon,
// never a TUI rendering over an interactive terminal, so its own stderr —
// labelled like every other hub diagnostic (past.go, roster.go) — is a safe
// destination, unlike the TUI's stderr (issue #783).
func hubConnectionLogf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "[hub] "+format+"\n", args...)
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
	appClient.SetLogf(hubConnectionLogf)
	appClient.Start(ctx)
	if _, err := appClient.Initialize(ctx, appwire.InitializeParams{ClientInfo: appwire.ClientInfo{Name: "evener-hub"}}); err != nil {
		var wire appwire.WireError
		var mismatch appwire.ProtocolVersionMismatchError
		if entry.Protocol != "" && entry.Protocol != appwire.ProtocolVersion &&
			(errors.As(err, &mismatch) || (errors.As(err, &wire) && wire.Code == appwire.CodeInvalidRequest)) {
			id := entry.SessionID
			if id == "" {
				id = entry.ThreadID
			}
			if id != "" {
				return ProbeResult{SessionID: id, Status: appwire.ThreadStatusRestartRequired, OK: true}
			}
		}
		return ProbeResult{}
	}
	// Read descendants before the root diagnostics. A retained delegate can be
	// resumed between these calls; taking the child projection first means a
	// later running lifecycle cannot be mistaken for stale active work and then
	// overwritten as idle by an older root snapshot.
	listResponse, err := appClient.ThreadList(ctx, appwire.ThreadListParams{IncludeSubagents: true})
	if err != nil {
		return ProbeResult{}
	}
	rootResponse, err := appClient.ThreadRead(ctx, appwire.ThreadReadParams{})
	if err != nil {
		return ProbeResult{}
	}
	root := rootResponse.Thread
	rootID := statusThreadID(root)
	if strings.TrimSpace(root.ID) == "" || rootID == "" {
		return ProbeResult{}
	}

	// ThreadList carries the root and descendants from one projection cut. Keep
	// the ThreadRead result only for identity validation, and use the matching
	// listed root so its diagnostics and child projections cannot come from
	// different snapshots.
	var listedRoot *appwire.Thread
	seen := make(map[string]bool)
	var runningSubagentIDs []string
	var runningSubagentStates map[string]string
	for i := range listResponse.Data {
		thread := listResponse.Data[i]
		if isRootThread(thread, root) {
			if listedRoot == nil {
				listedRoot = &listResponse.Data[i]
			}
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
	if listedRoot == nil {
		return ProbeResult{}
	}
	root = *listedRoot

	idleStableDelegateChildren := idleStableDelegateChildIDs(root.Evener.Diagnostics)
	for _, id := range runningSubagentIDs {
		if _, quiesced := idleStableDelegateChildren[id]; quiesced {
			if runningSubagentStates == nil {
				runningSubagentStates = make(map[string]string)
			}
			runningSubagentStates[id] = appwire.ThreadStatusIdle
		}
	}
	sort.Strings(runningSubagentIDs)

	runningJobs, completedJobs := splitNonAgentJobs(root.Evener.Diagnostics)
	return ProbeResult{
		SessionID:             rootID,
		Status:                root.Status.Type,
		PendingAsk:            root.Evener.AskPending,
		PendingEscalation:     len(root.Evener.PendingEscalations) > 0,
		RunningSubagentIDs:    runningSubagentIDs,
		RunningSubagentStates: runningSubagentStates,
		RunningJobs:           runningJobs,
		CompletedJobs:         completedJobs,
		OK:                    true,
	}
}

func statusThreadID(thread appwire.Thread) string {
	if sessionID := strings.TrimSpace(thread.SessionID); sessionID != "" {
		return sessionID
	}
	return strings.TrimSpace(thread.ID)
}

func isRootThread(thread, root appwire.Thread) bool {
	return thread.ID == root.ID && statusThreadID(thread) == statusThreadID(root)
}

// idleStableDelegateChildIDs identifies retained stable delegates with no
// current run. Their child thread can retain an older active projection while
// the runtime is kept for a later resume, so the durable delegate lifecycle is
// authoritative for the parent-side navigation state.
func idleStableDelegateChildIDs(diagnostics *appwire.EvenerDiagnostics) map[string]struct{} {
	if diagnostics == nil {
		return nil
	}
	var ids map[string]struct{}
	for _, delegate := range diagnostics.Delegates {
		childID := strings.TrimSpace(delegate.ChildSessionID)
		if childID == "" || strings.TrimSpace(delegate.Lifecycle) != "idle" {
			continue
		}
		if ids == nil {
			ids = make(map[string]struct{})
		}
		ids[childID] = struct{}{}
	}
	return ids
}

func splitNonAgentJobs(diagnostics *appwire.EvenerDiagnostics) ([]appwire.EvenerJobInfo, []appwire.EvenerJobInfo) {
	if diagnostics == nil {
		return nil, nil
	}
	return SplitNonAgentJobs(diagnostics.Jobs)
}

// SplitNonAgentJobs separates non-delegate jobs into active and terminal
// groups for navigation consumers. The input is already the daemon's bounded
// diagnostic inventory, so the function preserves its order within each group.
func SplitNonAgentJobs(jobs []appwire.EvenerJobInfo) ([]appwire.EvenerJobInfo, []appwire.EvenerJobInfo) {
	var running, completed []appwire.EvenerJobInfo
	for _, job := range jobs {
		if strings.TrimSpace(job.JobType) == "delegate" {
			continue
		}
		if terminalJobStatus(job.Status) {
			completed = append(completed, job)
		} else {
			running = append(running, job)
		}
	}
	return running, completed
}

func terminalJobStatus(status string) bool {
	switch strings.TrimSpace(status) {
	case "completed", "failed", "cancelled", "stopped", "exhausted":
		return true
	default:
		return false
	}
}
