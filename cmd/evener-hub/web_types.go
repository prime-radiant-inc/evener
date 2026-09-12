package hub

import (
	"sync"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/hubapi"
)

// modelsCache is a per-WebServer TTL cache of the raw live model list. Provider
// configuration overlays are applied to fresh descriptors on each response.
type modelsCache struct {
	mu      sync.Mutex
	expires time.Time
	models  []appwire.ModelDescriptor
}

const liveModelsTTL = 5 * time.Minute

// WorkspaceData is the template data for the workspace partial.
type WorkspaceData struct {
	ID          string
	SourceLabel string
	Title       string
	// OOBTitle, when true, makes the input_status partial also emit an
	// out-of-band swap of the header's #workspace-session-title span. Only the
	// polled /state response sets this true; the inline workspace render
	// leaves it at its zero value so the title renders exactly once.
	OOBTitle              bool
	Branch                string
	WorkingDir            string
	Worktree              string
	HomeDir               string
	State                 string
	StateLabel            string
	TurnCount             int
	Model                 string
	ContextWindow         int
	ContextPercent        int
	ContextNumbers        string
	CompactContextNumbers string
	Cost                  string
	ActiveTurnID          string
	RunningFor            string
	// WorkMillis, Usage, and ActiveTurnStartedAt mirror appwire.EvenerThread's
	// working-state/token metrics (WS2). Usage is nil when no token data is
	// available (fresh session, old daemon, or a source-backed thread).
	WorkMillis          int64
	Usage               *appwire.EvenerUsage
	ActiveTurnStartedAt int64
	ShowSidebarToggle   bool
	ThreadDocumentMode  bool
	// GoalStatus/GoalIterations mirror appwire.GoalState for the live goal
	// status pill in the input strip. Empty/zero when no goal is set (e.g. past
	// sessions). There is no iteration cap, so only status and turn count show.
	GoalStatus     string
	GoalIterations int
	Capabilities   hubapi.SessionCapabilities
	// Fork lineage for the preserved-original side of a fork. Non-empty
	// only when this session's meta carries ForkLabel — i.e., it's the
	// dim, snapshotted original. ForkOfTitle is the title of the new
	// branch (the session whose ParentSessionID == this.ID); empty if the
	// new branch is not in the past index.
	ForkLabel      string
	ForkOfTitle    string
	DivergenceTurn int
	// Subagent lineage for the breadcrumb banner (mockup #9). Non-empty only
	// when this session is a subagent with a known parent. ParentRouteID is the
	// /s/<id> route to the parent's workspace; ParentTitle is its display name.
	// The banner gives a subagent a way back to its parent — without it,
	// "view →" was a one-way hard nav with no back-out.
	ParentRouteID string
	ParentTitle   string
	// ObserverRouteIDs are the /s/<id> route ids of this worker's LIVE observer
	// subagents (sessions running a job_watch sidecar on this one). The agent
	// stamps them on the worker's meta at watch-install time (SessionMeta.
	// ObservedBy); workspaceData filters that to the live set. The template
	// renders them as data-observers on #conversation so the renderer can
	// auto-open each observer beside this worker. Local sources only — non-local
	// threads have no jobstore and so never carry observers.
	ObserverRouteIDs []string
}

// daemonStatus is the subset of a daemon AppWire thread snapshot used by the
// legacy workspace template projection.
type daemonStatus struct {
	SessionID           string
	Model               string
	Profile             string
	State               string
	Turns               int
	WorkingDir          string
	ContextPressure     float64
	ContextUsed         int
	ContextWindow       int
	ContextRemaining    int
	WorkMillis          int64
	Usage               *appwire.EvenerUsage
	Cost                string
	ActiveTurnStartedAt int64
}
