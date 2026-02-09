package agent

import "context"

type ExecResult struct {
	Stdout     string `json:"stdout"`
	Stderr     string `json:"stderr"`
	ExitCode   int    `json:"exit_code"`
	TimedOut   bool   `json:"timed_out"`
	DurationMS int64  `json:"duration_ms"`
}

type DirEntry struct {
	Name  string `json:"name"`
	IsDir bool   `json:"is_dir"`
	Size  int64  `json:"size,omitempty"`
}

// ExecutionEnvironment abstracts the filesystem and command runner used by tools.
type ExecutionEnvironment interface {
	WorkingDirectory() string
	Platform() string
	OSVersion() string

	ReadFile(path string, offsetLine *int, limitLines *int) (string, error)
	WriteFile(path string, content string) (string, error)
	EditFile(path string, oldString string, newString string, replaceAll bool) (string, error)
	FileExists(path string) bool

	Glob(pattern string, basePath string) ([]string, error)
	Grep(pattern string, path string, globFilter string, caseInsensitive bool, maxResults int) (string, error)
	ListDirectory(path string, depth int) ([]DirEntry, error)

	ExecCommand(ctx context.Context, command string, timeoutMS int, workingDir string, envVars map[string]string) (ExecResult, error)
}
