package appwire

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// PathsCompleteParams asks for path completions of Prefix. IncludeFiles adds
// regular files to the directory-only default; in that mode directory entries
// come back with a trailing separator so the client can tell the two apart.
type PathsCompleteParams struct {
	Prefix       string `json:"prefix"`
	Limit        int    `json:"limit,omitempty"`
	IncludeFiles bool   `json:"includeFiles,omitempty"`
}

type PathsCompleteResponse struct {
	Data []string `json:"data"`
}

// DirsCreateParams requests creation of a working directory and any missing
// parents for a Spawn preflight.
type DirsCreateParams struct {
	Path string `json:"path"`
}

type DirsCreateResponse struct {
	Path    string `json:"path"`
	Created bool   `json:"created"`
}

// ProjectsRecentParams selects how many recent project directories the hub
// returns. Limit <= 0 means the hub default (the session creation flows'
// 15-option dropdown cap).
type ProjectsRecentParams struct {
	Limit int `json:"limit,omitempty"`
}

// ProjectsRecentResponse lists distinct project working directories ordered
// by actual recency of use (most recently active session first).
type ProjectsRecentResponse struct {
	Data []string `json:"data"`
}

type PathValidateParams struct {
	Path string `json:"path"`
	Kind string `json:"kind,omitempty"`
}

type PathValidateResponse struct {
	Path  string `json:"path"`
	Valid bool   `json:"valid"`
	Error string `json:"error,omitempty"`
}

// GitHeadParams selects the working directory whose git HEAD should be read.
type GitHeadParams struct {
	CWD string `json:"cwd"`
	// IncludeOrigin asks the hub to also report the repo's sanitized "origin"
	// remote URL. It is opt-in because the method's other caller (the Spawn
	// pane's branch chip) renders only the branch: without it the hub neither
	// runs the origin lookup nor sends the remote anywhere.
	IncludeOrigin bool `json:"includeOrigin,omitempty"`
}

// GitHeadResponse reports a branch name, detached short SHA, or an empty
// string when the working directory has no readable git HEAD, plus the
// "origin" remote URL when one is configured. OriginURL is display metadata
// for callers that link the branch to its forge repo page; git or
// configuration failures leave it empty alongside a possibly-present Head.
type GitHeadResponse struct {
	Head      string `json:"head"`
	OriginURL string `json:"originUrl,omitempty"`
}

// MobilePairingParams supplies the authenticated web application's explicit
// origin. The hub validates it before embedding its auth token in a pairing
// URL, unless configured PublicURL takes precedence.
type MobilePairingParams struct {
	Origin string `json:"origin"`
}

// MobilePairingResponse carries the reusable /auth bootstrap URL scanned by
// the phone.
type MobilePairingResponse struct {
	AuthURL string `json:"authUrl"`
}

type HarnessListParams struct{}

type HarnessDescriptor struct {
	ID                             string `json:"id"`
	Label                          string `json:"label"`
	Kind                           string `json:"kind,omitempty"`
	EmptyTaskUnsupportedReason     string `json:"emptyTaskUnsupportedReason,omitempty"`
	EmptyTaskUnsupportedNextAction string `json:"emptyTaskUnsupportedNextAction,omitempty"`
}

type HarnessListResponse struct {
	Data []HarnessDescriptor `json:"data"`
}

type UpgradeParams struct {
	Requested string `json:"requested,omitempty"`
}

type UpgradeResponse struct {
	Release        string   `json:"release"`
	Channel        string   `json:"channel"`
	URL            string   `json:"url"`
	Archive        string   `json:"archive"`
	Prefix         string   `json:"prefix"`
	BinDir         string   `json:"binDir"`
	ShareBinDir    string   `json:"shareBinDir"`
	Installed      []string `json:"installed"`
	RestartMessage string   `json:"restartMessage"`
}

// UpdateCheckParams selects the channel to compare the running build against.
// Empty means the running binary's own upgrade channel.
type UpdateCheckParams struct {
	Channel string `json:"channel,omitempty"`
}

// UpdateCheckResponse reports the running build and what the channel points
// at. Applicable is false for dev builds, which are never self-updated; the
// Latest* fields are empty then and no network request was made.
type UpdateCheckResponse struct {
	Channel                    string    `json:"channel"`
	BuildChannel               string    `json:"buildChannel"`
	CurrentVersion             string    `json:"currentVersion"`
	CurrentCommit              string    `json:"currentCommit"`
	LatestTag                  string    `json:"latestTag,omitempty"`
	LatestCommit               string    `json:"latestCommit,omitempty"`
	UpdateAvailable            bool      `json:"updateAvailable"`
	Applicable                 bool      `json:"applicable"`
	OnDiskCommit               string    `json:"onDiskCommit,omitempty"`
	OnDiskError                string    `json:"onDiskError,omitempty"`
	InstallAvailable           bool      `json:"installAvailable"`
	RestartPending             bool      `json:"restartPending"`
	StartedAt                  time.Time `json:"startedAt"`
	RestartSupported           bool      `json:"restartSupported"`
	CheckError                 string    `json:"checkError,omitempty"`
	InstallTarget              string    `json:"installTarget,omitempty"`
	InstallTargetMatchesLaunch bool      `json:"installTargetMatchesLaunch"`
}

// UpdateApplyParams selects the channel to install. Empty means the running
// binary's own upgrade channel.
type UpdateApplyParams struct {
	Channel string `json:"channel,omitempty"`
}

// UpdateApplyResponse is returned just before the hub execs the installed
// binary in place; Restarting is always true on success.
type UpdateApplyResponse struct {
	Release    string   `json:"release"`
	Channel    string   `json:"channel"`
	Installed  []string `json:"installed"`
	Restarting bool     `json:"restarting"`
}

// HubRestartParams selects nothing: restart the hub into the binary at its
// recorded launch path.
type HubRestartParams struct{}

// HubRestartResponse is returned just before the hub execs the on-disk
// binary. Incarnation is the old process's health started_at value.
type HubRestartResponse struct {
	Restarting     bool      `json:"restarting"`
	Incarnation    time.Time `json:"incarnation"`
	CurrentVersion string    `json:"currentVersion"`
	TargetVersion  string    `json:"targetVersion"`
}

type AuthStatusParams struct {
	Provider string `json:"provider"`
}

const (
	AuthTestStatusSuccess              = "success"
	AuthTestStatusMissing              = "missing"
	AuthTestStatusAuthRejected         = "auth_rejected"
	AuthTestStatusEndpointFailure      = "endpoint_failure"
	AuthTestStatusConfigurationFailure = "configuration_failure"
	AuthTestStatusUnsupported          = "unsupported"
)

type AuthTestParams struct {
	Provider string `json:"provider"`
	// ExpectedEndpointFingerprint is the endpoint this client reviewed Provider
	// against (InstanceEntry.endpointFingerprint), when it captured one. The hub
	// validates it against the configuration the probe will dial and refuses the
	// check rather than send the stored credential to an endpoint the client
	// never showed. Empty asserts nothing, which is what the TUI's credential
	// panel always sends.
	ExpectedEndpointFingerprint string `json:"expectedEndpointFingerprint,omitempty"`
}

type AuthTestResponse struct {
	Provider string `json:"provider"`
	Status   string `json:"status"`
	Message  string `json:"message"`
}

type AuthStatusResponse struct {
	Provider       string   `json:"provider"`
	Supported      bool     `json:"supported"`
	SignedIn       bool     `json:"signedIn"`
	ActiveSource   string   `json:"activeSource"`
	AuthModes      []string `json:"authModes,omitempty"`
	HasStoredOAuth bool     `json:"hasStoredOAuth"`
	// HasStoredFile is true when a key exists in credentials.toml.
	HasStoredFile bool `json:"hasStoredFile,omitempty"`
	// EnvVar is the name of the env var that supplies a key, when present.
	EnvVar string `json:"envVar,omitempty"`
	// ShadowedEnvVar names an environment variable that is set but loses to
	// a higher-precedence credential (api_key, credential_headers, or
	// store, spec §10); empty when no such variable is set, including when
	// an env source is itself what resolves.
	ShadowedEnvVar string `json:"shadowedEnvVar,omitempty"`
	Email          string `json:"email,omitempty"`
	StoredEmail    string `json:"storedEmail,omitempty"`
	AccountID      string `json:"accountId,omitempty"`
	WorkspaceID    string `json:"workspaceId,omitempty"`
	NeedsRefresh   bool   `json:"needsRefresh,omitempty"`
	NeedsLogin     bool   `json:"needsLogin,omitempty"`
	// RefreshRejected is true when the issuer permanently refused this
	// instance's stored refresh token (authopenai.RefreshRejected, #2479):
	// the access token may still be valid, but the session needs a fresh
	// sign-in before its next refresh. It is why NeedsLogin can be true for a
	// record whose access token has not expired, so a client must not read
	// NeedsLogin alone as "the access token expired".
	RefreshRejected bool   `json:"refreshRejected,omitempty"`
	Error           string `json:"error,omitempty"`
	// ConfigRevision is this instance's effective credential-configuration
	// revision: a stable, keyed MAC the host re-resolves from the same state a
	// credential write lands in (cmd/evener-hub/app_auth.go +
	// hubcore.CredentialConfigRevision). It is keyed with the hub-held secret
	// the endpoint fingerprints use, so a reader who can see it cannot recover a
	// secret the covered destination carries - a base URL can hold one in its
	// userinfo or query string, which this field must not expose. The remote
	// credential push captures it from this read and echoes it as
	// ApiKeyConditionalSetParams.ExpectedRevision, so the host can refuse a
	// write prepared against a configuration that has since changed. It is
	// deliberately empty (JSON-omitted) when the host cannot resolve the
	// instance or cannot key a revision: the zero value asserts no revision
	// fence to a client, but a host that cannot key one refuses the conditional
	// set rather than reading that zero as permission, and the source fence
	// still applies.
	ConfigRevision string `json:"configRevision,omitempty"`
}

type AuthLoginStartParams struct {
	Provider string `json:"provider"`
}

type AuthLoginStartResponse struct {
	Provider string `json:"provider"`
	FlowID   string `json:"flowId"`
	URL      string `json:"url"`
}

type AuthLoginCompleteParams struct {
	Provider    string `json:"provider"`
	FlowID      string `json:"flowId"`
	RedirectURL string `json:"redirectUrl"`
	// OriginClientId is the client identity the hub echoes into the
	// evener/auth/updated broadcast this login completion triggers, so the
	// originator can recognize its own echo by id instead of by provider plus
	// timing. Optional: empty (an older build, the TUI) leaves the broadcast
	// without an id and consumers on the provider-plus-timing fallback.
	OriginClientId string `json:"originClientId,omitempty"`
}

// AuthLoginCompleteResponse reports a completed login. Restart is true when the
// hub could not check the flow against the endpoint it was started for, so it
// discarded the flow and saved nothing; Status is then empty and the client
// starts a fresh login (evener/auth/login/start), which binds to the endpoint
// the name resolves to now.
type AuthLoginCompleteResponse struct {
	Status  AuthStatusResponse `json:"status"`
	Restart bool               `json:"restart,omitempty"`
}

type AuthLogoutParams struct {
	Provider string `json:"provider"`
	// ExpectedEndpointFingerprint is the endpoint this client showed the user
	// for Provider (InstanceEntry.endpointFingerprint). The hub refuses the
	// logout when the name resolves to a different endpoint by the time it
	// lands: the sign-out was confirmed for the row the client listed, and a
	// name another client has re-pointed since belongs to a different
	// instance. Empty asserts nothing, which is what a client that never saw
	// an endpoint sends.
	ExpectedEndpointFingerprint string `json:"expectedEndpointFingerprint,omitempty"`
	// OriginClientId is the client identity the hub echoes into the
	// evener/auth/updated broadcast this logout triggers, so the originator
	// can recognize its own echo by id instead of by provider plus timing.
	// Optional: empty (an older build, the TUI) leaves the broadcast without
	// an id and consumers on the provider-plus-timing fallback.
	OriginClientId string `json:"originClientId,omitempty"`
}

type AuthLogoutResponse struct {
	Removed bool               `json:"removed"`
	Status  AuthStatusResponse `json:"status"`
}

type ModelListParams struct {
	Harness string `json:"harness,omitempty"`
	CWD     string `json:"cwd,omitempty"`
	// Refresh waits for a live listing instead of serving a cached catalogue.
	// Concurrent live listings for the same scope may share the request.
	Refresh bool `json:"refresh,omitempty"`
}

type ModelDescriptor struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
	// The remaining fields are optional because daemon and remote-source model
	// lists may know only the launchable identity. Hub responses fill these from
	// the embedded catalog when available. Pointer scalars preserve an explicit
	// false/zero from a live provider instead of treating it as unknown.
	DisplayName           string   `json:"displayName,omitempty"`
	ContextWindow         *int     `json:"contextWindow,omitempty"`
	MaxInputTokens        *int     `json:"maxInputTokens,omitempty"`
	SupportsTools         *bool    `json:"supportsTools,omitempty"`
	SupportsVision        *bool    `json:"supportsVision,omitempty"`
	MaxOutputTokens       *int     `json:"maxOutputTokens,omitempty"`
	SupportsWebSearch     *bool    `json:"supportsWebSearch,omitempty"`
	SupportsReasoning     *bool    `json:"supportsReasoning,omitempty"`
	InputCostPerMillion   *float64 `json:"inputCostPerMillion,omitempty"`
	OutputCostPerMillion  *float64 `json:"outputCostPerMillion,omitempty"`
	ReasoningEffortLevels []string `json:"reasoningEffortLevels,omitempty"`
	// Warnings carries the registry's resolved-row notes (e.g. a global-only
	// model under a regional Vertex location) so the model picker can flag a
	// row the resolver itself warns about.
	Warnings []string `json:"warnings,omitempty"`
	// InstanceDefault marks the row as its instance's configured default in
	// the instance's own usable current listing. It is not the global default
	// and not proof of generation access; an absent marker authorizes no
	// automatic selection.
	InstanceDefault bool `json:"instanceDefault,omitempty"`
}

type ModelListDiagnostic struct {
	Provider string `json:"provider,omitempty"`
	Source   string `json:"source,omitempty"`
	Title    string `json:"title,omitempty"`
	Message  string `json:"message"`
	Hint     string `json:"hint,omitempty"`
}

type ModelListResponse struct {
	Data        []ModelDescriptor     `json:"data"`
	Diagnostics []ModelListDiagnostic `json:"diagnostics,omitempty"`
	// Recent carries the model picker's "Recent" group: the last N distinct
	// models across all sessions, globally by recency (not scoped to the
	// currently selected harness/project), derived from the Past index. Empty
	// on a fresh install with no session history. A struct field, not a new
	// appwire method — no dual-router catalog change required.
	Recent []ModelDescriptor `json:"recent,omitempty"`
}

type EmptyResponse struct{}

type ThreadStatusChangedParams struct {
	ThreadID string       `json:"threadId"`
	Ref      string       `json:"ref"`
	Status   ThreadStatus `json:"status"`
	// FailedToolCalls carries the session's running failure count (see
	// EvenerThread.FailedToolCalls) so a client WATCHING a session sees it move.
	// The figure is otherwise snapshot-only, refreshed by thread/read — which
	// means a session that was clean when the client attached and failed later
	// would keep saying nothing, which is precisely the reader the count was
	// built for. Every status transition is a turn boundary, so riding along
	// here refreshes it exactly when it can have changed, with no polling.
	//
	// ABSENT MEANS "NO UPDATE", not "nobody counted" — an old daemon omits it,
	// and a client that cleared its count on absence would blank a figure the
	// hydrate legitimately gave it. Absence at HYDRATE is where "nobody
	// counted" is expressed.
	FailedToolCalls *int `json:"failedToolCalls,omitempty"`
	// AskPending carries EvenerThread.AskPending — "this session is waiting on
	// a human answer" — for the reason the failure count rides along above: it
	// is otherwise snapshot-only, so after the user answers, every OTHER
	// client keeps showing "question waiting" until its next read (#1613). The
	// pending set clears only at a turn boundary (a resolving user turn, an
	// interrupted turn: agent/session_tools_ask.go), which is exactly when a
	// status change is announced, so this refreshes it when it can have moved
	// and never polls.
	//
	// ABSENT MEANS "NO UPDATE" (an old daemon omits it), never "no question
	// waiting": a client that cleared the flag on absence would stop showing a
	// question the hydrate legitimately gave it.
	AskPending *bool `json:"askPending,omitempty"`
	// Capabilities carries the action set that goes WITH the status being
	// announced (see EvenerThread.Capabilities), for the same reason the failure
	// count rides along above: it is otherwise snapshot-only, and Send is the
	// entry defined by whether a turn is in flight (Steer, Interrupt and Queue
	// advertise harness support and do not move with the status, #1363/#1375).
	// A client that read the thread while it was idle therefore holds send=true
	// for the whole turn that follows, and renders a session it KNOWS is active
	// with a Send it must not offer until the page is reloaded (kata 06t8). A
	// status transition is exactly when Send flips, so the set refreshes there
	// and nowhere else — no polling, no re-read of the transcript.
	//
	// ABSENT MEANS "NO UPDATE", same as the count. Non-local/source-backed
	// threads may omit capabilities their source does not advertise. A client
	// that cleared its set on absence would strip a session of every action its
	// hydrate legitimately advertised.
	//
	// A CLOSE frame is the one status a daemon does not fill in: what a thread
	// can still be asked to do once its daemon is gone is the hub's answer, not
	// the departing daemon's. The hub stamps that frame as it relays it
	// (cmd/evener-hub/app_relay.go's stampClosedThreadCapabilities), so a close
	// still arrives carrying a set — the same one the next thread/read returns.
	// A client that read the daemon's own all-false set there would lose the
	// follow-up composer for a session the hub would happily resume (kata pk2d).
	Capabilities *ThreadCapabilities `json:"capabilities,omitempty"`
	// ActiveTurnID is the currently running turn's id, empty when none. See
	// EvenerThread.ActiveTurnID.
	ActiveTurnID string `json:"activeTurnId,omitempty"`
}

// ThreadModelRetryParams is the params shape for the evener/thread/modelRetry
// notification: the session's model call failed with a retryable error and will
// be tried again after DelayMS.
//
// Thread-scoped and deliberately item-less. One rate-limited session produced 91
// retries in four hours; as transcript items that is noise, and the reader's
// actual question ("is this alive, and when does it come back?") is a question
// about now, not about history. Clients render it as ephemeral liveness state,
// which the next retry or the turn's settlement supersedes.
//
// Attempt counts retries, so the first retry is 1. MaxAttempts is the whole
// budget including the initial try, so "attempt 9 of 11" renders without the
// client knowing the retry policy.
//
// AttemptCap is the honest denominator to render instead of MaxAttempts once
// it differs: the full policy budget until the current retry group has a
// consume-phase failure, then the early-stop bound that will actually govern
// it. Both are zero when a rate limit is being retried against a wall-clock
// budget instead of an attempt count, so clients should render the bare attempt
// number. GroupElapsedMS is wall-clock time since the retry group's first
// attempt (one model call), so a client can render how long the call has
// been running.
type ThreadModelRetryParams struct {
	ThreadID       string `json:"threadId"`
	Ref            string `json:"ref"`
	TurnID         string `json:"turnId,omitempty"`
	Attempt        int    `json:"attempt"`
	MaxAttempts    int    `json:"maxAttempts"`
	DelayMS        int64  `json:"delayMs"`
	ErrorClass     string `json:"errorClass,omitempty"`
	StatusCode     int    `json:"statusCode,omitempty"`
	Message        string `json:"message,omitempty"`
	Model          string `json:"model,omitempty"`
	GroupElapsedMS int64  `json:"groupElapsedMs"`
	AttemptCap     int    `json:"attemptCap"`
}

// ThreadStartedParams is the params shape for the thread/started
// notification: the new session's initial Thread snapshot, so a client can
// render the session without a follow-up thread/read.
type ThreadStartedParams struct {
	ThreadID string `json:"threadId"`
	Ref      string `json:"ref"`
	Thread   Thread `json:"thread"`
}

// ThreadClosedParams is the params shape for the thread/closed notification.
// Reason is the session's shutdown reason, empty when the source reported
// none.
type ThreadClosedParams struct {
	ThreadID string `json:"threadId"`
	Ref      string `json:"ref"`
	Reason   string `json:"reason,omitempty"`
}

type ThreadResyncParams struct {
	ThreadID string `json:"threadId"`
	Ref      string `json:"ref"`
	// BootGeneration is the publishing daemon's boot generation for this
	// thread, as on HistoryUpdatedParams. Empty on a resync the hub pushes
	// itself. See CompareBootGeneration.
	BootGeneration string `json:"bootGeneration,omitempty"`
	// Epoch is the history epoch a client should resync to, when known.
	Epoch uint64 `json:"epoch,omitempty"`
}

// WarningParams is the params shape for the warning notification: a
// non-fatal diagnostic, also used for cancelled turns and relay-attach
// failures. Message/Source/Title/Hint are the human-facing diagnostic;
// Warning carries the raw producer-side event payload and Cause the
// structured error cause (present only on the cancelled-turn path), neither
// of which has a UI consumer today.
//
// A genuine turn failure sends no warning at all — only a failed
// turn/completed carrying the same diagnostic on its TurnError — so clients
// that render both channels do not show one error twice.
type WarningParams struct {
	ThreadID string           `json:"threadId"`
	Ref      string           `json:"ref"`
	Message  string           `json:"message,omitempty"`
	Source   string           `json:"source,omitempty"`
	Code     string           `json:"code,omitempty"`
	Title    string           `json:"title,omitempty"`
	Hint     string           `json:"hint,omitempty"`
	Warning  any              `json:"warning,omitempty"`
	Cause    *DiagnosticCause `json:"cause,omitempty"`
}

// EvenerJobParams is the params shape shared by the evener/job/started and
// evener/job/finished notifications. Both carry the same envelope around a
// EvenerJobInfo; which of its fields are populated is what differs (a finished
// job adds status/reason/exitCode/output), so one type describes both.
type EvenerJobParams struct {
	ThreadID string        `json:"threadId"`
	Ref      string        `json:"ref"`
	Job      EvenerJobInfo `json:"job"`
}

// EvenerAuthUpdatedParams is the params shape for the evener/auth/updated
// notification. Provider and ActiveSource are absent when the broadcast
// follows a provider-instance mutation, which no single provider/activeSource
// pair honestly summarizes; clients treat this notification as
// payload-agnostic ("credentials or instances changed, refetch") either way.
type EvenerAuthUpdatedParams struct {
	Provider     string `json:"provider,omitempty"`
	ActiveSource string `json:"activeSource,omitempty"`
	// Reason names a broadcast that did not change credentials or provider
	// configuration. The only value today is "models_changed": a background
	// launch-model refresh produced a different list than a picker was served,
	// so clients re-read their model catalog. It is catalog-only: no credential,
	// instance or destination changed. Every credential write and
	// provider-instance CRUD broadcast leaves it empty, and consumers must treat
	// those empty-reason broadcasts as changes even when they carry no other
	// payload.
	Reason string `json:"reason,omitempty"`
	// OriginClientId is the echoed OriginClientId of the mutation that caused
	// this broadcast - an auth write or a provider-instance CRUD change - the
	// identity of the client whose change it announces, so that client can
	// recognize its own echo by id instead of by provider plus timing.
	// Optional: a mutation from a client that sends none (an older build, the
	// TUI) leaves the broadcast without an id. A consumer matching a broadcast
	// against its own mutation then falls back to provider-plus-timing
	// correlation for a provider-bearing auth echo only; an id-less
	// provider-instance broadcast names no provider to correlate on and is
	// treated as a foreign change.
	OriginClientId string `json:"originClientId,omitempty"`
}

// NavigationTargetKind identifies one exact invalidation target variant.
type NavigationTargetKind string

const (
	NavigationTargetManifest          NavigationTargetKind = "manifest"
	NavigationTargetSection           NavigationTargetKind = "section"
	NavigationTargetPinCatalog        NavigationTargetKind = "pin_catalog"
	NavigationTargetPinSection        NavigationTargetKind = "pin_section"
	NavigationTargetCatalog           NavigationTargetKind = "catalog"
	NavigationTargetProject           NavigationTargetKind = "project"
	NavigationTargetAllLoadedProjects NavigationTargetKind = "all_loaded_projects"
)

// AllNavigationTargetKinds is the complete wire-level target-kind set.
var AllNavigationTargetKinds = []NavigationTargetKind{
	NavigationTargetManifest,
	NavigationTargetSection,
	NavigationTargetPinCatalog,
	NavigationTargetPinSection,
	NavigationTargetCatalog,
	NavigationTargetProject,
	NavigationTargetAllLoadedProjects,
}

// NavigationInvalidationTarget identifies one loaded navigation resource that
// clients must revalidate. Revision is omitted only by the wildcard target.
type NavigationInvalidationTarget struct {
	Kind       NavigationTargetKind `json:"kind"`
	Section    string               `json:"section,omitempty"`
	SectionID  string               `json:"sectionId,omitempty"`
	Catalog    string               `json:"catalog,omitempty"`
	ProjectKey string               `json:"projectKey,omitempty"`
	Revision   uint64               `json:"revision,omitempty"`
}

// MarshalJSON emits only the fields valid for target's kind. Every scoped
// target includes a revision, including revision zero; the wildcard has no
// revision or selector.
func (target NavigationInvalidationTarget) MarshalJSON() ([]byte, error) {
	if err := target.validate(false); err != nil {
		return nil, err
	}
	if target.Kind == NavigationTargetAllLoadedProjects {
		return json.Marshal(struct {
			Kind NavigationTargetKind `json:"kind"`
		}{Kind: target.Kind})
	}
	return json.Marshal(struct {
		Kind       NavigationTargetKind `json:"kind"`
		Section    string               `json:"section,omitempty"`
		SectionID  string               `json:"sectionId,omitempty"`
		Catalog    string               `json:"catalog,omitempty"`
		ProjectKey string               `json:"projectKey,omitempty"`
		Revision   uint64               `json:"revision"`
	}{
		Kind:       target.Kind,
		Section:    target.Section,
		SectionID:  target.SectionID,
		Catalog:    target.Catalog,
		ProjectKey: target.ProjectKey,
		Revision:   target.Revision,
	})
}

// UnmarshalJSON rejects unknown fields and every invalid target variant so an
// incomplete or widened invalidation cannot silently reach a client.
func (target *NavigationInvalidationTarget) UnmarshalJSON(data []byte) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	if fields == nil {
		return errors.New("navigation invalidation target must be an object")
	}
	for name := range fields {
		switch name {
		case "kind", "section", "sectionId", "catalog", "projectKey", "revision":
		default:
			return fmt.Errorf("navigation invalidation target has unknown field %q", name)
		}
	}
	var decoded struct {
		Kind       NavigationTargetKind `json:"kind"`
		Section    string               `json:"section"`
		SectionID  string               `json:"sectionId"`
		Catalog    string               `json:"catalog"`
		ProjectKey string               `json:"projectKey"`
		Revision   uint64               `json:"revision"`
	}
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	_, hasRevision := fields["revision"]
	if decoded.Kind == NavigationTargetAllLoadedProjects && hasRevision {
		return fmt.Errorf("navigation target %q permits no selector or revision", decoded.Kind)
	}
	candidate := NavigationInvalidationTarget{
		Kind:       decoded.Kind,
		Section:    decoded.Section,
		SectionID:  decoded.SectionID,
		Catalog:    decoded.Catalog,
		ProjectKey: decoded.ProjectKey,
		Revision:   decoded.Revision,
	}
	if err := candidate.validate(!hasRevision); err != nil {
		return err
	}
	*target = candidate
	return nil
}

func (target NavigationInvalidationTarget) validate(revisionMissing bool) error {
	if target.Kind == NavigationTargetAllLoadedProjects {
		if target.Section != "" || target.SectionID != "" || target.Catalog != "" || target.ProjectKey != "" || target.Revision != 0 {
			return fmt.Errorf("navigation target %q permits no selector or revision", target.Kind)
		}
		return nil
	}
	if revisionMissing {
		return fmt.Errorf("navigation target %q requires revision", target.Kind)
	}
	switch target.Kind {
	case NavigationTargetManifest, NavigationTargetPinCatalog:
		if target.Section != "" || target.SectionID != "" || target.Catalog != "" || target.ProjectKey != "" {
			return fmt.Errorf("navigation target %q permits no selector", target.Kind)
		}
	case NavigationTargetSection:
		if target.Section == "" || target.SectionID != "" || target.Catalog != "" || target.ProjectKey != "" {
			return fmt.Errorf("navigation target %q requires only section", target.Kind)
		}
	case NavigationTargetPinSection:
		if target.Section != "" || target.SectionID == "" || target.Catalog != "" || target.ProjectKey != "" {
			return fmt.Errorf("navigation target %q requires only sectionId", target.Kind)
		}
	case NavigationTargetCatalog:
		if target.Section != "" || target.SectionID != "" || target.Catalog == "" || target.ProjectKey != "" {
			return fmt.Errorf("navigation target %q requires only catalog", target.Kind)
		}
	case NavigationTargetProject:
		if target.Section != "" || target.SectionID != "" || target.Catalog != "" || target.ProjectKey == "" {
			return fmt.Errorf("navigation target %q requires only projectKey", target.Kind)
		}
	default:
		return fmt.Errorf("unknown navigation target kind %q", target.Kind)
	}
	return nil
}

// NavigationInvalidatedPayload is the evener/navigation/invalidated
// notification body. Sequence orders notifications within GenerationID.
type NavigationInvalidatedPayload struct {
	GenerationID string                         `json:"generationId"`
	Sequence     uint64                         `json:"sequence"`
	Targets      []NavigationInvalidationTarget `json:"targets"`
}

// EvenerLaunchUpdatedParams is the params shape for the evener/launch/updated
// notification: which working directory's launch config changed, and at
// which layer.
type EvenerLaunchUpdatedParams struct {
	CWD   string `json:"cwd"`
	Layer string `json:"layer"`
}

// NotificationRef carries just the routing fields shared by most
// notifications (ref + threadId). Use it when you only need to know which
// session a notification belongs to.
type NotificationRef struct {
	Ref      string `json:"ref"`
	ThreadID string `json:"threadId"`
}

// EmptyParams is the typed-empty params shape used by methods that take none.
type EmptyParams struct{}

type LaunchOptionChoice struct {
	Value    string `json:"value"`
	Label    string `json:"label"`
	Disabled bool   `json:"disabled,omitempty"`
	Hint     string `json:"hint,omitempty"`
}

type LaunchOptionEnvFallback struct {
	Name string `json:"name"`
}

type LaunchOption struct {
	Field               string                   `json:"field"`
	WireField           string                   `json:"wireField"`
	Label               string                   `json:"label"`
	Description         string                   `json:"description,omitempty"`
	Group               string                   `json:"group"`
	Kind                string                   `json:"kind"`
	PathKind            string                   `json:"pathKind,omitempty"`
	Repeatable          bool                     `json:"repeatable,omitempty"`
	DefaultableLayers   []string                 `json:"defaultableLayers,omitempty"`
	PerLaunch           bool                     `json:"perLaunch"`
	DebugOnly           bool                     `json:"debugOnly,omitempty"`
	EnvFallback         *LaunchOptionEnvFallback `json:"envFallback,omitempty"`
	Choices             []LaunchOptionChoice     `json:"choices,omitempty"`
	DriverSupport       map[string]bool          `json:"driverSupport,omitempty"`
	BuiltinDefault      string                   `json:"builtinDefault,omitempty"`
	BuiltinDefaultInt   *int                     `json:"builtinDefaultInt,omitempty"`
	BuiltinDefaultBool  *bool                    `json:"builtinDefaultBool,omitempty"`
	BuiltinDefaultLabel string                   `json:"builtinDefaultLabel,omitempty"`
}

type LaunchOptionSchemaResponse struct {
	Options  []LaunchOption    `json:"options"`
	Excluded map[string]string `json:"excluded,omitempty"`
}
