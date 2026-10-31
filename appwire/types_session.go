package appwire

import (
	"bytes"
	"encoding/json"
	"errors"
)

type InitializeParams struct {
	ProtocolVersion string       `json:"protocolVersion"`
	ClientInfo      ClientInfo   `json:"clientInfo"`
	Capabilities    Capabilities `json:"capabilities"`
}

type ClientInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

type Capabilities struct {
	SessionTools            bool     `json:"sessionTools,omitempty"`
	ExperimentalAPI         bool     `json:"experimentalApi"`
	OptOutNotificationNames []string `json:"optOutNotificationMethods,omitempty"`
}

type InitializeResponse struct {
	ServerInfo      ServerInfo            `json:"serverInfo"`
	ProtocolVersion string                `json:"protocolVersion"`
	SourceID        string                `json:"sourceId"`
	Features        FeatureSet            `json:"features"`
	Navigation      *NavigationCapability `json:"navigation,omitempty"`
}

// NavigationCapability advertises the version and current ordered AppWire
// invalidation stream for the hub's navigation resources.
type NavigationCapability struct {
	Version      int    `json:"version"`
	GenerationID string `json:"generationId"`
	Sequence     uint64 `json:"sequence"`
	ReadVersions []int  `json:"readVersions,omitempty"`
}

// NavigationReadParams selects one bounded hub navigation resource. Offset
// and Limit are pointers so an explicit zero remains distinguishable from an
// omitted page parameter on the wire.
type NavigationReadParams struct {
	RepresentationVersion uint8  `json:"representationVersion"`
	Resource              string `json:"resource"`
	Section               string `json:"section,omitempty"`
	SectionID             string `json:"sectionId,omitempty"`
	// Catalog names a catalog read's catalog. On a project or project_page
	// read it narrows the read to that catalog's project, as a location's
	// catalog names it; an older hub refuses it there, and sends no location
	// catalog to take it from.
	Catalog    string              `json:"catalog,omitempty"`
	ProjectKey string              `json:"projectKey,omitempty"`
	Tier       string              `json:"tier,omitempty"`
	Ref        string              `json:"ref,omitempty"`
	Offset     *uint32             `json:"offset,omitempty"`
	Limit      *uint32             `json:"limit,omitempty"`
	Base       *NavigationReadBase `json:"base,omitempty"`
}

func (params *NavigationReadParams) UnmarshalJSON(data []byte) error {
	type wire NavigationReadParams
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	var decoded wire
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&decoded); err != nil {
		return err
	}
	if base, present := fields["base"]; present {
		var baseFields map[string]json.RawMessage
		if err := json.Unmarshal(base, &baseFields); err != nil || baseFields == nil || decoded.Base == nil {
			return errors.New("base must be an object when present")
		}
		if len(baseFields) != 3 {
			return errors.New("invalid navigation base")
		}
		for _, name := range []string{"generationId", "revision", "etag"} {
			value, ok := baseFields[name]
			if !ok || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
				return errors.New("invalid navigation base")
			}
		}
		if decoded.Base.GenerationID == "" || decoded.Base.ETag == "" || len(decoded.Base.GenerationID) > 256 || len(decoded.Base.ETag) > 1024 || decoded.Base.Revision > 9007199254740991 {
			return errors.New("invalid navigation base")
		}
	}
	if decoded.RepresentationVersion != 3 {
		return errors.New("representationVersion must be 3")
	}
	if _, present := fields["etag"]; present {
		return errors.New("etag is not a navigation field")
	}
	*params = NavigationReadParams(decoded)
	return nil
}

type NavigationReadBase struct {
	GenerationID string `json:"generationId"`
	Revision     uint64 `json:"revision"`
	ETag         string `json:"etag"`
}

// NavigationReadResponse carries one revisioned navigation resource. Data is
// raw JSON because the resource discriminator selects among several existing
// projection shapes.
type NavigationReadResponse struct {
	Status         string                   `json:"status"`
	Representation NavigationRepresentation `json:"representation,omitempty"`
	GenerationID   string                   `json:"generationId"`
	Revision       uint64                   `json:"revision"`
	ETag           string                   `json:"etag"`
	Base           *NavigationReadBase      `json:"base,omitempty"`
	Data           json.RawMessage          `json:"data,omitempty"`
}

type NavigationRepresentation string

const (
	NavigationRepresentationSnapshot NavigationRepresentation = "snapshot"
	NavigationRepresentationDelta    NavigationRepresentation = "delta"
)

// FavoriteSetParams selects the project favorite decision to persist. Kind is
// retained so the typed method preserves the explicit rejection for the
// obsolete session-favorite request shape.
type FavoriteSetParams struct {
	Kind      string `json:"kind"`
	ID        string `json:"id"`
	Favorited bool   `json:"favorited"`
	// Source names the registered source (host) that owns the project. Empty
	// and "local" both address the controller's own projects; a configured host
	// name addresses that host's project, so two hosts' projects with the same
	// ID keep separate favorites.
	Source string `json:"source,omitempty"`
}

// FavoriteSetResponse acknowledges the committed favorite decision and gives
// clients the exact navigation targets to converge before the invalidation
// notification arrives.
type FavoriteSetResponse struct {
	OK         bool               `json:"ok"`
	Navigation NavigationMutation `json:"navigation"`
}

// NavigationMutation is returned by hub-owned mutations after navigation
// state has committed, so clients can converge before the matching AppWire
// event. Targets is intentionally a plain slice so existing hubapi callers
// can assign their named NavigationArray values to it.
type NavigationMutation struct {
	GenerationID string                         `json:"generation_id"` //nolint:tagliatelle // wire field uses the established snake_case name
	Targets      []NavigationInvalidationTarget `json:"targets"`
}

// MarshalJSON keeps the navigation wire contract's arrays non-null even for a
// zero-value mutation.
func (mutation NavigationMutation) MarshalJSON() ([]byte, error) {
	targets := mutation.Targets
	if targets == nil {
		targets = []NavigationInvalidationTarget{}
	}
	return json.Marshal(struct {
		GenerationID string                         `json:"generation_id"` //nolint:tagliatelle // wire field uses the established snake_case name
		Targets      []NavigationInvalidationTarget `json:"targets"`
	}{GenerationID: mutation.GenerationID, Targets: targets})
}

// ArchiveTargetKind identifies the hub-owned object whose explicit archive
// decision is being changed.
type ArchiveTargetKind string

const (
	ArchiveTargetSession ArchiveTargetKind = "session"
	ArchiveTargetProject ArchiveTargetKind = "project"
)

// ArchiveParams sets or clears an explicit archive decision. Project targets
// must include the working directory used to resolve and verify their
// canonical project ID; session targets omit it. For a non-local Source,
// WorkingDir is optional and is only cross-checked against the identity the
// host already reported, never resolved against the controller's filesystem.
type ArchiveParams struct {
	Kind       ArchiveTargetKind `json:"kind"`
	ID         string            `json:"id"`
	WorkingDir string            `json:"workingDir,omitempty"`
	Archived   bool              `json:"archived"`
	// Source names the registered source (host) that owns the project. Empty
	// and "local" both address the controller's own projects; a configured host
	// name addresses that host's project, so two hosts' projects with the same
	// ID (or path) keep separate archive decisions.
	Source string `json:"source,omitempty"`
}

// ArchiveResponse confirms the durable decision and returns the navigation
// receipt committed by the same refresh that publishes its invalidation.
type ArchiveResponse struct {
	OK         bool               `json:"ok"`
	Navigation NavigationMutation `json:"navigation"`
}

// ProjectDeleteParams identifies the exact local project to delete. WorkingDir
// is resolved independently and must produce Key before any destructive work.
type ProjectDeleteParams struct {
	Key        string `json:"key"`
	WorkingDir string `json:"workingDir"`
	// Source names the registered source (host) that owns the project row.
	// Deletion is local-only in v1: empty and "local" both address the
	// controller's own projects, and any other source is refused before
	// anything is resolved or removed, so a remote row's delete can never be
	// answered with a controller-local removal of a project that merely shares
	// its ID or path.
	Source string `json:"source,omitempty"`
}

// ProjectDeleteSkip records one session that a project deletion could not own
// or remove without weakening its live-session and deletion-fence protections.
type ProjectDeleteSkip struct {
	ID     string `json:"id"`
	Reason string `json:"reason"`
	// Kind classifies the skip structurally: SkipKindLive, or empty for any
	// other reason, which Reason then explains.
	Kind string `json:"kind,omitempty"`
}

// ProjectDeleteResponse reports exactly which project sessions were removed or
// skipped and the navigation mutation committed for the resulting tree.
type ProjectDeleteResponse struct {
	Deleted    []string            `json:"deleted"`
	Skipped    []ProjectDeleteSkip `json:"skipped"`
	Navigation NavigationMutation  `json:"navigation"`
}

// ProjectDeleteConflictData preserves the live-session details returned when
// a whole-project delete refuses to begin.
type ProjectDeleteConflictData struct {
	ErrorData
	Live []string `json:"live"`
}

// SessionDeleteParams selects one local session by its qualified AppWire ref.
type SessionDeleteParams struct {
	Ref string `json:"ref"`
}

// DeletionSkip reports a target that could not be deleted without falsely
// presenting it as deleted. Live and concurrently reserved sessions use this
// completed response path rather than a wire error.
type DeletionSkip struct {
	ID     string `json:"id"`
	Reason string `json:"reason"`
	// Kind classifies the skip structurally: SkipKindLive, or empty for any
	// other reason, which Reason then explains.
	Kind string `json:"kind,omitempty"`
}

// SkipKindLive is the Kind of a ProjectDeleteSkip or DeletionSkip whose
// session a live daemon owns, or whose API log another process holds: the
// session was left in place and its owner keeps running.
const SkipKindLive = "live"

// SessionDeleteResponse reports the committed single-session deletion outcome
// and the navigation mutation clients must converge before clearing overlays.
type SessionDeleteResponse struct {
	Deleted    []string           `json:"deleted"`
	Skipped    []DeletionSkip     `json:"skipped"`
	Navigation NavigationMutation `json:"navigation"`
}

// PinSection is one named navigation group and its current durable membership.
type PinSection struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	MemberCount int    `json:"memberCount"`
}

type PinSectionRenameParams struct {
	SectionID string `json:"sectionId"`
	Name      string `json:"name"`
}

type PinSectionRenameResponse struct {
	OK         bool               `json:"ok"`
	Changed    bool               `json:"changed"`
	Section    PinSection         `json:"section"`
	Navigation NavigationMutation `json:"navigation"`
}

type PinSectionDeleteParams struct {
	SectionID string `json:"sectionId"`
}

type PinSectionDeleteResponse struct {
	OK          bool               `json:"ok"`
	Changed     bool               `json:"changed"`
	MemberCount int                `json:"memberCount"`
	Navigation  NavigationMutation `json:"navigation"`
}

type SessionPinAssignParams struct {
	SessionRef  string  `json:"sessionRef"`
	SectionID   *string `json:"sectionId,omitempty"`
	SectionName *string `json:"sectionName,omitempty"`
}

type SessionPinUnpinParams struct {
	SessionRef string `json:"sessionRef"`
}

type SessionPinAssignment struct {
	SessionRef string     `json:"sessionRef"`
	Section    PinSection `json:"section"`
}

type SessionPinAssignResponse struct {
	OK         bool                 `json:"ok"`
	Changed    bool                 `json:"changed"`
	Assignment SessionPinAssignment `json:"assignment"`
	Navigation NavigationMutation   `json:"navigation"`
}

type SessionPinUnpinAssignment struct {
	SessionRef string `json:"sessionRef"`
}

type SessionPinUnpinResponse struct {
	OK         bool                      `json:"ok"`
	Changed    bool                      `json:"changed"`
	Assignment SessionPinUnpinAssignment `json:"assignment"`
	Navigation NavigationMutation        `json:"navigation"`
}

// SessionSeenSetParams marks sessions seen or unread on the hub (S4), so the
// Board's Finished and Idle agree on every device.
type SessionSeenSetParams struct {
	Sessions []SessionSeenMark `json:"sessions"`
}

// SessionSeenMark is one session's mark, addressed by the ref its row carries.
// It sets exactly one of SeenThrough and Unread. SeenThrough is a hub or
// daemon timestamp the client showed, in Unix milliseconds: the row's
// turn_ended_at, or the session's lastMovedAt from an activity read taken
// while the session was on screen; never a client clock. The hub keeps the
// newest it has been sent. Unread is "Mark as
// unread", which lasts until the next SeenThrough mark.
type SessionSeenMark struct {
	Ref         string `json:"ref"`
	SeenThrough int64  `json:"seenThrough,omitempty"`
	Unread      bool   `json:"unread,omitempty"`
}

// SessionSeenSetResponse acknowledges the marks. Changed reports whether any
// mark moved a stored marker; Navigation carries the committed invalidation
// targets, empty when nothing changed.
type SessionSeenSetResponse struct {
	OK         bool               `json:"ok"`
	Changed    bool               `json:"changed"`
	Navigation NavigationMutation `json:"navigation"`
}

// ArchivedListParams names the project whose archived sessions to list: its
// key, and the catalog its row came from ("projects", "archived_projects" or
// "test_runs") as a hint. The same key can exist in more than one catalog, so
// the hub reads the hinted catalog when it holds the key; a project that moved
// between Projects and Archived projects is read from the one holding it now
// (a test-runs hint has no such fallback); with no hint, the first of
// projects, archived_projects and test_runs holding the key. Cursor is the
// previous page's NextCursor, bound to the same hint and key; Limit defaults
// to 50.
type ArchivedListParams struct {
	Catalog    string `json:"catalog,omitempty"`
	ProjectKey string `json:"projectKey"`
	Cursor     string `json:"cursor,omitempty"`
	Limit      int    `json:"limit,omitempty"`
	// Revision is the revision of the list the caller holds (a response's
	// Revision). When it is still the list's, the response says Unchanged and
	// carries no rows. An older hub ignores it and answers the page.
	Revision string `json:"revision,omitempty"`
}

// ArchivedListResponse is one page of a project's archived sessions, newest
// first. Sessions is a JSON array of hubapi.NavigationSessionSummary: appwire
// cannot import hubapi, so the rows travel as raw JSON the way a navigation
// read's data does. NextCursor is empty on the last page. Total counts every
// archived session of the project. Catalog is the catalog the hub read; it is
// absent when none of the catalogs the hint allows holds the key, and the page
// is empty.
type ArchivedListResponse struct {
	Sessions   json.RawMessage `json:"sessions"`
	NextCursor string          `json:"nextCursor,omitempty"`
	Total      int             `json:"total"`
	Catalog    string          `json:"catalog,omitempty"`
	// Revision fingerprints the whole list read, the same on every page of it;
	// it changes when any row a page could show changes. An older hub sends
	// none. It vouches for the rows a client holds only when every page it
	// holds carried this same revision; a client whose pages carried
	// different revisions holds none, and reads again.
	Revision string `json:"revision,omitempty"`
	// Unchanged is true when the request's Revision is still the list's: the
	// response then carries no rows, and Total, Catalog and Revision stand.
	Unchanged bool `json:"unchanged,omitempty"`
}

// SearchParams selects matching live and past sessions for the hub command
// palette. An empty query returns the most recent past sessions and all live
// sessions, matching the palette's initial result set.
type SearchParams struct {
	Query string `json:"query,omitempty"`
	// Scope narrows every group of the answer (S14, spec 7.4): SearchScopeAll
	// (the default when absent), SearchScopeLive or SearchScopeArchived. An
	// older hub ignores it and answers as for all; SearchResponse.Scope says
	// whether it was applied.
	Scope string `json:"scope,omitempty"`
}

// The search scopes (S14, spec 7.4).
const (
	// SearchScopeAll is every session.
	SearchScopeAll = "all"
	// SearchScopeLive is the sessions the Board's Live section holds: live and
	// not archived.
	SearchScopeLive = "live"
	// SearchScopeArchived is the sessions the rail files as archived, live or
	// ended.
	SearchScopeArchived = "archived"
)

// SearchResult is one session hit from the hub's live or past search index.
// Ref is the qualified session reference that clients use to open the hit.
type SearchResult struct {
	ID      string `json:"id"`
	Title   string `json:"title"`
	Project string `json:"project"`
	State   string `json:"state"`
	Age     string `json:"age"`
	Ref     string `json:"ref"`
	// AskPending and ApprovalPending carry the flags a navigation row does: a
	// live session is waiting on an answer to an ask_user question, or on a
	// person to allow or deny a sandbox escalation (M7). State keeps its real
	// value ("active" while an escalation blocks mid-turn), so a pending
	// approval shows only in ApprovalPending. A past (ended) result carries
	// neither. Additive: an older hub omits both, decoding as false.
	AskPending      bool `json:"askPending,omitempty"`
	ApprovalPending bool `json:"approvalPending,omitempty"`
	// Archived says the rail files the session as archived: its own archive
	// decision, its project's, or two weeks without activity (S14). Absent
	// when it is not, and from an older hub.
	Archived bool `json:"archived,omitempty"`
	// Hits are the session's newest messages that match the search, newest
	// first, and HitCount how many match in all. Only an InSessions result
	// carries them (S14).
	Hits     []SearchHit `json:"hits,omitempty"`
	HitCount int         `json:"hitCount,omitempty"`
	// TopLevel names the top-level session this hit groups under when the
	// hit is a subagent session: its first non-subagent ancestor, then
	// through fork supersessions to the row the navigation tree would show.
	// Present only on a Past result whose session is a subagent and whose
	// ancestry resolves; absent from an older hub and from every Live
	// result, and ignored by clients that do not group.
	TopLevel *SearchTopLevel `json:"topLevel,omitempty"`
}

// SearchTopLevel describes the top-level session a matching subagent groups
// under: the fields of its own search result a client needs to render the
// group's anchor row, including when the session itself has no row in the
// response. The hub owns lineage resolution; clients only group by Ref. ID,
// Ref, Title and State always carry values; Project and Age may be empty.
type SearchTopLevel struct {
	ID      string `json:"id"`
	Ref     string `json:"ref"`
	Title   string `json:"title"`
	Project string `json:"project,omitempty"`
	State   string `json:"state"`
	Age     string `json:"age,omitempty"`
	// AskPending and ApprovalPending carry the flags of the anchor's own
	// navigation row, so a live anchor renders its real marks. Absent from
	// an older hub and from an ended anchor.
	AskPending      bool `json:"askPending,omitempty"`
	ApprovalPending bool `json:"approvalPending,omitempty"`
}

// SearchHit is one message that matches a search (S14).
type SearchHit struct {
	// TranscriptKey and Position name the transcript item the message is, as
	// a thread read's items carry them, so a client opens the session at it.
	TranscriptKey string             `json:"transcriptKey"`
	Position      ThreadItemPosition `json:"position"`
	// Snippet is the message around its first match, one line, in parts: a
	// part with match set is text the search matched.
	Snippet []SearchSnippetPart `json:"snippet"`
}

// SearchSnippetPart is one run of a snippet's text.
type SearchSnippetPart struct {
	Text  string `json:"text"`
	Match bool   `json:"match,omitempty"`
}

// SearchResponse groups matching live sessions separately from persisted
// sessions so the command palette can render its two result sections.
type SearchResponse struct {
	Live []SearchResult `json:"live"`
	Past []SearchResult `json:"past"`
	// InSessions lists the sessions whose messages match, each with its hits
	// (S14), newest session first: live sessions, then ended ones. Absent when
	// none match, and from an older hub.
	InSessions []SearchResult `json:"inSessions,omitempty"`
	// Scope is the scope the answer applied. An older hub leaves it out, so a
	// client knows it offers no Archived scope and no message hits.
	Scope string `json:"scope,omitempty"`
}

// ActivityReadParams selects the sessions evener/activity/read reports. Refs
// names sessions by the refs their Live rows carry; empty reads every live
// top-level session of the hub and of its attached hosts.
type ActivityReadParams struct {
	Refs []string `json:"refs,omitempty"`
}

// ActivityReadResponse is one read of the pulse meters. A session whose daemon
// predates the meter, or whose host did not answer in time, is absent, and a
// client keeps its fallback for it until the next read.
type ActivityReadResponse struct {
	Sessions []SessionActivity `json:"sessions"`
}

// SessionActivity is one live top-level session's activity (spec 13.1, 16.4).
type SessionActivity struct {
	// Ref is the session's navigation ref, the one its Live row carries.
	Ref string `json:"ref"`
	// Minutes holds seven one-minute counts over the session's whole tree,
	// oldest first, the last ending when the hub last probed its daemon: the
	// transcript items that finished and the tool output events in each.
	Minutes []int `json:"minutes"`
	// RunningSubagents counts the session's subagents, at every depth, whose
	// run is open: SubagentTally.Running, the count its Live row shows.
	RunningSubagents int `json:"runningSubagents"`
	// QuietForMS is how long the session's whole tree has gone without
	// transcript motion, as of this read. It is present only while the session
	// is working and none of its subagents runs: an agent waiting on subagents
	// is never quiet or stuck (Jesse's ruling for S5), and a subagent inside
	// one long model call emits nothing for minutes.
	QuietForMS *int64 `json:"quietForMs,omitempty"`
	// LatestIntent is what the session's own root turn last set out to do
	// ("Reading the board's row tests."), one line of at most MaxIntentRunes:
	// the daemon's own tool-call intent, re-cut here. Absent when this turn has
	// stated none, which is most of a session's life before its first tool
	// call, and when the daemon predates the field.
	LatestIntent string `json:"latestIntent,omitempty"`
	// LastMovedAt is the Unix-millisecond time of the tree's newest transcript
	// motion (ThreadActivity.LastMovedAt). A client compares it with the
	// session's seen_through to tell whether anything moved since the person
	// last looked, mid-turn included. Absent until the tree has moved since
	// its daemon began serving it, and from an older hub or daemon.
	LastMovedAt int64 `json:"lastMovedAt,omitempty"`
}

// The kinds of hub notice (S11, spec 7.1).
const (
	// NoticeKindSignInRequired: a provider instance on this hub needs signing
	// in again before it can serve a request.
	NoticeKindSignInRequired = "signInRequired"
	// NoticeKindHostOffline: a host this hub manages is not attached, so its
	// sessions are out of reach.
	NoticeKindHostOffline = "hostOffline"
	// NoticeKindPluginBroken: an installed plugin fails validation.
	NoticeKindPluginBroken = "pluginBroken"
)

// HubNotice is one hub-level problem that blocks sessions (S11, spec 7.1).
type HubNotice struct {
	// ID is the notice's identity, stable while the problem lasts:
	// "<kind>:<subject>", and "<kind>:<plugin>@<marketplace>" for a plugin.
	ID   string `json:"id"`
	Kind string `json:"kind"`
	// Subject is what the notice names and what its action routes by: the
	// provider instance (an evener/auth/list row's provider), the host's source
	// ID (a manifest source's id), or the plugin's name.
	Subject string `json:"subject"`
	// Marketplace is a plugin notice's marketplace: two marketplaces can each
	// ship a plugin of the same name.
	Marketplace string `json:"marketplace,omitempty"`
	// AffectedSessions counts the live top-level sessions the problem blocks:
	// for a sign-in, this hub's live sessions whose current model runs on the
	// provider instance; for a host, the host's sessions that were live when it
	// was last reached. Absent when there are none, and on a plugin notice,
	// since no row says which plugins a session runs.
	AffectedSessions int `json:"affectedSessions,omitempty"`
}

// NoticesListResponse is evener/notices/list's answer and
// evener/notices/changed's payload: every notice the hub derives now, sign-ins
// first, then hosts, then plugins.
type NoticesListResponse struct {
	Notices []HubNotice `json:"notices"`
}

// ServerInfo names the serving hub and its machine: Name and Version identify
// the build, while OS, Arch and Roots describe the hub's own machine — the same
// system and project-root facts a HostRow carries for a host the hub reaches
// over SSH. A client that already shows the hub's own machine (the phone's
// Hosts) renders those fields instead of leaving the machine's system and roots
// unknown. OS and Arch name the machine's system and are absent when a server
// does not report them; Roots is the machine's project roots, absent when it
// has none.
type ServerInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	// BuildVersion identifies the running executable, independently of the
	// server package version. Absence means the build is unknown.
	BuildVersion string   `json:"buildVersion,omitempty"`
	OS           string   `json:"os,omitempty"`
	Arch         string   `json:"arch,omitempty"`
	Roots        []string `json:"roots,omitempty"`
}

type FeatureSet struct {
	SessionTools              bool `json:"sessionTools,omitempty"`
	ThreadList                bool `json:"threadList"`
	ThreadTurnsList           bool `json:"threadTurnsList"`
	TurnStart                 bool `json:"turnStart"`
	TurnSteer                 bool `json:"turnSteer"`
	ThreadClear               bool `json:"threadClear"`
	ThreadShutdown            bool `json:"threadShutdown"`
	ForkFromTurn              bool `json:"forkFromTurn"`
	Tasks                     bool `json:"tasks"`
	TranscriptList            bool `json:"transcriptList"`
	ModelList                 bool `json:"modelList"`
	DirectoryComplete         bool `json:"directoryComplete"`
	Auth                      bool `json:"auth"`
	TranscriptDisplaySettings bool `json:"transcriptDisplaySettings,omitempty"`
	KeybindingsSettings       bool `json:"keybindingsSettings,omitempty"`
}
