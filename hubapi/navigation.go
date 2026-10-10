package hubapi

import (
	"encoding/json"
	"time"

	"primeradiant.com/evener/appwire"
)

// NavigationArray preserves the navigation wire rule that every array is
// present and non-null, including a zero-value response struct.
type NavigationArray[T any] []T

// MarshalJSON encodes a nil navigation array as an explicit empty JSON array.
func (array NavigationArray[T]) MarshalJSON() ([]byte, error) {
	if array == nil {
		return []byte("[]"), nil
	}
	return json.Marshal([]T(array))
}

// NavigationManifest is the hub's top-level navigation resource.
type NavigationManifest struct {
	GenerationID     string                  `json:"generation_id"`
	Revision         uint64                  `json:"revision"`
	Sources          NavigationArray[Source] `json:"sources"`
	AttentionSummary AttentionSummary        `json:"attentionSummary"` //nolint:tagliatelle // shares the attention notification shape
	Sections         NavigationSections      `json:"sections"`
	Catalogs         NavigationCatalogs      `json:"catalogs"`
}

// NavigationResourceDescriptor describes the number of rows available from a
// bounded navigation resource.
type NavigationResourceDescriptor struct {
	Count int `json:"count"`
}

// NavigationSections describes the manifest's global section resources.
type NavigationSections struct {
	Live        NavigationResourceDescriptor `json:"live"`
	NeedsYou    NavigationResourceDescriptor `json:"needs_you"`
	PinSections NavigationResourceDescriptor `json:"pin_sections"`
}

// NavigationCatalogs describes the manifest's project catalog resources.
type NavigationCatalogs struct {
	Projects         NavigationResourceDescriptor `json:"projects"`
	ArchivedProjects NavigationResourceDescriptor `json:"archived_projects"`
	TestRuns         NavigationResourceDescriptor `json:"test_runs"`
}

// NavigationSectionResource is one bounded global or pin-section session page.
type NavigationSectionResource struct {
	GenerationID string                                    `json:"generation_id"`
	Revision     uint64                                    `json:"revision"`
	Sessions     NavigationArray[NavigationSessionSummary] `json:"sessions"`
	Remaining    int                                       `json:"remaining"`
	Truncated    bool                                      `json:"truncated"`
}

// NavigationPinSectionCatalog is one bounded page of pin-section descriptors.
type NavigationPinSectionCatalog struct {
	GenerationID string                                          `json:"generation_id"`
	Revision     uint64                                          `json:"revision"`
	PinSections  NavigationArray[NavigationPinSectionDescriptor] `json:"pin_sections"`
	Remaining    int                                             `json:"remaining"`
}

// NavigationPinSectionDescriptor describes one named section in the
// pin-section catalog.
type NavigationPinSectionDescriptor struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Count int    `json:"count"`
}

// NavigationProjectCatalog is one bounded page of project summaries.
type NavigationProjectCatalog struct {
	GenerationID string                                    `json:"generation_id"`
	Revision     uint64                                    `json:"revision"`
	Projects     NavigationArray[NavigationProjectSummary] `json:"projects"`
	Remaining    int                                       `json:"remaining"`
}

// NavigationProjectSummary is the bounded project header exposed by a catalog.
type NavigationProjectSummary struct {
	Key             string `json:"key"`
	Name            string `json:"name"`
	WorkingDir      string `json:"working_dir,omitempty"`
	RollupState     string `json:"rollup_state,omitempty"`
	RollupLive      int    `json:"rollup_live,omitempty"`
	RollupAttn      int    `json:"rollup_attn,omitempty"`
	DefaultExpanded bool   `json:"default_expanded,omitempty"`
	MoreCurrent     int    `json:"more_current,omitempty"`
	MoreRecent      int    `json:"more_recent,omitempty"`
	MoreArchived    int    `json:"more_archived,omitempty"`
	Worktrees       int    `json:"worktrees,omitempty"`
	IsArchived      bool   `json:"is_archived,omitempty"`
	Favorite        bool   `json:"favorite,omitempty"`
	// Sources names every source that owns sessions in this project: the
	// literal "local" for the controller's own sessions and a configured host
	// name for a remote host's. The field is omitted when every session belongs
	// to the controller, which is the same default the decision readers apply
	// to an empty list. A project merged across hosts (the same canonical ID
	// and path, e.g. a local checkout and a remote host's clone) carries
	// "local" alongside every host name, so a caller that must name exactly one
	// owner — a delete, or a per-source favorite/archive decision — can refuse
	// or address each owner instead of guessing which host a mutation means.
	Sources      NavigationArray[string] `json:"sources,omitempty"`
	SessionCount int                     `json:"session_count"`
}

// NavigationProjectResource is the first bounded page for each project tier.
type NavigationProjectResource struct {
	GenerationID string         `json:"generation_id"`
	Revision     uint64         `json:"revision"`
	Key          string         `json:"key"`
	Current      NavigationTier `json:"current"`
	Recent       NavigationTier `json:"recent"`
	Archived     NavigationTier `json:"archived"`
	Truncated    bool           `json:"truncated"`
}

// NavigationTier is one bounded page of a project tier.
type NavigationTier struct {
	Sessions  NavigationArray[NavigationSessionSummary] `json:"sessions"`
	Remaining int                                       `json:"remaining"`
}

// NavigationProjectPage is a bounded page beyond a project's initial tier.
type NavigationProjectPage struct {
	GenerationID string                                    `json:"generation_id"`
	Revision     uint64                                    `json:"revision"`
	Key          string                                    `json:"key"`
	Tier         string                                    `json:"tier"`
	Offset       uint32                                    `json:"offset"`
	Sessions     NavigationArray[NavigationSessionSummary] `json:"sessions"`
	Remaining    int                                       `json:"remaining"`
	Truncated    bool                                      `json:"truncated"`
}

// NavigationSessionLocation is the top-level owner and current summary for a
// session referenced by a deep link.
type NavigationSessionLocation struct {
	GenerationID string `json:"generation_id"`
	Revision     uint64 `json:"revision"`
	Ref          string `json:"ref"`
	TopLevelRef  string `json:"top_level_ref"`
	// Catalog is the catalog ("projects", "archived_projects" or "test_runs")
	// whose project ProjectKey names: a key can be in several, and a project
	// or project_page read naming this catalog returns the project holding
	// the session. Absent outside a project, and from an older hub, which
	// also refuses a catalog on those reads.
	Catalog      string                    `json:"catalog,omitempty"`
	ProjectKey   string                    `json:"project_key,omitempty"`
	TopLevel     bool                      `json:"top_level"`
	Tier         string                    `json:"tier,omitempty"`
	PinSectionID string                    `json:"pin_section_id,omitempty"`
	Session      *NavigationSessionSummary `json:"session,omitempty"`
}

// NavigationTaskProgress is a live session's task-list progress: how many of
// its tasks exist, are done and were cancelled, and the first task in
// progress. Carried only when the list is non-empty.
type NavigationTaskProgress struct {
	Total     int `json:"total"`
	Done      int `json:"done"`
	Cancelled int `json:"cancelled,omitempty"`
	// CurrentID and Current name the first task in progress: its ID in the
	// session's task list and its description, cut to the label bound. Both
	// are absent while no task is in progress.
	CurrentID int    `json:"current_id,omitempty"`
	Current   string `json:"current,omitempty"`
}

// NavigationSubagentTally is a live root's whole-tree subagent tally (S3):
// every subagent at every depth, running, failed (its latest run ended failed
// or exhausted) or done.
type NavigationSubagentTally struct {
	Running int `json:"running"`
	Failed  int `json:"failed"`
	Done    int `json:"done"`
}

// NavigationFailureCrashed is the cause_kind of a row whose daemon's process
// exited while the hub still lists it. Nothing is left to report why, so the
// hub says so itself (S1c).
const NavigationFailureCrashed = "crashed"

// NavigationFailure says why a Failed row failed (S1c): the failure's headline
// ("Provider error", "Usage limit reached", "Sign-in required") and its cause's
// kind ("provider", "signInRequired", "transcript_failed_closed", or the hub's
// own NavigationFailureCrashed), the provider it came from and its HTTP status,
// from which a client composes the row's why line ("codex-jesse-fsck.com
// sign-in expired (401)"). It never carries the failure's message, which can
// quote a provider's error body.
type NavigationFailure struct {
	Title     string `json:"title,omitempty"`
	CauseKind string `json:"cause_kind,omitempty"`
	Provider  string `json:"provider,omitempty"`
	Status    int    `json:"status,omitempty"`
}

// NavigationQuestion is the first question of a live session's pending ask
// (S1b): a Needs you row's why line ("Question · keep or drop the implied
// options?"), the option labels a long-press preview lists, and how many
// questions the ask holds. Text and each label are one line, cut to the
// wire's bounds (appwire.BoundedPendingQuestion).
type NavigationQuestion struct {
	Text    string   `json:"text"`
	Options []string `json:"options,omitempty"`
	Count   int      `json:"count"`
}

// NavigationSessionSummary is a bounded session row with compact own-session activity facts.
type NavigationSessionSummary struct {
	Ref        string `json:"ref"`
	HostID     string `json:"host_id"`
	SessionID  string `json:"session_id"`
	Title      string `json:"title"`
	Project    string `json:"project"`
	State      string `json:"state"`
	Kind       string `json:"kind"`
	Branch     string `json:"branch,omitempty"`
	Favorite   bool   `json:"favorite,omitempty"`
	Rename     bool   `json:"rename,omitempty"`
	Live       bool   `json:"live"`
	AskPending bool   `json:"ask_pending,omitempty"`
	// ApprovalPending is true while the session is blocked on a sandbox
	// escalation a human must allow or deny (M7). The row keeps its real State
	// ("active": the escalation blocks mid-turn); the flag says why the session
	// is in NeedsYou, beside AskPending for a question.
	ApprovalPending bool `json:"approval_pending,omitempty"`
	// ApprovalTool and ApprovalTarget say what the oldest pending escalation
	// asks for, so the row can say why it waits ("wants to write outside the
	// workspace: ~/sites/docs"): the tool that was denied, which a client maps
	// to a verb, and the escalation's full literal denied path. The path is
	// shown for informed consent (appwire.SandboxEscalationRequested) and
	// reaches human clients only, as thread/read's cards already do. Both are
	// absent unless ApprovalPending is set; the tool is cut to the identity
	// bound and the target to the label bound.
	ApprovalTool   string `json:"approval_tool,omitempty"`
	ApprovalTarget string `json:"approval_target,omitempty"`
	// Question is the first question of the session's pending ask (S1b). It
	// is present only on a row that carries AskPending and whose daemon named
	// the question.
	Question *NavigationQuestion `json:"question,omitempty"`
	// Failure says why the session failed (S1c). It is present only on a
	// Failed row whose daemon summarized the failure, or whose daemon
	// crashed.
	Failure *NavigationFailure `json:"failure,omitempty"`
	// LastMessage is the opening of the session's last agent message (S1d):
	// a Finished row's why line and the long-press preview's excerpt, one
	// line of at most appwire.MaxMessageExcerptRunes. It is the agent's own
	// words, never its reasoning or a tool's output. A live session's comes
	// from its daemon and an ended one's from its meta; subagent rows carry
	// none.
	LastMessage string `json:"last_message,omitempty"`
	// ModelName is the display name of the model the session runs (S17), for
	// the row's last line when a client shows models on rows: the name the
	// hub's model/list gives the same model, so a row and the model picker
	// agree. A live session's is its current model, which follows a switch;
	// an ended one's is its meta's; subagent rows carry none.
	ModelName string `json:"model_name,omitempty"`
	Dormant   bool   `json:"dormant,omitempty"`
	// Offline marks a row folded into the merged list from a source that is
	// currently unreachable: its last-known rows stay visible, but they are not
	// live and cannot serve host-targeted actions until the source reattaches.
	// It is keyed off the row's source identity (HostID), never the row's own
	// state, and is never set for the controller's own local rows.
	//
	// It sits BESIDE Dormant rather than reusing it: Dormant means the session
	// has never run, and an offline row that ran must not read as "Not started".
	Offline       bool       `json:"offline,omitempty"`
	UpdatedAt     *time.Time `json:"updated_at,omitempty"`
	MoreSubagents int        `json:"more_subagents,omitempty"`
	// Subagents is a live root's whole-tree subagent tally, counted by its
	// daemon (S3). Present only on a live root row whose tree has a subagent;
	// it counts descendants without embedding their detail rows.
	Subagents          *NavigationSubagentTally `json:"subagents,omitempty"`
	OmittedDescendants int                      `json:"omitted_descendants,omitempty"`
	// TurnEndedAt is when a live session's last turn ended, stamped by its
	// daemon (S4). It is present only on a live row whose daemon reported one.
	// A client that marks the row seen echoes it back as seenThrough.
	TurnEndedAt *time.Time `json:"turn_ended_at,omitempty"`
	// Unseen marks a live row whose last turn ended after the hub's
	// seen-through marker for it, or that was marked unread (S4): Finished on
	// the Board, and Idle when absent. It is only ever set on a row that
	// carries TurnEndedAt.
	Unseen bool `json:"unseen,omitempty"`
	// SeenThrough is the hub's seen-through mark for a live row, floored at
	// the seen store's start, so a client can tell output that moved after it
	// (an activity time) from output already seen, even mid-turn. Absent on a
	// row that isn't live, and from a hub with no seen store or an older hub.
	SeenThrough *time.Time `json:"seen_through,omitempty"`
	// Own-session activity counts are captured before navigation fitting.
	// RunningJobCommand is the first available command, bounded for display.
	RunningJobCount   int    `json:"running_job_count,omitempty"`
	RunningJobCommand string `json:"running_job_command,omitempty"`
	WatchCount        int    `json:"watch_count,omitempty"`
	ArmedWatchCount   int    `json:"armed_watch_count,omitempty"`
	// Tasks is the task line's facts ("Task 4 of 7 · Fix the settle/drain
	// race"). Absent for a session with no task list or an empty one, and for
	// every session with no live daemon entry: ended sessions and in-process
	// children. A live session on another host carries its host's (S13b).
	Tasks    *NavigationTaskProgress                   `json:"tasks,omitempty"`
	Children NavigationArray[NavigationSessionSummary] `json:"children"`
}

// NavigationMutation remains available to hubapi callers while the shared wire
// shape is owned by appwire.
type NavigationMutation = appwire.NavigationMutation
