package agenttest

import (
	"context"
	"fmt"
	"hash/fnv"
	"io"
	"strconv"

	"primeradiant.com/serf/agent/execenv"
)

// DenyEnv is an execenv.ExecutionEnvironment for fuzzing tool-handler execution
// entirely offline. It NEVER touches the filesystem, forks a process, or opens a
// network connection. Every method returns a deterministic, bounded result
// derived purely from Seed plus the call's arguments — so outputs vary enough to
// drive more handler branches (exit codes, file-exists, content length) while a
// replay with the same Seed is byte-identical, regardless of the order in which
// concurrent job goroutines call it (the outputs depend on no shared counter or
// host state).
//
// Seed is the fuzz draw; the harness records it in the artifact so a replay
// reconstructs the exact environment. It implements execenv.StreamingExecutor so
// the streaming shell-job path is in scope (bounded canned stream — a fidelity
// gap vs real subprocess streaming, flagged in the design's honesty section).
type DenyEnv struct {
	WorkDir string
	Seed    uint64
}

const denyMaxBytes = 512

var _ execenv.ExecutionEnvironment = (*DenyEnv)(nil)
var _ execenv.StreamingExecutor = (*DenyEnv)(nil)

func (d *DenyEnv) Initialize() error { return nil }
func (d *DenyEnv) Cleanup()          {}

func (d *DenyEnv) WorkingDirectory() string { return d.WorkDir }
func (d *DenyEnv) Platform() string         { return "linux" }
func (d *DenyEnv) OSVersion() string        { return "deny-env" }

// draw derives a stable 64-bit value from the seed and the call's discriminators.
func (d *DenyEnv) draw(parts ...string) uint64 {
	h := fnv.New64a()
	var seed [8]byte
	for i := 0; i < 8; i++ {
		seed[i] = byte(d.Seed >> (8 * i))
	}
	_, _ = h.Write(seed[:])
	for _, p := range parts {
		_, _ = h.Write([]byte{0})
		_, _ = io.WriteString(h, p)
	}
	return h.Sum64()
}

// boundedText produces deterministic, capped ASCII content from a draw.
func (d *DenyEnv) boundedText(h uint64) string {
	n := int(h % (denyMaxBytes + 1))
	if n == 0 {
		return ""
	}
	b := make([]byte, n)
	for i := range b {
		b[i] = byte('a' + (uint64(i)+h)%26)
	}
	return string(b)
}

func (d *DenyEnv) ReadFile(path string, _ *int, _ *int) (string, error) {
	h := d.draw("read", path)
	if h%5 == 0 {
		return "", fmt.Errorf("deny-env: no such file: %s", path)
	}
	return d.boundedText(h), nil
}

func (d *DenyEnv) WriteFile(path string, content string) (string, error) {
	return fmt.Sprintf("wrote %d bytes to %s", len(content), path), nil
}

func (d *DenyEnv) EditFile(path string, oldString string, _ string, _ bool) (string, error) {
	h := d.draw("edit", path, oldString)
	if h%3 == 0 {
		return "", fmt.Errorf("deny-env: no match for edit in %s", path)
	}
	return "edited " + path, nil
}

func (d *DenyEnv) FileExists(path string) bool {
	return d.draw("exists", path)%2 == 0
}

func (d *DenyEnv) Glob(pattern string, basePath string) ([]string, error) {
	h := d.draw("glob", pattern, basePath)
	n := int(h % 4)
	out := make([]string, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, fmt.Sprintf("%s/match-%d.txt", basePath, i))
	}
	return out, nil
}

func (d *DenyEnv) Grep(pattern string, path string, _ string, _ bool, _ int, outputMode string) (string, error) {
	h := d.draw("grep", pattern, path, outputMode)
	switch outputMode {
	case "count":
		return strconv.Itoa(int(h % 10)), nil
	case "files_with_matches":
		if h%2 == 0 {
			return path, nil
		}
		return "", nil
	default:
		return d.boundedText(h), nil
	}
}

func (d *DenyEnv) ListDirectory(path string, _ int) ([]execenv.DirEntry, error) {
	h := d.draw("ls", path)
	n := int(h % 4)
	out := make([]execenv.DirEntry, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, execenv.DirEntry{
			Name:  fmt.Sprintf("entry-%d", i),
			IsDir: (h>>uint(i))&1 == 0,
			Size:  int64(h % 1024),
		})
	}
	return out, nil
}

func (d *DenyEnv) ExecCommand(_ context.Context, command string, _ int, workingDir string, _ map[string]string) (execenv.ExecResult, error) {
	h := d.draw("exec", command, workingDir)
	if h%7 == 0 {
		return execenv.ExecResult{}, fmt.Errorf("deny-env: command failed to start: %s", command)
	}
	return execenv.ExecResult{
		Stdout:   d.boundedText(h),
		Stderr:   "",
		ExitCode: int(h % 3),
	}, nil
}

func (d *DenyEnv) StreamCommand(_ context.Context, command, workingDir string, _ map[string]string, out io.Writer) (*execenv.StreamHandle, error) {
	h := d.draw("stream", command, workingDir)
	if out != nil {
		_, _ = io.WriteString(out, d.boundedText(h))
	}
	exit := int(h % 3)
	return &execenv.StreamHandle{
		Pid:    1000 + int(h%1000),
		Wait:   func() (int, error) { return exit, nil },
		Signal: func() {},
	}, nil
}
