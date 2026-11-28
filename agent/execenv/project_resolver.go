package execenv

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"

	"primeradiant.com/evener/identifier"
)

// NewProjectResolver binds identifier's project policy to an execution
// environment. The identifier package remains the policy owner; this adapter
// only supplies path and Git operations through env.
func NewProjectResolver(env ExecutionEnvironment) identifier.Resolver {
	if isNilExecutionEnvironment(env) {
		return (*projectResolver)(nil)
	}
	return &projectResolver{env: env}
}

type projectResolver struct{ env ExecutionEnvironment }

func isNilExecutionEnvironment(env ExecutionEnvironment) bool {
	if env == nil {
		return true
	}
	value := reflect.ValueOf(env)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}

func (r *projectResolver) Abs(path string) (string, error) {
	if filepath.IsAbs(path) {
		return filepath.Clean(path), nil
	}
	base := r.env.WorkingDirectory()
	if base == "" {
		return "", errors.New("resolve project relative path: execution environment working directory is empty")
	}
	absolute, err := filepath.Abs(filepath.Join(base, path))
	if err != nil {
		return "", err
	}
	return filepath.Clean(absolute), nil
}

func (r *projectResolver) EvalSymlinks(path string) (string, error) {
	if _, local := r.env.(*LocalExecutionEnvironment); local {
		resolved, err := filepath.EvalSymlinks(path)
		if err != nil {
			return "", err
		}
		info, err := os.Stat(resolved)
		if err != nil {
			return "", err
		}
		if !info.IsDir() {
			return "", fmt.Errorf("%q is not a directory", resolved)
		}
		return resolved, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), gitExecTimeout)
	defer cancel()
	result, err := r.env.ExecCommand(ctx, "pwd -P", gitExecTimeoutMS(), path, nil)
	if err != nil {
		return "", fmt.Errorf("evaluate symlinks with pwd -P: %w", err)
	}
	if result.ExitCode != 0 {
		return "", fmt.Errorf("pwd -P exited with code %d", result.ExitCode)
	}
	resolved := strings.TrimSpace(result.Stdout)
	if resolved == "" {
		return "", errors.New("pwd -P returned blank output")
	}
	return filepath.Clean(resolved), nil
}

func (r *projectResolver) MainCheckout(path string) (string, bool, error) {
	return resolveMainRepoRoot(r.env, path)
}

// resolveMainRepoRoot is the strict execution-environment boundary used by the
// identifier adapter. A missing .git entry is the only non-Git success. Once a
// Git entry is observed, every malformed, incomplete, or contradictory result
// remains Git and returns an error instead of silently changing identity.
func resolveMainRepoRoot(env ExecutionEnvironment, cwd string) (root string, isGit bool, err error) {
	if _, local := env.(*LocalExecutionEnvironment); local {
		if root, detected, handled, err := localStructuralMainRoot(cwd); handled {
			return root, detected, err
		}
	}
	return gitBinaryMainRootStrict(env, cwd)
}

func envHasGitEntryAncestor(env ExecutionEnvironment, cwd string) bool {
	dir := filepath.Clean(cwd)
	for {
		if env.FileExists(filepath.Join(dir, ".git")) {
			return true
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return false
		}
		dir = parent
	}
}

// localStructuralMainRoot resolves a checkout's main root from the ".git"
// layout. handled says the walk answered the question; when it is false the
// caller falls through to the Git binary, which resolves a program from PATH —
// a PATH workspace directories can reach. So handled=false is allowed only
// where the walk has NOT established what the entry is. Every ".git" shape is
// enumerated here, and each one says which it is:
//
//   - .git directory: terminal, the checkout is its own main root.
//   - regular .git pointer file: terminal, linked-worktree pointer (main root,
//     validated) or submodule pointer. A validated submodule pointer is the one
//     pointer that falls through (handled=false), because the submodule's own
//     root is the answer and only Git reports it here.
//   - symlink to a directory: terminal, it is a .git directory. os.Stat follows
//     the link, so this arrives as the directory case.
//   - symlink to a regular pointer file: terminal, the pointer cases above.
//   - dangling symlink (the link exists, its target does not): terminal refusal.
//     os.Stat reports ErrNotExist here, which is NOT an absence: an entry that
//     exists and cannot be resolved is a refused entry. The link itself is
//     Lstat'd to establish which it is before the walk moves on.
//   - symlink to a readable pseudo-filesystem stream (/proc/kmsg and its
//     aliases): terminal refusal, decided on the OPENED descriptor so no swap
//     between stat and open can hide it. Such a file stats regular and can
//     block in read(2) forever, so the byte cap alone does not bound it.
//   - symlink that does not resolve, FIFO, socket, device: terminal refusal.
//     The entry exists and has been read and refused; answering handled=false
//     would escalate to running a workspace-reachable program instead.
//   - pointer file larger than any pointer: terminal refusal, same reason. It
//     is also never read whole.
//   - unreadable .git (permission, I/O error): terminal error, again never a
//     fall-through.
//   - no .git entry in this directory or any ancestor: terminal non-Git. This is
//     the ONLY row where absence is claimed, and it is established by Lstat
//     reporting ErrNotExist for the entry itself — never inferred from a read,
//     open, or stat of something else that failed.
//   - a stat that failed for a reason other than absence: terminal error,
//     because nothing established what is there.
func localStructuralMainRoot(cwd string) (root string, isGit bool, handled bool, err error) {
	dir := filepath.Clean(cwd)
	for {
		gitPath := filepath.Join(dir, ".git")
		info, statErr := os.Stat(gitPath)
		if statErr != nil && !errors.Is(statErr, fs.ErrNotExist) {
			// Only an absence the filesystem actually reported lets the walk
			// move on: a stat that failed for want of permission (or on a
			// filesystem error) leaves it unknown whether a .git is there. Say
			// so rather than walking past it, or the caller memoizes an absence
			// nothing ever observed — the walk shares gitRootCache with
			// GitRootOrEmpty, and a cached "not a repository" outlives the
			// condition that produced it. This error is not an exit status, so
			// mainRepoRootUncached's classification treats it as transient.
			return "", false, true, fmt.Errorf("stat %q: %w", gitPath, statErr)
		}
		if errors.Is(statErr, fs.ErrNotExist) {
			// ErrNotExist from Stat means the PATH does not resolve, which is not
			// the same as the ENTRY being absent: a dangling symlink reports
			// ErrNotExist here while the link itself is plainly there. Absence is
			// established on the entry, not inferred from the failed resolution.
			// Walking past a dangling link would let a nested checkout inherit its
			// enclosing repository's identity.
			linkInfo, linkErr := os.Lstat(gitPath)
			if linkErr == nil {
				// The entry is there: a dangling symlink, or any other entry whose
				// target cannot be resolved. Refused, not absent.
				return "", true, true, fmt.Errorf("stat %q: %s: %w", gitPath, linkInfo.Mode().Type(), ErrNotRegularFile)
			}
			if !errors.Is(linkErr, fs.ErrNotExist) {
				return "", true, true, fmt.Errorf("lstat %q: %w", gitPath, linkErr)
			}
			// The entry itself is absent: this directory is not a checkout.
			parent := filepath.Dir(dir)
			if parent == dir {
				return "", false, true, nil
			}
			dir = parent
			continue
		}
		if statErr == nil {
			if info.IsDir() {
				return identifier.ResolveClean(dir), true, true, nil
			}
			content, readErr := ReadGitPointerFile(gitPath)
			if readErr != nil {
				// The entry exists and was refused as a pointer: a FIFO with no
				// writer, a socket, a device, an unresolvable symlink, a file
				// larger than any pointer, or an unreadable file. That is a
				// structural answer about this checkout, not an unknown, so it
				// ends resolution here. Returning handled=false would hand the
				// question to the Git binary, which searches a PATH that
				// workspace directories can reach.
				return "", true, true, fmt.Errorf("read Git pointer %q: %w", gitPath, readErr)
			}
			contentString := string(content)
			if root, ok := identifier.MainRootFromGitdirPointer(contentString, dir); ok {
				if err := identifier.ValidateLinkedWorktreePointer(contentString, dir, root); err != nil {
					return "", true, true, err
				}
				return identifier.ResolveClean(root), true, true, nil
			}
			if _, ok := identifier.ParseGitdirPointer(contentString); !ok {
				return "", true, true, errors.New("malformed Git worktree pointer")
			}
			if err := identifier.ValidateSubmodulePointer(contentString, dir); err != nil {
				return "", true, true, err
			}
			return "", true, false, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", false, true, nil
		}
		dir = parent
	}
}

// ReadGitPointerFile reads a .git pointer file through the guarded reader the
// identifier package owns: the leaf may be a symlink, the resolved target must
// be a bounded regular file, and a FIFO with no writer, a socket, a device, a
// pseudo-filesystem stream, or a file larger than any pointer is refused rather
// than blocking or over-reading. The guard lives there because sandbox policy
// classification and project identity resolution read the same workspace-
// controlled entry, and this package imports both of them.
func ReadGitPointerFile(path string) ([]byte, error) {
	return identifier.ReadGitPointerFile(path)
}

func gitBinaryMainRootStrict(env ExecutionEnvironment, cwd string) (string, bool, error) {
	common, err := execGitOutput(env, cwd, "rev-parse", "--git-common-dir")
	if err != nil {
		if !isLocalEnv(env) && !envHasGitEntryAncestor(env, cwd) {
			return "", false, nil
		}
		return "", true, fmt.Errorf("resolve Git common directory: %w", err)
	}
	candidate := identifier.MainRootCandidateFromCommonDir(cwd, common)
	if candidate != "" && isLocalEnv(env) && identifier.GitEntryResolvesToCommon(candidate, common) {
		return candidate, true, nil
	}
	commonPath := common
	if !filepath.IsAbs(commonPath) {
		commonPath = filepath.Join(cwd, commonPath)
	}
	commonPath = filepath.Clean(commonPath)

	top, err := execGitOutput(env, cwd, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", true, fmt.Errorf("resolve Git checkout root: %w", err)
	}
	if !pathContains(top, cwd, isLocalEnv(env)) {
		return "", true, fmt.Errorf("git checkout root %q does not contain %q", top, cwd)
	}
	if identifier.IsSubmoduleGitDirShape(commonPath) {
		if !isLocalEnv(env) && !env.FileExists(commonPath) {
			return "", true, fmt.Errorf("git submodule common directory %q does not exist", commonPath)
		}
		return resolveCleanForEnv(env, top), true, nil
	}
	if !isLocalEnv(env) {
		if candidate == "" || !env.FileExists(filepath.Join(candidate, ".git")) || commonPath != filepath.Join(filepath.Clean(candidate), ".git") {
			return "", true, fmt.Errorf("git common directory %q does not identify a validated checkout root", common)
		}
		return filepath.Clean(candidate), true, nil
	}
	if candidate == "" || !pathContains(candidate, top, true) {
		return "", true, fmt.Errorf("git common directory %q does not identify checkout root %q", common, top)
	}
	return resolveCleanForEnv(env, candidate), true, nil
}

func execGitOutput(env ExecutionEnvironment, cwd string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), gitExecTimeout)
	defer cancel()
	result, err := RunGit(ctx, env, cwd, gitExecTimeoutMS(), args...)
	if err != nil {
		return "", err
	}
	if result.ExitCode != 0 {
		return "", fmt.Errorf("command exited with code %d", result.ExitCode)
	}
	// A truncated STDOUT capture is head and tail fragments; a partial path
	// decoded here would be a wrong answer, so refuse it (#4568 round 2). The
	// answer comes from stdout, so a capped STDERR still leaves it whole
	// (#4621 round 16).
	if result.StdoutTruncated {
		return "", ErrOutputTruncated
	}
	output := strings.TrimSpace(result.Stdout)
	if output == "" {
		return "", errors.New("command returned blank output")
	}
	return output, nil
}

func isLocalEnv(env ExecutionEnvironment) bool {
	_, ok := env.(*LocalExecutionEnvironment)
	return ok
}

func pathContains(root, path string, resolveSymlinks bool) bool {
	if resolveSymlinks {
		root = identifier.ResolveClean(root)
		path = identifier.ResolveClean(path)
	}
	return identifier.PathUnder(path, root)
}

func resolveCleanForEnv(env ExecutionEnvironment, path string) string {
	if isLocalEnv(env) {
		return identifier.ResolveClean(path)
	}
	return filepath.Clean(path)
}
