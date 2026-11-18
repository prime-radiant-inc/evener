package schema

// ResourceCaps records the effective CPU and memory available to the process.
// Zero values mean that the corresponding value could not be established
// without treating an untrusted host observation as a container fact.
type ResourceCaps struct {
	CPUs     float64 `json:"cpus,omitempty"`
	MemoryMB int64   `json:"memory_mb,omitempty"` // whole MiB, rounded down
}

// EnvironmentInfo holds the working directory, platform, version, date, and
// git/workspace details describing the environment an agent runs in.
type EnvironmentInfo struct {
	WorkingDir string `json:"working_dir"` // the agent's working directory
	// LaunchDir is the absolute spelling the session was launched with, recorded
	// only when it differs from WorkingDir — a launch through a symlink, or a path
	// with ".." behind one. WorkingDir stays canonical because project identity is
	// a hash of it and every filesystem operation is rooted at it; LaunchDir
	// exists because the transcript client binds an absolute file reference to the
	// session's CWD by lexical prefix, so a document written with the launch alias
	// stops opening if the alias is erased. Anything the user sees or the client
	// binds reads BindingDir, which prefers this; anything that decides identity or
	// touches the filesystem reads WorkingDir.
	LaunchDir             string        `json:"launch_dir,omitempty"`
	Platform              string        `json:"platform"`                           // OS platform (e.g. "darwin")
	OSVersion             string        `json:"os_version"`                         // human-readable OS version
	Today                 string        `json:"today"`                              // YYYY-MM-DD
	KnowledgeCutoff       string        `json:"knowledge_cutoff"`                   // YYYY-MM-DD
	IsGitRepo             bool          `json:"is_git_repo"`                        // whether WorkingDir is inside a git repo
	GitBranch             string        `json:"git_branch,omitempty"`               // current branch name
	GitOriginURL          string        `json:"git_origin_url,omitempty"`           // "origin" remote URL
	GitModifiedFiles      int           `json:"git_modified_files"`                 // count of tracked files with changes
	GitUntrackedFiles     int           `json:"git_untracked_files"`                // count of untracked files
	GitStatusUnknown      bool          `json:"git_status_unknown,omitempty"`       // true when the status read was truncated or failed, so the counts above are not a claim
	GitSnapshotFailed     bool          `json:"git_snapshot_failed,omitempty"`      // true when a git probe could not run or was cut short, so absent values below are not a claim about the repository
	GitRecentCommitTitles []string      `json:"git_recent_commit_titles,omitempty"` // recent commit subject lines
	Workspace             WorkspaceInfo `json:"workspace"`                          // detected build/workspace layout
	Resources             *ResourceCaps `json:"resources,omitempty"`                // effective process resource caps, when observable
}

// BindingDir is the working directory as the CLIENT should bind it: the launch
// spelling when the session recorded one, and the canonical path otherwise.
//
// The two are deliberately different fields, because one question was being asked
// of one field. WorkingDir is canonical: project identity is a hash of it and
// every filesystem operation is rooted at it. An absolute file reference in a
// transcript is bound to the session's CWD by LEXICAL prefix, though, so a
// document written with the launch alias — a symlinked path, or one carrying ".."
// behind a symlink — stops opening once the alias is erased, even though the
// symlink and the file both still exist. The launch path is an identity for the
// transcript, the resolved path is where bytes go.
func (e EnvironmentInfo) BindingDir() string {
	if e.LaunchDir != "" {
		return e.LaunchDir
	}
	return e.WorkingDir
}
