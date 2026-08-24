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
	WorkingDir            string        `json:"working_dir"`                        // the agent's working directory
	Platform              string        `json:"platform"`                           // OS platform (e.g. "darwin")
	OSVersion             string        `json:"os_version"`                         // human-readable OS version
	Today                 string        `json:"today"`                              // YYYY-MM-DD
	KnowledgeCutoff       string        `json:"knowledge_cutoff"`                   // YYYY-MM-DD
	CPUs                  float64       `json:"cpus,omitempty"`                     // effective CPU cap, when measured
	MemoryMB              int64         `json:"memory_mb,omitempty"`                // effective memory cap in MiB, when measured
	IsGitRepo             bool          `json:"is_git_repo"`                        // whether WorkingDir is inside a git repo
	GitBranch             string        `json:"git_branch,omitempty"`               // current branch name
	GitOriginURL          string        `json:"git_origin_url,omitempty"`           // "origin" remote URL
	GitModifiedFiles      int           `json:"git_modified_files"`                 // count of tracked files with changes
	GitUntrackedFiles     int           `json:"git_untracked_files"`                // count of untracked files
	GitRecentCommitTitles []string      `json:"git_recent_commit_titles,omitempty"` // recent commit subject lines
	Workspace             WorkspaceInfo `json:"workspace"`                          // detected build/workspace layout
	Resources             *ResourceCaps `json:"resources,omitempty"`                // effective process resource caps, when observable
}
