package execenv

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"primeradiant.com/evener/agent/sandbox"
	"primeradiant.com/evener/envvars"
	"primeradiant.com/evener/execsupport/shellquote"
	"primeradiant.com/evener/identifier"
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

var coreEnvVars = []envvars.Var{
	envvars.Path,
	envvars.Home,
	envvars.User,
	envvars.Shell,
	envvars.Lang,
	envvars.Term,
	envvars.TmpDir,
	envvars.XDGConfigHome,
	envvars.XDGCacheHome,
	envvars.GoCache,
	envvars.GoEnv,
	envvars.GoPath,
	envvars.GoModCache,
	envvars.CargoHome,
	envvars.RustupHome,
	envvars.NVMDir,
	envvars.PyenvRoot,
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

// resolveWrite resolves a mutation path relative to the working directory and
// applies the active file-tool boundary. An unrestricted off session imposes no
// workspace restriction; confined policies retain their workspace and scratch
// limits.
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
	if err := e.ensureWritePath(abs); err != nil {
		return "", fmt.Errorf("%s: %w", path, err)
	}
	return abs, nil
}

// ensureWritePath imposes no workspace boundary on unrestricted off sessions.
// Confined policies permit the workspace root and, only after that check fails,
// the one scratch root already allocated to this environment. The scratch
// fallback is used on platforms without the fd layer; Linux and macOS
// route matching operations through scratchSandboxFor first. It deliberately
// uses symlink-aware best-effort canonicalization and never treats a
// caller-supplied absolute path as a grant.
func (e *LocalExecutionEnvironment) ensureWritePath(abs string) error {
	if e.Sandbox == nil || !e.Sandbox.FileToolConfined() {
		return nil
	}
	if err := e.ensureUnderRoot(abs); err == nil {
		return nil
	}
	if scratch := e.allocatedSessionScratchPath(); scratch != "" && pathWithinRoot(abs, scratch) {
		return nil
	}
	return fmt.Errorf("is outside working directory %q", e.RootDir)
}

// ensureUnderRoot reports whether the cleaned absolute path equals RootDir
// or is a descendant of it. Both paths are resolved through EvalSymlinks
// (best-effort; missing leaves resolve via their parent) so that symlink
// canonicalisation differences like macOS's /var -> /private/var don't
// false-positive as escapes. Callers are responsible for cleaning and
// absolutising the path before calling.
func (e *LocalExecutionEnvironment) ensureUnderRoot(abs string) error {
	if pathWithinRoot(abs, e.RootDir) {
		return nil
	}
	return fmt.Errorf("is outside working directory %q", e.RootDir)
}

// pathWithinRoot applies the existing symlink-aware escape check for an
// arbitrary authorized root. Missing leaves are resolved through their nearest
// existing ancestor, while a symlinked component that points outside remains
// outside. The fd-anchored Unix path performs the final race-safe decision when
// a scratch target is actually opened.
func pathWithinRoot(abs, root string) bool {
	return identifier.PathUnder(resolveSymlinksBestEffort(abs), resolveSymlinksBestEffort(root))
}

// EnsureCommandWorkingDirectory applies workspace containment to command cwd
// only when the environment carries a confined policy. A write-blocked off
// delegate keeps its chosen role boundary even without a kernel wrapper.
func (e *LocalExecutionEnvironment) EnsureCommandWorkingDirectory(abs string) error {
	if e.Sandbox == nil || !e.Sandbox.FileToolConfined() {
		return nil
	}
	return e.ensureUnderRoot(abs)
}

// EnsureUnderRoot validates an already-resolved absolute path against the working
// root, independently of sandbox mode. Attachment callers use this narrower
// scope even when file tools and command cwd are unrestricted.
func (e *LocalExecutionEnvironment) EnsureUnderRoot(abs string) error {
	return e.ensureUnderRoot(abs)
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

func filteredEnvWithPolicy(policy EnvVarPolicy, extra map[string]string) []string {
	return filteredEnvWithSource(policy, extra, os.Environ())
}

func filteredEnvWithSource(policy EnvVarPolicy, extra map[string]string, inherited []string) []string {
	switch policy {
	case EnvPolicyAll:
		out := make([]string, 0, len(inherited)+len(extra))
		for _, kv := range inherited {
			k, _, ok := strings.Cut(kv, "=")
			if ok {
				if _, overridden := extra[k]; overridden {
					continue
				}
			}
			out = append(out, kv)
		}
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
		core := make(map[string]bool, len(coreEnvVars))
		for _, v := range coreEnvVars {
			core[v.Name] = true
		}
		out := []string{}
		for _, kv := range inherited {
			k, _, ok := strings.Cut(kv, "=")
			if !ok || !core[k] {
				continue
			}
			if _, overridden := extra[k]; overridden {
				continue
			}
			out = append(out, kv)
		}
		for k, v := range extra {
			out = append(out, k+"="+v)
		}
		return out
	default: // EnvPolicyDefault
		return filteredEnvFrom(extra, inherited)
	}
}

func filteredEnv(extra map[string]string) []string {
	return filteredEnvFrom(extra, os.Environ())
}

// filteredEnvFrom scrubs secret-named vars from inherited, then layers extra
// on top. A key present in extra always wins over the SAME key in inherited
// (skipped rather than duplicated) so a caller-supplied override — e.g. the
// login-shell PATH or the always-on scratch vars — replaces the inherited
// value instead of appending a second, shadowing entry.
func filteredEnvFrom(extra map[string]string, inherited []string) []string {
	deny := sandbox.IsSecretEnvName
	out := []string{}
	for _, kv := range inherited {
		k, _, ok := strings.Cut(kv, "=")
		if !ok {
			continue
		}
		if deny(k) {
			continue
		}
		if _, overridden := extra[k]; overridden {
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

// ShellEscapeArgs joins args into a single POSIX-shell command string, quoting
// each token so it survives the shell word-splitting ExecCommand performs for a
// POSIX shell. It is the argv-discipline helper used to assemble shell command
// strings (spec §2 "name validation": "Do not hand-build shell command
// strings"), so on that path a worktree name or path cannot inject a shell
// metacharacter. The quoting itself lives in execsupport/shellquote so the whole
// product shares one implementation; this name is kept for its callers, and
// each argument is rendered with shellquote.Literal.
//
// It is NOT cmd.exe quoting, and Windows callers must not build an ExecCommand
// string with it. ExecCommand runs the rendered line through cmd.exe on Windows
// (shellCommand), where a single quote is an ordinary character rather than a
// delimiter: 'a & calc &' still runs calc, and the '%' shellquote leaves bare
// still expands as %VAR%. The shared helper keeps high bytes (non-ASCII) and
// the caret bare so the bytes cmd.exe receives match the pre-consolidation
// rendering, but that is byte-compatibility on the existing fallback path, not
// safety. A Windows caller gets shell-free safety only from an argument vector
// (ExecArgv); RunGit already prefers ExecArgv and reaches this fallback only
// for environments that are not argv-capable.
func ShellEscapeArgs(args ...string) string { return shellquote.Args(args...) }
