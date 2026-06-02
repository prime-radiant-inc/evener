package agent

import (
	"time"

	"primeradiant.com/serf/agent/execenv"
)

// EnvironmentInfo holds the working directory, platform, version, date, and
// git/workspace details describing the environment an agent runs in.
type EnvironmentInfo struct {
	WorkingDir            string        `json:"working_dir"`                        // the agent's working directory
	Platform              string        `json:"platform"`                           // OS platform (e.g. "darwin")
	OSVersion             string        `json:"os_version"`                         // human-readable OS version
	Today                 string        `json:"today"`                              // YYYY-MM-DD
	KnowledgeCutoff       string        `json:"knowledge_cutoff"`                   // YYYY-MM-DD
	IsGitRepo             bool          `json:"is_git_repo"`                        // whether WorkingDir is inside a git repo
	GitBranch             string        `json:"git_branch,omitempty"`               // current branch name
	GitOriginURL          string        `json:"git_origin_url,omitempty"`           // "origin" remote URL
	GitModifiedFiles      int           `json:"git_modified_files"`                 // count of tracked files with changes
	GitUntrackedFiles     int           `json:"git_untracked_files"`                // count of untracked files
	GitRecentCommitTitles []string      `json:"git_recent_commit_titles,omitempty"` // recent commit subject lines
	Workspace             WorkspaceInfo `json:"workspace,omitempty"`                // detected build/workspace layout
}

// envInfoFromEnv builds an EnvironmentInfo from the execution environment,
// stamping today's date and the detected workspace layout.
func envInfoFromEnv(env execenv.ExecutionEnvironment) EnvironmentInfo {
	wd := ""
	plat := ""
	osv := ""
	if env != nil {
		wd = env.WorkingDirectory()
		plat = env.Platform()
		osv = env.OSVersion()
	}
	return EnvironmentInfo{
		WorkingDir: wd,
		Platform:   plat,
		OSVersion:  osv,
		Today:      time.Now().UTC().Format("2006-01-02"),
		Workspace:  ScanWorkspace(wd),
	}
}
