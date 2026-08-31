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
	SessionCount    int    `json:"session_count"`
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
	GenerationID string                    `json:"generation_id"`
	Revision     uint64                    `json:"revision"`
	Ref          string                    `json:"ref"`
	TopLevelRef  string                    `json:"top_level_ref"`
	ProjectKey   string                    `json:"project_key,omitempty"`
	TopLevel     bool                      `json:"top_level"`
	Tier         string                    `json:"tier,omitempty"`
	PinSectionID string                    `json:"pin_section_id,omitempty"`
	Session      *NavigationSessionSummary `json:"session,omitempty"`
}

// NavigationJobSummary is the compact non-delegate job row shown beneath its
// owning session. Delegate jobs remain represented as session children.
type NavigationJobSummary struct {
	JobID   string `json:"job_id"`
	JobType string `json:"job_type"`
	Status  string `json:"status"`
	Command string `json:"command,omitempty"`
	Task    string `json:"task,omitempty"`
	Reason  string `json:"reason,omitempty"`
	// Intent is the tool call's `intent` argument: why the command is being
	// run, in the model's own words. Surfaces in the rail row's tooltip.
	Intent string `json:"intent,omitempty"`
	// FullCommand carries the command when it exceeds the label bound
	// (maxNavigationLabelRunes), so a tooltip can show more of what was
	// actually executed. Still bounded by maxNavigationFullCommandRunes:
	// a pathological command cannot dominate the response's byte budget.
	// Absent when the command fits the label bound (no truncation).
	FullCommand string `json:"full_command,omitempty"`
}

// NavigationSessionSummary is the bounded recursive navigation row shape.
type NavigationSessionSummary struct {
	Ref                string                                    `json:"ref"`
	HostID             string                                    `json:"host_id"`
	SessionID          string                                    `json:"session_id"`
	Title              string                                    `json:"title"`
	Project            string                                    `json:"project"`
	State              string                                    `json:"state"`
	Kind               string                                    `json:"kind"`
	Branch             string                                    `json:"branch,omitempty"`
	ClusterCount       int                                       `json:"cluster_count,omitempty"`
	Favorite           bool                                      `json:"favorite,omitempty"`
	Rename             bool                                      `json:"rename,omitempty"`
	Live               bool                                      `json:"live"`
	AskPending         bool                                      `json:"ask_pending,omitempty"`
	Dormant            bool                                      `json:"dormant,omitempty"`
	UpdatedAt          *time.Time                                `json:"updated_at,omitempty"`
	MoreSubagents      int                                       `json:"more_subagents,omitempty"`
	OmittedDescendants int                                       `json:"omitted_descendants,omitempty"`
	RunningJobs        NavigationArray[NavigationJobSummary]     `json:"running_jobs,omitempty"`
	CompletedJobs      NavigationArray[NavigationJobSummary]     `json:"completed_jobs,omitempty"`
	Children           NavigationArray[NavigationSessionSummary] `json:"children"`
}

// NavigationMutation remains available to hubapi callers while the shared wire
// shape is owned by appwire.
type NavigationMutation = appwire.NavigationMutation
