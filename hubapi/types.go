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
	BootID           string             `json:"boot_id"`
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

// AttentionSummary is the authoritative badge count set. It is the same
// "summary" object evener/attention/changed pushes incrementally, so the JS
// layer applies one field-access path to either the REST baseline or the live
// notification.
type AttentionSummary = appwire.AttentionSummary

type Source struct {
	ID     string `json:"id"`
	Label  string `json:"label"`
	Kind   string `json:"kind"`
	Online bool   `json:"online"`
}

type ErrorResponse struct {
	Error           string `json:"error"`
	Code            int    `json:"code,omitempty"`
	EvenerErrorInfo string `json:"evener_error_info,omitempty"`
}
