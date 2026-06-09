package execenv

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/bmatcuk/doublestar/v4"
)

// EnvVarPolicy controls which environment variables are inherited by child processes.
type EnvVarPolicy int

const (
	// EnvPolicyDefault inherits all non-sensitive environment variables (the current default behavior).
	EnvPolicyDefault EnvVarPolicy = iota // Inherit all non-sensitive (current behavior)
	// EnvPolicyAll inherits every environment variable, including sensitive ones.
	EnvPolicyAll // Inherit everything including sensitive
	// EnvPolicyNone starts with a clean environment, passing only explicitly provided vars.
	EnvPolicyNone // Start clean, only explicitly passed vars
	// EnvPolicyCoreOnly inherits only a core set of variables (PATH, HOME, USER, SHELL, LANG, TERM, TMPDIR) plus language toolchain paths.
	EnvPolicyCoreOnly // Only PATH, HOME, USER, SHELL, LANG, TERM, TMPDIR + language paths
)

// LocalExecutionEnvironment runs commands and file operations on the local
// machine, rooted at RootDir and governed by EnvPolicy. It tracks the PIDs of
// started processes so they can be terminated on cleanup.
type LocalExecutionEnvironment struct {
	RootDir     string       // directory that file operations and commands are rooted at
	EnvPolicy   EnvVarPolicy // which env vars child processes inherit
	runningPIDs *sync.Map    // pid (int) → struct{}
}

// NewLocalExecutionEnvironment returns a LocalExecutionEnvironment rooted at
// rootDir with a fresh PID tracker.
func NewLocalExecutionEnvironment(rootDir string) *LocalExecutionEnvironment {
	return &LocalExecutionEnvironment{
		RootDir:     rootDir,
		runningPIDs: &sync.Map{},
	}
}

// WithWorkingDirectory returns a new LocalExecutionEnvironment that uses the
// given directory as its root but shares PID tracking with the parent.
func (e *LocalExecutionEnvironment) WithWorkingDirectory(dir string) *LocalExecutionEnvironment {
	return &LocalExecutionEnvironment{
		RootDir:     dir,
		EnvPolicy:   e.EnvPolicy,
		runningPIDs: e.runningPIDs,
	}
}

// Initialize prepares the environment for use.
func (e *LocalExecutionEnvironment) Initialize() error {
	if e.runningPIDs == nil {
		e.runningPIDs = &sync.Map{}
	}
	return nil
}

// Cleanup terminates any tracked processes by sending SIGTERM to each process
// group, waiting two seconds for graceful shutdown, then sending SIGKILL to
// every tracked process group (a no-op for those that already exited).
func (e *LocalExecutionEnvironment) Cleanup() {
	// Collect running PIDs and send SIGTERM.
	var pids []int
	e.runningPIDs.Range(func(key, _ any) bool {
		pids = append(pids, key.(int))
		return true
	})
	if len(pids) == 0 {
		return
	}
	for _, pid := range pids {
		terminateProcessGroup(pid)
	}
	// Wait for graceful shutdown, then SIGKILL survivors.
	time.Sleep(2 * time.Second)
	for _, pid := range pids {
		killProcessGroup(pid)
	}
}

// WorkingDirectory returns the environment's root directory.
func (e *LocalExecutionEnvironment) WorkingDirectory() string { return e.RootDir }

// Platform returns the operating system family as one of "darwin", "windows",
// or "linux" (the default for any other GOOS).
func (e *LocalExecutionEnvironment) Platform() string {
	switch runtime.GOOS {
	case "darwin":
		return "darwin"
	case "windows":
		return "windows"
	default:
		return "linux"
	}
}

// OSVersion returns the OS version string. On darwin and linux it shells out to
// "uname -rs"; on windows it runs "ver". If the command fails it falls back to
// "GOOS/GOARCH".
func (e *LocalExecutionEnvironment) OSVersion() string {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	switch runtime.GOOS {
	case "darwin", "linux":
		out, err := exec.CommandContext(ctx, "uname", "-rs").Output()
		if err == nil {
			return strings.TrimSpace(string(out))
		}
	case "windows":
		out, err := exec.CommandContext(ctx, "cmd", "/c", "ver").Output()
		if err == nil {
			return strings.TrimSpace(string(out))
		}
	}
	return runtime.GOOS + "/" + runtime.GOARCH // fallback
}

// ReadFile reads the file at path (resolved relative to RootDir) and returns
// its contents. Image and PDF files are returned as base64-encoded data with a
// descriptive header. Files containing a NUL byte are rejected as binary. For
// text files, lines are returned numbered, starting at offsetLine (default 1)
// and limited to limitLines lines (default 2000); CRLF line endings are
// normalized to LF.
func (e *LocalExecutionEnvironment) ReadFile(path string, offsetLine *int, limitLines *int) (string, error) {
	abs := e.resolve(path)
	b, err := os.ReadFile(abs)
	if err != nil {
		return "", err
	}
	// Image files: return base64-encoded data instead of erroring on binary.
	if format := detectImageFormat(path, b); format != "" {
		encoded := base64.StdEncoding.EncodeToString(b)
		return fmt.Sprintf("[image: %s, %d bytes, base64 data follows]\n%s", format, len(b), encoded), nil
	}
	// Document files (PDF): return base64-encoded data for vision/content pipeline.
	if format := detectDocumentFormat(path, b); format != "" {
		encoded := base64.StdEncoding.EncodeToString(b)
		return fmt.Sprintf("[document: %s, %d bytes, base64 data follows]\n%s", format, len(b), encoded), nil
	}
	// Basic binary detection.
	if bytes.IndexByte(b, 0) >= 0 {
		return "", fmt.Errorf("binary file (NUL byte): %s", path)
	}
	s := strings.ReplaceAll(string(b), "\r\n", "\n")
	lines := strings.Split(s, "\n")

	start := 1
	if offsetLine != nil && *offsetLine > 0 {
		start = *offsetLine
	}
	limit := 2000
	if limitLines != nil && *limitLines > 0 {
		limit = *limitLines
	}
	if start > len(lines) {
		return "", nil
	}
	end := start - 1 + limit
	if end > len(lines) {
		end = len(lines)
	}
	var out strings.Builder
	for i := start; i <= end; i++ {
		out.WriteString(fmt.Sprintf("%4d | %s\n", i, lines[i-1]))
	}
	return out.String(), nil
}

// WriteFile writes content to the file at path, creating any missing parent
// directories. The path must resolve to a location under RootDir. It returns a
// human-readable summary of the bytes written.
func (e *LocalExecutionEnvironment) WriteFile(path string, content string) (string, error) {
	abs, err := e.resolveWrite(path)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(abs, []byte(content), 0o644); err != nil {
		return "", err
	}
	return fmt.Sprintf("wrote %d bytes to %s", len(content), path), nil
}

// EditFile replaces occurrences of oldString with newString in the file at
// path, which must resolve to a location under RootDir. If oldString is not
// found exactly, a whitespace-normalized fuzzy match is attempted. Unless
// replaceAll is true, oldString must match exactly once. It returns a summary
// of the number of replacements made.
func (e *LocalExecutionEnvironment) EditFile(path string, oldString string, newString string, replaceAll bool) (string, error) {
	abs, err := e.resolveWrite(path)
	if err != nil {
		return "", err
	}
	b, err := os.ReadFile(abs)
	if err != nil {
		return "", err
	}
	s := string(b)
	fuzzyNote := ""
	if !strings.Contains(s, oldString) {
		// Fuzzy fallback: try whitespace-normalized matching.
		match := findFuzzyMatch(s, oldString)
		if match == "" {
			return "", fmt.Errorf("old_string not found in %s", path)
		}
		oldString = match
		fuzzyNote = " [NOTE: Matched with whitespace normalization]"
	}
	if !replaceAll && strings.Count(s, oldString) != 1 {
		return "", fmt.Errorf("old_string not unique in %s; use replace_all=true or provide a more specific old_string", path)
	}
	n := strings.Count(s, oldString)
	if replaceAll {
		s = strings.ReplaceAll(s, oldString, newString)
	} else {
		s = strings.Replace(s, oldString, newString, 1)
		n = 1
	}
	if err := os.WriteFile(abs, []byte(s), 0o644); err != nil {
		return "", err
	}
	return fmt.Sprintf("edited %s: %d replacement(s)%s", path, n, fuzzyNote), nil
}

// findFuzzyMatch scans the file content for a substring that matches
// oldString when whitespace is normalized. Returns the actual substring from
// the file, or "" if no match.
func findFuzzyMatch(content, oldString string) string {
	normOld := normalizeWS(oldString)
	if normOld == "" {
		return ""
	}
	// Scan lines for single-line matches.
	lines := strings.Split(content, "\n")
	for _, line := range lines {
		if normalizeWS(line) == normOld {
			return line
		}
	}
	// Multi-line: try sliding window of the same line count.
	oldLines := strings.Split(oldString, "\n")
	wSize := len(oldLines)
	if wSize > 1 && wSize <= len(lines) {
		for i := 0; i <= len(lines)-wSize; i++ {
			candidate := strings.Join(lines[i:i+wSize], "\n")
			if normalizeWS(candidate) == normOld {
				return candidate
			}
		}
	}
	return ""
}

// normalizeWS collapses all whitespace runs to single spaces and trims ends.
func normalizeWS(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// detectImageFormat checks file extension and magic bytes to identify image files.
// Returns the format name (e.g. "png", "jpeg") or "" if not an image.
func detectImageFormat(path string, data []byte) string {
	ext := strings.ToLower(filepath.Ext(path))
	imageExts := map[string]string{
		".png": "png", ".jpg": "jpeg", ".jpeg": "jpeg",
		".gif": "gif", ".webp": "webp", ".bmp": "bmp",
		".svg": "svg", ".ico": "ico",
	}
	if format, ok := imageExts[ext]; ok {
		return format
	}
	// Check magic bytes for images without recognized extension.
	if len(data) >= 8 && bytes.Equal(data[:8], []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A}) {
		return "png"
	}
	if len(data) >= 3 && data[0] == 0xFF && data[1] == 0xD8 && data[2] == 0xFF {
		return "jpeg"
	}
	if len(data) >= 6 && (string(data[:6]) == "GIF87a" || string(data[:6]) == "GIF89a") {
		return "gif"
	}
	return ""
}

// detectDocumentFormat checks file extension and magic bytes to identify document files
// that can be processed natively by the model (e.g. PDFs). Returns the format name or "".
func detectDocumentFormat(path string, data []byte) string {
	ext := strings.ToLower(filepath.Ext(path))
	if ext == ".pdf" {
		return "pdf"
	}
	// Check magic bytes: PDF files start with %PDF-
	if len(data) >= 5 && string(data[:5]) == "%PDF-" {
		return "pdf"
	}
	return ""
}

// FileExists reports whether a file or directory exists at path (resolved
// relative to RootDir).
func (e *LocalExecutionEnvironment) FileExists(path string) bool {
	_, err := os.Stat(e.resolve(path))
	return err == nil
}

// ListDirectory returns the entries under path (resolved relative to RootDir),
// recursing up to depth levels (depth <= 0 is treated as 1). Entries are sorted
// by name within each directory, nested names are prefixed with their relative
// path, and file sizes are populated.
func (e *LocalExecutionEnvironment) ListDirectory(path string, depth int) ([]DirEntry, error) {
	if depth <= 0 {
		depth = 1
	}
	root := e.resolve(path)

	var out []DirEntry
	var walk func(absDir string, relPrefix string, d int) error
	walk = func(absDir string, relPrefix string, d int) error {
		ents, err := os.ReadDir(absDir)
		if err != nil {
			return err
		}
		sort.SliceStable(ents, func(i, j int) bool { return ents[i].Name() < ents[j].Name() })
		for _, ent := range ents {
			name := ent.Name()
			relName := name
			if relPrefix != "" {
				relName = filepath.Join(relPrefix, name)
			}
			de := DirEntry{Name: relName, IsDir: ent.IsDir()}
			if !ent.IsDir() {
				if info, err := ent.Info(); err == nil {
					de.Size = info.Size()
				}
			}
			out = append(out, de)
			if ent.IsDir() && d > 1 {
				if err := walk(filepath.Join(absDir, name), relName, d-1); err != nil {
					return err
				}
			}
		}
		return nil
	}

	if err := walk(root, "", depth); err != nil {
		return nil, err
	}
	return out, nil
}

// Glob returns absolute paths matching pattern (doublestar syntax) under
// basePath, which defaults to RootDir and is resolved relative to RootDir when
// not absolute. Results are sorted by modification time, newest first, with
// ties broken by path.
func (e *LocalExecutionEnvironment) Glob(pattern string, basePath string) ([]string, error) {
	base := strings.TrimSpace(basePath)
	if base == "" {
		base = e.RootDir
	}
	if !filepath.IsAbs(base) {
		base = filepath.Join(e.RootDir, base)
	}
	matches, err := doublestar.Glob(os.DirFS(base), pattern)
	if err != nil {
		return nil, err
	}
	abs := make([]string, 0, len(matches))
	for _, m := range matches {
		abs = append(abs, filepath.Join(base, m))
	}
	sort.SliceStable(abs, func(i, j int) bool {
		fi, _ := os.Stat(abs[i])
		fj, _ := os.Stat(abs[j])
		if fi == nil || fj == nil {
			return abs[i] < abs[j]
		}
		if fi.ModTime() != fj.ModTime() {
			return fi.ModTime().After(fj.ModTime())
		}
		return abs[i] < abs[j]
	})
	return abs, nil
}

// Grep searches for pattern under path (defaulting to RootDir), using ripgrep
// when available and falling back to a native Go regex search otherwise.
// globFilter restricts which files are searched, caseInsensitive enables
// case-insensitive matching, and maxResults caps the output (default 100).
// outputMode selects the result format: "files_with_matches", "count", or
// matching lines otherwise.
func (e *LocalExecutionEnvironment) Grep(pattern string, path string, globFilter string, caseInsensitive bool, maxResults int, outputMode string) (string, error) {
	rg, err := exec.LookPath("rg")
	if err != nil {
		// Fallback to native Go regex search when ripgrep is absent
		dir := strings.TrimSpace(path)
		if dir == "" {
			dir = e.RootDir
		}
		if !filepath.IsAbs(dir) {
			dir = filepath.Join(e.RootDir, dir)
		}
		return e.grepNative(pattern, dir, globFilter, caseInsensitive, maxResults, outputMode)
	}
	dir := strings.TrimSpace(path)
	if dir == "" {
		dir = e.RootDir
	}
	if !filepath.IsAbs(dir) {
		dir = filepath.Join(e.RootDir, dir)
	}

	args := []string{"--no-heading", "--color", "never"}
	switch outputMode {
	case "files_with_matches":
		args = append(args, "--files-with-matches")
	case "count":
		args = append(args, "--count")
	default:
		args = append(args, "--line-number")
	}
	if caseInsensitive {
		args = append(args, "-i")
	}
	if strings.TrimSpace(globFilter) != "" {
		args = append(args, "-g", globFilter)
	}
	args = append(args, pattern, dir)

	ctx := context.Background()
	if maxResults <= 0 {
		maxResults = 100
	}
	res, err := e.ExecCommand(ctx, rg+" "+shellEscapeArgs(args...), 10_000, e.RootDir, nil)
	if err == nil {
		// Best-effort cap: keep first maxResults lines.
		lines := strings.Split(res.Stdout, "\n")
		if len(lines) > maxResults {
			lines = lines[:maxResults]
		}
		return strings.Join(lines, "\n"), nil
	}
	// Exit code 1 means "no matches" for rg.
	if res.ExitCode == 1 {
		return "", nil
	}
	return res.Stdout + res.Stderr, err
}

func (e *LocalExecutionEnvironment) grepNative(pattern, path, globFilter string, caseInsensitive bool, maxResults int, outputMode string) (string, error) {
	flags := ""
	if caseInsensitive {
		flags = "(?i)"
	}
	re, err := regexp.Compile(flags + pattern)
	if err != nil {
		return "", fmt.Errorf("invalid regex: %w", err)
	}

	if maxResults <= 0 {
		maxResults = 100
	}

	var results []string
	fileCounts := map[string]int{}     // for "count" mode
	filesSeen := map[string]struct{}{} // for "files_with_matches" mode
	totalResults := 0

	err = filepath.WalkDir(path, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil //nolint:nilerr // best-effort grep: skip unreadable entries and keep walking
		}
		if d.IsDir() {
			if strings.HasPrefix(d.Name(), ".") && p != path {
				return filepath.SkipDir
			}
			return nil
		}
		// Skip hidden files
		if strings.HasPrefix(d.Name(), ".") {
			return nil
		}
		if globFilter != "" {
			matched, _ := filepath.Match(globFilter, filepath.Base(p))
			if !matched {
				return nil
			}
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return nil //nolint:nilerr // best-effort grep: skip unreadable files and keep walking
		}
		// Skip binary files
		if bytes.IndexByte(data, 0) >= 0 {
			return nil
		}
		relPath, _ := filepath.Rel(path, p)
		lines := strings.Split(string(data), "\n")
		for i, line := range lines {
			if re.MatchString(line) {
				switch outputMode {
				case "files_with_matches":
					if _, seen := filesSeen[relPath]; !seen {
						filesSeen[relPath] = struct{}{}
						results = append(results, relPath)
						totalResults++
						if totalResults >= maxResults {
							return filepath.SkipAll
						}
					}
					// Once we've recorded this file, skip to next file
					return nil
				case "count":
					fileCounts[relPath]++
				default: // "content" or ""
					results = append(results, fmt.Sprintf("%s:%d:%s", relPath, i+1, line))
					totalResults++
					if totalResults >= maxResults {
						return filepath.SkipAll
					}
				}
			}
		}
		return nil
	})
	if err != nil {
		return "", err
	}

	if outputMode == "count" {
		// Build sorted output from fileCounts
		var countResults []string
		for file, cnt := range fileCounts {
			countResults = append(countResults, fmt.Sprintf("%s:%d", file, cnt))
		}
		sort.Strings(countResults)
		return strings.Join(countResults, "\n"), nil
	}
	return strings.Join(results, "\n"), nil
}

// ExecCommand runs command through the platform shell in its own process group,
// rooted at workingDir (defaulting to RootDir and required to be under RootDir).
// The environment is built from EnvPolicy plus envVars, with any local
// virtualenv bin directory prepended to PATH. The command is terminated if ctx
// is cancelled or timeoutMS elapses (default 10000ms), escalating from SIGTERM
// to SIGKILL. It returns an ExecResult capturing stdout, stderr, exit code,
// timeout status, and duration.
func (e *LocalExecutionEnvironment) ExecCommand(ctx context.Context, command string, timeoutMS int, workingDir string, envVars map[string]string) (ExecResult, error) {
	if timeoutMS <= 0 {
		timeoutMS = 10_000
	}
	dir := strings.TrimSpace(workingDir)
	if dir == "" {
		dir = e.RootDir
	}
	if !filepath.IsAbs(dir) {
		dir = filepath.Join(e.RootDir, dir)
	}
	if err := e.ensureUnderRoot(dir); err != nil {
		return ExecResult{ExitCode: 127}, fmt.Errorf("working directory %w", err)
	}

	start := time.Now()
	cmd := shellCommand(command)
	cmd.Dir = dir
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Env = filteredEnvWithPolicy(e.EnvPolicy, envVars)
	cmd.Env = injectLocalVenvPath(cmd.Env, []string{dir, e.RootDir})

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Start(); err != nil {
		return ExecResult{ExitCode: 127}, err
	}
	pid := cmd.Process.Pid
	e.runningPIDs.Store(pid, struct{}{})
	defer e.runningPIDs.Delete(pid)

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	timedOut := false
	interrupted := false
	var waitErr error
	select {
	case <-ctx.Done():
		interrupted = true
		waitErr = ctx.Err()
		timedOut = errors.Is(waitErr, context.DeadlineExceeded)
	case err := <-done:
		waitErr = err
	case <-time.After(time.Duration(timeoutMS) * time.Millisecond):
		interrupted = true
		timedOut = true
		waitErr = context.DeadlineExceeded
	}

	if interrupted {
		terminateProcessGroup(cmd.Process.Pid)
		select {
		case <-done:
			// exited on SIGTERM
		case <-time.After(2 * time.Second):
			killProcessGroup(cmd.Process.Pid)
			// Best-effort: wait a bit for Wait() to return so we don't leak the goroutine.
			select {
			case <-done:
			case <-time.After(2 * time.Second):
			}
		}
	}

	exitCode := 0
	if waitErr != nil {
		ee := &exec.ExitError{}
		if errors.As(waitErr, &ee) {
			exitCode = ee.ExitCode()
		} else if timedOut {
			exitCode = 124
		} else if errors.Is(waitErr, context.Canceled) {
			exitCode = 130
		} else {
			exitCode = 1
		}
	}

	return ExecResult{
		Stdout:     stdout.String(),
		Stderr:     stderr.String(),
		ExitCode:   exitCode,
		TimedOut:   timedOut,
		DurationMS: time.Since(start).Milliseconds(),
	}, waitErr
}

// StreamCommand runs command through the platform shell in its own process
// group, streaming combined stdout/stderr to out and returning immediately with
// a handle for waiting on or signalling the command.
func (e *LocalExecutionEnvironment) StreamCommand(ctx context.Context, command, workingDir string, envVars map[string]string, out io.Writer) (*StreamHandle, error) {
	if e.runningPIDs == nil {
		e.runningPIDs = &sync.Map{}
	}
	dir := strings.TrimSpace(workingDir)
	if dir == "" {
		dir = e.RootDir
	}
	if !filepath.IsAbs(dir) {
		dir = filepath.Join(e.RootDir, dir)
	}
	if err := e.ensureUnderRoot(dir); err != nil {
		return nil, fmt.Errorf("working directory %w", err)
	}

	if ctx != nil {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}
	}

	cmd := shellCommand(command)
	cmd.Dir = dir
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Env = filteredEnvWithPolicy(e.EnvPolicy, envVars)
	cmd.Env = injectLocalVenvPath(cmd.Env, []string{dir, e.RootDir})
	cmd.Stdout = out
	cmd.Stderr = out

	if err := cmd.Start(); err != nil {
		return nil, err
	}
	pid := cmd.Process.Pid
	e.runningPIDs.Store(pid, struct{}{})
	detachedStart := false
	if d, ok := ctx.(interface{ DetachAfterStart() }); ok {
		d.DetachAfterStart()
		detachedStart = true
	}

	done := make(chan struct{})
	var doneOnce sync.Once
	var signalOnce sync.Once
	signal := func() {
		select {
		case <-done:
			return
		default:
		}
		signalOnce.Do(func() {
			select {
			case <-done:
				return
			default:
			}
			terminateProcessGroup(pid)
			go func() {
				timer := time.NewTimer(2 * time.Second)
				defer timer.Stop()
				select {
				case <-done:
					return
				case <-timer.C:
					select {
					case <-done:
						return
					default:
						killProcessGroup(pid)
					}
				}
			}()
		})
	}

	if ctx != nil && !detachedStart {
		go func() {
			select {
			case <-ctx.Done():
				signal()
			case <-done:
			}
		}()
	}

	wait := func() (int, error) {
		defer doneOnce.Do(func() { close(done) })
		defer e.runningPIDs.Delete(pid)
		if err := cmd.Wait(); err != nil {
			ee := &exec.ExitError{}
			if errors.As(err, &ee) {
				return ee.ExitCode(), nil
			}
			return 127, err
		}
		return 0, nil
	}

	return &StreamHandle{
		Pid:    pid,
		Wait:   wait,
		Signal: signal,
	}, nil
}

func injectLocalVenvPath(env []string, roots []string) []string {
	if len(env) == 0 || len(roots) == 0 {
		return env
	}

	seenRoots := map[string]struct{}{}
	var uniqueRoots []string
	for _, r := range roots {
		r = strings.TrimSpace(r)
		if r == "" {
			continue
		}
		r = filepath.Clean(r)
		if _, ok := seenRoots[r]; ok {
			continue
		}
		seenRoots[r] = struct{}{}
		uniqueRoots = append(uniqueRoots, r)
	}
	if len(uniqueRoots) == 0 {
		return env
	}

	binDir := "bin"
	if runtime.GOOS == "windows" {
		binDir = "Scripts"
	}

	seenDirs := map[string]struct{}{}
	var prefixDirs []string
	for _, root := range uniqueRoots {
		candidates := []string{
			filepath.Join(root, ".venv", binDir),
			filepath.Join(root, "venv", binDir),
		}
		for _, cand := range candidates {
			info, err := os.Stat(cand)
			if err != nil || !info.IsDir() {
				continue
			}
			if _, ok := seenDirs[cand]; ok {
				continue
			}
			seenDirs[cand] = struct{}{}
			prefixDirs = append(prefixDirs, cand)
		}
	}
	if len(prefixDirs) == 0 {
		return env
	}

	sep := string(os.PathListSeparator)
	findPath := func(env []string) (int, string) {
		for i, kv := range env {
			if strings.HasPrefix(kv, "PATH=") {
				return i, strings.TrimPrefix(kv, "PATH=")
			}
		}
		return -1, ""
	}

	idx, existing := findPath(env)
	if existing != "" {
		parts := strings.Split(existing, sep)
		existingSet := map[string]struct{}{}
		for _, p := range parts {
			p = strings.TrimSpace(p)
			if p == "" {
				continue
			}
			existingSet[p] = struct{}{}
		}
		var filteredPrefix []string
		for _, d := range prefixDirs {
			if _, ok := existingSet[d]; ok {
				continue
			}
			filteredPrefix = append(filteredPrefix, d)
		}
		prefixDirs = filteredPrefix
	}
	if len(prefixDirs) == 0 {
		return env
	}

	prefix := strings.Join(prefixDirs, sep)
	var newPath string
	if existing == "" {
		newPath = prefix
	} else {
		newPath = prefix + sep + existing
	}

	if idx >= 0 {
		env[idx] = "PATH=" + newPath
		return env
	}
	return append(env, "PATH="+newPath)
}

// shellCommand returns an *exec.Cmd that runs the given command string
// through the platform's default shell. The command's lifecycle (cancellation,
// timeout) is managed by the caller (ExecCommand) via its own process-group
// SIGTERM->SIGKILL escalation, so CommandContext is deliberately not used here.
func shellCommand(command string) *exec.Cmd {
	if runtime.GOOS == "windows" {
		return exec.Command("cmd.exe", "/c", command) //nolint:noctx // lifecycle managed by ExecCommand's process-group kill
	}
	shell := "/bin/bash"
	if _, err := os.Stat(shell); err != nil {
		shell = "/bin/sh"
	}
	return exec.Command(shell, "-c", command) //nolint:noctx // lifecycle managed by ExecCommand's process-group kill
}

func (e *LocalExecutionEnvironment) resolve(path string) string {
	p := strings.TrimSpace(path)
	if p == "" {
		return e.RootDir
	}
	if filepath.IsAbs(p) {
		return p
	}
	return filepath.Join(e.RootDir, p)
}

// resolveWrite is the boundary-enforcing counterpart to resolve. Unlike
// resolve (which trusts reads), resolveWrite rejects paths that escape the
// working directory so write_file / edit_file can't reach into the
// orchestrator's source tree or anywhere else outside the project.
func (e *LocalExecutionEnvironment) resolveWrite(path string) (string, error) {
	p := strings.TrimSpace(path)
	if p == "" {
		return "", errors.New("empty path")
	}
	abs := p
	if !filepath.IsAbs(abs) {
		abs = filepath.Join(e.RootDir, abs)
	}
	abs = filepath.Clean(abs)
	if err := e.ensureUnderRoot(abs); err != nil {
		return "", fmt.Errorf("%s: %w", path, err)
	}
	return abs, nil
}

// ensureUnderRoot reports whether the cleaned absolute path equals RootDir
// or is a descendant of it. Both paths are resolved through EvalSymlinks
// (best-effort; missing leaves resolve via their parent) so that symlink
// canonicalisation differences like macOS's /var -> /private/var don't
// false-positive as escapes. Callers are responsible for cleaning and
// absolutising the path before calling.
func (e *LocalExecutionEnvironment) ensureUnderRoot(abs string) error {
	abs = resolveSymlinksBestEffort(abs)
	root := resolveSymlinksBestEffort(e.RootDir)
	if abs == root {
		return nil
	}
	if strings.HasPrefix(abs, root+string(filepath.Separator)) {
		return nil
	}
	return fmt.Errorf("is outside working directory %q", e.RootDir)
}

// resolveSymlinksBestEffort resolves symlinks in path. For write targets the
// leaf (and sometimes intermediate dirs) may not exist yet, so this walks up
// the ancestors until it finds one that does, resolves that, and re-joins
// the missing suffix. Returns a cleaned path in all cases.
func resolveSymlinksBestEffort(path string) string {
	path = filepath.Clean(path)
	if r, err := filepath.EvalSymlinks(path); err == nil {
		return filepath.Clean(r)
	}
	suffix := []string{}
	cur := path
	for {
		parent, base := filepath.Split(cur)
		parent = strings.TrimRight(parent, string(filepath.Separator))
		if parent == "" || parent == cur {
			return path
		}
		suffix = append([]string{base}, suffix...)
		cur = parent
		if r, err := filepath.EvalSymlinks(cur); err == nil {
			return filepath.Clean(filepath.Join(append([]string{r}, suffix...)...))
		}
	}
}

func terminateProcessGroup(pid int) {
	if pid <= 0 {
		return
	}
	_ = syscall.Kill(-pid, syscall.SIGTERM)
}

func killProcessGroup(pid int) {
	if pid <= 0 {
		return
	}
	_ = syscall.Kill(-pid, syscall.SIGKILL)
}

func filteredEnvWithPolicy(policy EnvVarPolicy, extra map[string]string) []string {
	switch policy {
	case EnvPolicyAll:
		out := os.Environ()
		for k, v := range extra {
			out = append(out, k+"="+v)
		}
		return out
	case EnvPolicyNone:
		out := make([]string, 0, len(extra))
		for k, v := range extra {
			out = append(out, k+"="+v)
		}
		return out
	case EnvPolicyCoreOnly:
		core := map[string]bool{
			"PATH": true, "HOME": true, "USER": true,
			"SHELL": true, "LANG": true, "TERM": true,
			"TMPDIR": true, "GOPATH": true, "GOMODCACHE": true,
			"CARGO_HOME": true, "RUSTUP_HOME": true,
			"NVM_DIR": true, "PYENV_ROOT": true,
		}
		out := []string{}
		for _, kv := range os.Environ() {
			k, _, ok := strings.Cut(kv, "=")
			if ok && core[k] {
				out = append(out, kv)
			}
		}
		for k, v := range extra {
			out = append(out, k+"="+v)
		}
		return out
	default: // EnvPolicyDefault
		return filteredEnv(extra)
	}
}

func filteredEnv(extra map[string]string) []string {
	deny := func(k string) bool {
		uk := strings.ToUpper(k)
		return strings.Contains(uk, "API_KEY") || strings.Contains(uk, "SECRET") || strings.Contains(uk, "TOKEN") || strings.Contains(uk, "PASSWORD") || strings.Contains(uk, "CREDENTIAL")
	}
	out := []string{}
	for _, kv := range os.Environ() {
		k, _, ok := strings.Cut(kv, "=")
		if !ok {
			continue
		}
		if deny(k) {
			continue
		}
		out = append(out, kv)
	}
	for k, v := range extra {
		if deny(k) {
			continue
		}
		out = append(out, k+"="+v)
	}
	return out
}

func shellEscapeArgs(args ...string) string {
	var b strings.Builder
	for i, a := range args {
		if i > 0 {
			b.WriteByte(' ')
		}
		b.WriteString(shellEscape(a))
	}
	return b.String()
}

func shellEscape(s string) string {
	if s == "" {
		return "''"
	}
	if strings.IndexFunc(s, func(r rune) bool {
		return r == ' ' || r == '\t' || r == '\n' || r == '"' || r == '\'' || r == '\\' || r == '$' || r == '`' || r == '!' || r == '(' || r == ')' || r == ';' || r == '|' || r == '&' || r == '<' || r == '>' || r == '*'
	}) == -1 {
		return s
	}
	// Single-quote escape strategy for bash.
	return "'" + strings.ReplaceAll(s, "'", `'"'"'`) + "'"
}
