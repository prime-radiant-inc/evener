package hubapi

import (
	"time"

	"primeradiant.com/evener/appwire"
)

// MobileAPIVersion is the REST contract version implemented by the dedicated
// mobile client. Pairing requires an exact match with HealthResponse.
const MobileAPIVersion = 1

// HealthResponse is returned by GET /api/health.
type HealthResponse struct {
	Version          string             `json:"version"`
	MobileAPIVersion int                `json:"mobile_api_version"`
	StartedAt        time.Time          `json:"started_at"`
	HubAddr          string             `json:"hub_addr"`
	RunDir           string             `json:"run_dir,omitempty"`
	StateGlob        string             `json:"state_glob,omitempty"`
	BackendGitSha    string             `json:"backend_git_sha,omitempty"`
	FrontendHash     string             `json:"frontend_hash,omitempty"`
	Capabilities     HealthCapabilities `json:"capabilities"`
}

type HealthCapabilities struct {
	TranscriptFollow bool `json:"transcript_follow"`
	Fork             bool `json:"fork"`
	RemoteSources    bool `json:"remote_sources"`
}

// AttentionSummary is the authoritative badge count set: how many live,
// top-level, not-manually-archived sessions need attention, are erroring, or
// are working — the NeedsYou tier's eligibility set (only an explicit archive
// decision suppresses; age never decays attention). The web client drives the
// tab title from needsYou+error (frontend/src/notifications/title.ts) and the
// favicon badge dot from all three counts
// (frontend/src/notifications/favicon.ts) on baseline load. It is the same
// "summary" object evener/attention/changed pushes incrementally
// (appwire.AttentionChangedPayload.Summary), so the JS layer applies one
// field-access path to either the REST baseline or the live notification.
type AttentionSummary = appwire.AttentionSummary

type Source struct {
	ID     string `json:"id"`
	Label  string `json:"label"`
	Kind   string `json:"kind"`
	Online bool   `json:"online"`
}
type SessionCapabilities struct {
	Send        bool `json:"send"`
	Steer       bool `json:"steer"`
	Interrupt   bool `json:"interrupt"`
	Compact     bool `json:"compact"`
	Clear       bool `json:"clear"`
	Fork        bool `json:"fork"`
	Resume      bool `json:"resume"`
	Shutdown    bool `json:"shutdown"`
	ChangeModel bool `json:"change_model"`
	// Queue mirrors appwire.ThreadCapabilities.Queue (kata 111a). True when
	// the daemon will accept turn/queue while a turn is in flight; gates the
	// composer's queue affordance on the web UI.
	Queue          bool   `json:"queue"`
	ReadOnlyReason string `json:"read_only_reason,omitempty"`
}

type ErrorResponse struct {
	Error           string `json:"error"`
	Code            int    `json:"code,omitempty"`
	EvenerErrorInfo string `json:"evener_error_info,omitempty"`
}
