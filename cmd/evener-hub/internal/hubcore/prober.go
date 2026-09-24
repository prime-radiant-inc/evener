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
		if errors.As(err, &mismatch) ||
			(entry.Protocol != "" && entry.Protocol != appwire.ProtocolVersion &&
				errors.As(err, &wire) && wire.Code == appwire.CodeInvalidRequest) {
			id := entry.SessionID
			if id == "" {
				id = entry.ThreadID
			}
			if id != "" {
				return ProbeResult{SessionID: id, Status: appwire.ThreadStatusRestartRequired, ProtocolMismatch: true, OK: true}
			}
		}
		return ProbeResult{}
	}
	listResponse, err := appClient.ThreadList(ctx, appwire.ThreadListParams{IncludeSubagents: true, StatusOnly: true})
	if err != nil {
		return ProbeResult{}
	}
	root, ok := probeRootIdentity(ctx, appClient, entry, listResponse.Data)
	if !ok {
		return ProbeResult{}
	}
	rootID := statusThreadID(root)

	// ThreadList carries the root and descendants from one projection cut, so
	// the probe uses the listed root: its diagnostics and the child projections
	// cannot then come from different snapshots.
	var listedRoot *appwire.Thread
	seen := make(map[string]bool)
	var runningSubagentIDs []string
	var runningSubagentStates map[string]string
	var childWatches map[string][]appwire.EvenerWatchInfo
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
		// Each child thread now carries its own watches in its diagnostics (the
		// daemon samples them on read). Record them per child so the tree can
		// attach them to the child's row; a child with none stays absent, which
		// the tree reads as "no watches".
		if watches := diagnosticsWatches(thread.Evener.Diagnostics); len(watches) > 0 {
			if childWatches == nil {
				childWatches = make(map[string][]appwire.EvenerWatchInfo)
			}
			childWatches[id] = append([]appwire.EvenerWatchInfo(nil), watches...)
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
	// Lifecycle status is the explicit typed input to retirement UI: a
	// detached control read that never marks activity. A failed lifecycle
	// probe (older daemon, mid-restart race) clears current capability — it
	// never changes process ownership or this probe's residency verdict.
	lifecycle, lifecycleFresh := probeDaemonLifecycle(ctx, appClient)
	return ProbeResult{
		SessionID:             rootID,
		Status:                root.Status.Type,
		ActiveFlags:           append([]string(nil), root.Status.ActiveFlags...),
		PendingAsk:            root.Evener.AskPending,
		PendingEscalation:     len(root.Evener.PendingEscalations) > 0,
		Capabilities:          root.Evener.Capabilities,
		CapabilitiesKnown:     true,
		RunningSubagentIDs:    runningSubagentIDs,
		RunningSubagentStates: runningSubagentStates,
		RunningJobs:           runningJobs,
		CompletedJobs:         completedJobs,
		Lifecycle:             lifecycle,
		LifecycleFresh:        lifecycleFresh,
		Watches:               diagnosticsWatches(root.Evener.Diagnostics),
		ChildWatches:          childWatches,
		OK:                    true,
	}
}

// probeRootIdentity names the root thread a probe's list answer must carry.
//
// The answer names the answering daemon's session, and a daemon keeps its
// entry's session id current (rvreg.UpdateSessionID). So when the entry names
// its session, the listed row for that session is the root, and a list with no
// such row is another daemon that re-bound the port; no second snapshot is
// needed to tell them apart.
//
// An entry that names no session yet has only the answer to go on. The probe
// then reads the root separately and requires the list to carry the same
// thread, which rejects a daemon swap between the two calls. The read follows
// the list: a retained delegate can be resumed between them, and taking the
// child projection first means a later running lifecycle cannot be mistaken
// for stale active work.
func probeRootIdentity(ctx context.Context, client *appwire.Client, entry rendezvous.Entry, listed []appwire.Thread) (appwire.Thread, bool) {
	if named := strings.TrimSpace(entry.SessionID); named != "" {
		for _, thread := range listed {
			if statusThreadID(thread) == named && strings.TrimSpace(thread.ID) != "" {
				return thread, true
			}
		}
		return appwire.Thread{}, false
	}
	response, err := client.ThreadRead(ctx, appwire.ThreadReadParams{})
	if err != nil {
		return appwire.Thread{}, false
	}
	root := response.Thread
	if strings.TrimSpace(root.ID) == "" || statusThreadID(root) == "" {
		return appwire.Thread{}, false
	}
	return root, true
}

// probeDaemonLifecycle reads the daemon's retirement lifecycle through the
// typed daemon/status contract. Any error means the capability is unknown
// (nil, false) — never guessed from thread state.
func probeDaemonLifecycle(ctx context.Context, client *appwire.Client) (*appwire.DaemonLifecycle, bool) {
	resp, err := client.DaemonStatus(ctx, appwire.DaemonStatusParams{})
	if err != nil {
		return nil, false
	}
	lifecycle := resp.Lifecycle
	return &lifecycle, true
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

// diagnosticsWatches returns a daemon's own live-watch rows. A nil diagnostics
// (old daemon, or a probe that listed nothing) and a diagnostics that omits
// Watches both yield an empty list: absence is never an error. It mirrors
// diagnosticsJobs — the input is already the daemon's bounded watch inventory,
// so no hub-side cap is introduced here.
func diagnosticsWatches(diagnostics *appwire.EvenerDiagnostics) []appwire.EvenerWatchInfo {
	if diagnostics == nil {
		return nil
	}
	return diagnostics.Watches
}

// DiagnosticsWatches is the exported form of diagnosticsWatches, shared with
// package hub's own tree projection so the two cannot drift. It is the same
// logic; the package-local name stays for the existing in-package callers.
func DiagnosticsWatches(diagnostics *appwire.EvenerDiagnostics) []appwire.EvenerWatchInfo {
	return diagnosticsWatches(diagnostics)
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
	case "completed", "failed", "cancelled", "stopped", "exhausted", "command_exited_nonzero", "command_killed":
		return true
	default:
		return false
	}
}
