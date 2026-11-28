package skill

const (
	// GardeningMemorySkillName is the bundled skill whose instructions correct
	// memory pages with the memory save tools.
	GardeningMemorySkillName = "gardening-memory"
	// RunningJobsSkillName is the bundled guide for delegates, background shell
	// jobs, watches, and job inspection.
	RunningJobsSkillName = "running-jobs"
	// UsingWorktreesSkillName is the bundled guide for manage_worktree, local
	// merges, and post-merge worktree cleanup.
	UsingWorktreesSkillName = "using-worktrees"
	// ReadingTranscriptsSkillName is the bundled guide for read_transcript and
	// find_session_transcripts.
	ReadingTranscriptsSkillName = "reading-transcripts"
)

// SessionAvailability is the session capability a skill advertisement needs
// beyond the source's own controls. The owning Session computes it for a live
// catalog; a cold hub catalog reads the same answer from persisted session
// metadata.
type SessionAvailability struct {
	// MemorySaveAvailable reports that the session serves memory_write,
	// memory_edit and memory_delete.
	MemorySaveAvailable bool
	// RunningJobsAvailable reports that the session serves delegate, job_watch, or job_list.
	RunningJobsAvailable bool
	// WorktreeAvailable reports that the session serves the full manage_worktree tool.
	WorktreeAvailable bool
	// TranscriptsAvailable reports that the session serves read_transcript or find_session_transcripts.
	TranscriptsAvailable bool
}

// SkillAvailable reports whether a session with this availability may advertise
// and load name. Capability-gated bundled skills require their matching tools;
// every other skill is available as its source controls say.
func (a SessionAvailability) SkillAvailable(name string) bool {
	switch name {
	case GardeningMemorySkillName:
		return a.MemorySaveAvailable
	case RunningJobsSkillName:
		return a.RunningJobsAvailable
	case UsingWorktreesSkillName:
		return a.WorktreeAvailable
	case ReadingTranscriptsSkillName:
		return a.TranscriptsAvailable
	default:
		return true
	}
}

// IsAutoDisclosedGuideSkill reports whether d is one of the bundled guide
// skills omitted from the Turn-1 <skill-catalog> and disclosed on first use.
// Non-bundled overrides (project, user, extra dirs) return false so they stay advertised.
func IsAutoDisclosedGuideSkill(d Descriptor) bool {
	if !d.Bundled {
		return false
	}
	switch d.CatalogName {
	case RunningJobsSkillName, UsingWorktreesSkillName, ReadingTranscriptsSkillName:
		return true
	default:
		return false
	}
}

// UserEntriesForSession is UserEntries narrowed by a session's availability,
// preserving canonical order.
func (c Catalog) UserEntriesForSession(availability SessionAvailability) []Descriptor {
	entries := c.UserEntries()
	result := make([]Descriptor, 0, len(entries))
	for _, entry := range entries {
		if availability.SkillAvailable(entry.CatalogName) {
			result = append(result, entry)
		}
	}
	return result
}
