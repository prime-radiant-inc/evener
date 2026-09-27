package sshconn

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strconv"
	"strings"

	"primeradiant.com/evener/buildinfo"
	"primeradiant.com/evener/cmd/evener-hub/internal/hostreg"
	"primeradiant.com/evener/execsupport/shellquote"
	"primeradiant.com/evener/internal/remoteinstall"
)

// buildinfoPkg is the import path whose ldflags the controller stamps into the
// deployed binary, so its launch-check version equals this process's
// buildinfo.Version().
const buildinfoPkg = "primeradiant.com/evener/buildinfo"

// deployTempSuffix precedes the mktemp XXXXXX template that names the file the
// push writes before the atomic mv. A fresh name is created atomically on the
// host for every push, so two managers deploying the same host cannot interleave
// writes and mv over one another; the per-host lock alone could not guarantee
// that across processes.
const deployTempSuffix = ".tmp."

// localBuild is the production cross-compile seam. source is the explicit
// evener checkout to build from (Options.BuildSource); it is verified before
// use, so an unset or wrong source is a clear error rather than a build of
// whatever tree is nearby. It runs the same command shape make build-linux uses
// (make/building.mk:42), including `-a` so the embedded files (the frontend dist
// among them) are re-read from disk, and stamps this process's own buildinfo
// values so the deployed binary reports the controller's exact version.
//
// Unlike every runtime build target, it does not inherit build-web as a
// prerequisite (make/building.mk:32,124), so it verifies the embedded SPA was
// actually built before compiling: otherwise the deployed binary would embed the
// tracked placeholder and serve the documented 503, and version auto-match could
// not detect it because it compares only buildinfo.GitSHA.
func localBuild(ctx context.Context, source, goos, goarch, out string) error {
	root, err := verifyBuildSource(source)
	if err != nil {
		return fmt.Errorf("go build %s/%s: %w", goos, goarch, err)
	}
	if err := ensureWebBuilt(root); err != nil {
		return fmt.Errorf("go build %s/%s: %w", goos, goarch, err)
	}
	cmd := exec.CommandContext(ctx, "go", "build", "-a", "-ldflags", buildLdflags(), "-o", out, "./cmd/evener/")
	cmd.Env = append(os.Environ(),
		"CGO_ENABLED=0",
		"GOOS="+goos,
		"GOARCH="+goarch,
	)
	cmd.Dir = root
	outBytes, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("go build %s/%s: %w: %s", goos, goarch, err, tail(outBytes))
	}
	return nil
}

// frontendDist is the directory cmd/evener-hub embeds with go:embed.
const frontendDist = "cmd/evener-hub/frontend/dist"

// distPlaceholder is the only file a failed or never-run `make build-web` leaves
// in the dist directory (frontend/scripts/clean-dist.mjs, vite.config.ts).
const distPlaceholder = "PLACEHOLDER"

// ensureWebBuilt refuses to build when the embedded SPA has no real build
// artifact: the deployed binary would serve the documented 503 instead of the
// web UI, and nothing downstream can detect that. A real `vite build` always
// writes dist/index.html (frontend/vite.config.ts sets outDir "dist" at the app
// root), so requiring it distinguishes a built dist from a directory that merely
// has some other entry in it — .DS_Store or any other stray file used to pass the
// old "anything but PLACEHOLDER" check.
func ensureWebBuilt(root string) error {
	dist := filepath.Join(root, filepath.FromSlash(frontendDist))
	entries, err := os.ReadDir(dist)
	if err != nil {
		return fmt.Errorf("web UI not built: %w (run `make build-web` in %s)", err, root)
	}
	for _, e := range entries {
		if e.Name() == "index.html" && !e.IsDir() {
			return nil
		}
	}
	if len(entries) == 1 && entries[0].Name() == distPlaceholder {
		return fmt.Errorf("web UI not built: %s holds only the placeholder (run `make build-web` in %s)", dist, root)
	}
	return fmt.Errorf("web UI not built: %s has no index.html (run `make build-web` in %s)", dist, root)
}

// buildLdflags renders the -X flags that carry this process's buildinfo into
// the deployed binary. Reading the values in-process (instead of re-running git
// and date as the Makefile does) guarantees the deployed version is exactly the
// controller's buildinfo.Version().
func buildLdflags() string {
	set := func(name, value string) string {
		return "-X " + buildinfoPkg + "." + name + "=" + value
	}
	return strings.Join([]string{
		set("GitSHA", buildinfo.GitSHA),
		set("GitDirty", buildinfo.GitDirty),
		set("BuildTime", buildinfo.BuildTime),
		set("Channel", buildinfo.Channel),
		set("ReleaseTag", buildinfo.ReleaseTag),
	}, " ")
}

// modulePath is this module's path, used to tell the evener checkout apart from
// any other workspace module the hub might be launched inside.
const modulePath = "primeradiant.com/evener"

// verifyBuildSource checks that source is an explicit, real evener checkout and
// returns its absolute path. The source cannot be discovered: an installed hub
// has no reliable way to locate its own source tree, and guessing — walking the
// working directory or runtime.Caller frames — can select a different ancestor
// checkout that declares the same module path. An unset source, or one that is
// not this module with a ./cmd/evener package, is therefore a clear error
// rather than a build of whatever tree happens to be nearby.
func verifyBuildSource(source string) (string, error) {
	if strings.TrimSpace(source) == "" {
		return "", errors.New("no build source configured: set Options.BuildSource to the evener checkout's module root (an installed hub cannot locate its own source)")
	}
	abs, err := filepath.Abs(source)
	if err != nil {
		return "", fmt.Errorf("build source %q: %w", source, err)
	}
	// Canonicalize: on macOS t.TempDir (and /var/folders generally) lives under a
	// symlinked /var, and a caller comparing this against EvalSymlinks would
	// otherwise see two spellings of the same tree.
	abs, err = filepath.EvalSymlinks(abs)
	if err != nil {
		return "", fmt.Errorf("build source %q: %w", source, err)
	}
	if !declaresEvenerModule(abs) {
		return "", fmt.Errorf("build source %q is not the evener checkout: no go.mod declaring %q", abs, modulePath)
	}
	info, err := os.Stat(filepath.Join(abs, "cmd", "evener"))
	if err != nil {
		return "", fmt.Errorf("build source %q has no cmd/evener package: %w", abs, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("build source %q has a non-directory cmd/evener", abs)
	}
	if err := verifyBuildRevision(abs); err != nil {
		return "", err
	}
	return abs, nil
}

// ValidateBuildSource is the exported entry point an embedder uses to check a
// build-source value at startup — where its flag is read — instead of deferring
// a bad value to the first attach. It is verifyBuildSource: the same refusals
// (not an evener checkout, a dirty tree, an ignored compiled .go file), and it
// returns the canonical absolute path to store in Options.BuildSource.
func ValidateBuildSource(source string) (string, error) {
	return verifyBuildSource(source)
}

// verifyBuildRevision makes the deployed binary's version stamp honest. The
// builder stamps this process's own buildinfo into whatever the source checkout
// compiles, so a checkout at a different revision would install code that reports
// the controller's version while running something else — version auto-match
// would then accept it. When this process carries a GitSHA, the source checkout's
// HEAD must resolve to that same commit; otherwise the build is refused. An
// unstamped (dev) controller has no revision to match and is skipped.
func verifyBuildRevision(root string) error {
	want := strings.TrimSpace(buildinfo.GitSHA)
	if want == "" {
		return nil
	}
	// A dirty controller cannot be reproduced from a checkout. The builder stamps
	// this process's GitDirty into the binary, so the deployed build would report
	// exactly the controller's version ("<sha>-dirty") while the source tree it
	// compiled may carry a different set of uncommitted changes. HEAD equality
	// below cannot see that difference, so refuse rather than install code that
	// version auto-match cannot verify.
	if strings.TrimSpace(buildinfo.GitDirty) == "true" {
		return fmt.Errorf("this controller was built from a dirty tree (version %q); the build source %q cannot be verified to match it, so refusing to deploy", buildinfo.Version(), root)
	}
	head, err := gitCommit(root, "HEAD")
	if err != nil {
		return fmt.Errorf("build source %q: cannot read HEAD to check it against this controller's build %q: %w", root, want, err)
	}
	got, err := gitCommit(root, want)
	if err != nil {
		return fmt.Errorf("build source %q does not contain this controller's build commit %q: %w", root, want, err)
	}
	if got != head {
		return fmt.Errorf("build source %q is at commit %s, but this controller was built from %s; refusing to deploy code other than the controller's own", root, head, want)
	}
	// HEAD equality does not pin the code: the builder compiles the working tree,
	// so a modified or untracked file at that same commit produces a different
	// binary that still reports this controller's clean version, and version
	// auto-match would accept it. Require a clean worktree.
	status, err := gitStatus(root)
	if err != nil {
		return fmt.Errorf("build source %q: cannot read its working-tree status to check it against this controller's build %q: %w", root, want, err)
	}
	if changes := strings.TrimSpace(status); changes != "" {
		return fmt.Errorf("build source %q has uncommitted changes (%s); refusing to deploy code other than the controller's own", root, firstLine(changes))
	}
	// gitStatus's porcelain output omits ignored untracked files, so an ignored
	// .go source file would slip past it and still be compiled into the build.
	// Check those separately, scoped to Go sources so the ignored (and required)
	// embedded frontend dist does not count as a change.
	ignored, err := ignoredGoFiles(root)
	if err != nil {
		return fmt.Errorf("build source %q: cannot read its ignored files to check it against this controller's build %q: %w", root, want, err)
	}
	if files := strings.TrimSpace(ignored); files != "" {
		return fmt.Errorf("build source %q has ignored untracked Go files (%s); refusing to deploy code other than the controller's own", root, firstLine(files))
	}
	return nil
}

// gitCommit resolves rev to a full commit id within the checkout at root. A rev
// the checkout does not contain (or a missing git) is an error, so an unverifiable
// source fails closed rather than being assumed to match.
func gitCommit(root, rev string) (string, error) {
	out, err := exec.Command("git", "-C", root, "rev-parse", "--verify", "--quiet", rev+"^{commit}").Output() //nolint:noctx // local, fast; no request context to thread here
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// gitStatus returns `git status --porcelain` for the checkout at root. Porcelain
// output is stable and machine-readable: one line per staged, modified, or
// untracked path, empty for a clean tree. Untracked files count because they are
// part of what `go build` compiles.
func gitStatus(root string) (string, error) {
	out, err := exec.Command("git", "-C", root, "status", "--porcelain").Output() //nolint:noctx // local, fast; no request context to thread here
	if err != nil {
		return "", err
	}
	return string(out), nil
}

// ignoredGoFiles lists the ignored untracked .go files in the checkout at root,
// one per line, empty when there are none. `git status --porcelain` omits ignored
// paths, so without this an ignored .go source file would be compiled by
// `go build` while the deployed binary kept the repository's unchanged (clean)
// SHA — defeating the source/version identity the revision check exists to
// provide. Only .go files are listed: the build's embedded frontend dist is
// itself ignored (and required), so a blanket ignored-file check would refuse
// every real build.
func ignoredGoFiles(root string) (string, error) {
	out, err := exec.Command("git", "-C", root, "ls-files", "--others", "--ignored", "--exclude-standard", "--", "*.go").Output() //nolint:noctx // local, fast; no request context to thread here
	if err != nil {
		return "", err
	}
	return string(out), nil
}

// declaresEvenerModule reports whether dir holds a go.mod for this module.
func declaresEvenerModule(dir string) bool {
	data, err := os.ReadFile(filepath.Join(dir, "go.mod"))
	if err != nil {
		return false
	}
	for line := range strings.SplitSeq(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 2 && fields[0] == "module" && fields[1] == modulePath {
			return true
		}
	}
	return false
}

// errControllerDirty marks a deploy refused because this controller was built
// from a dirty tree. Such a build has no reproducible identity: the push path
// refuses to compile a tree it cannot prove matches this process
// (verifyBuildRevision), the installer fallback has no published artifact to pin
// (installerRefFor), and version auto-match cannot prove a host reporting the
// same "<sha>-dirty" names the same code. The refusal is terminal rather than
// ErrDeploy: the same install can never succeed by being retried, and a
// supervisor that retried it cross-compiled forever while the host was never
// attached (round thirteen).
var errControllerDirty = errors.New("sshconn: controller build is a dirty tree")

// errDeployUnstamped marks a deploy that left the host reporting a build other
// than the controller's. Unlike the dirty-tree refusal above, the controller HAD a
// deploy path and used it; the freshly re-read launch contract still disagrees, so
// the artifact that reached the host was not stamped by this controller — the one
// case the pre-push check cannot catch, an operator-supplied -deploy-binary built
// from a different tree that targets the right platform but carries another
// build's identity. The refusal is terminal rather than ErrDeploy: retrying
// re-pushes the same artifact, so the supervisor would loop forever while the host
// was never attached (the same mistake round thirteen records for the
// dirty-controller refusal). Attaching instead would serve a build version
// auto-match exists to prevent.
//
// It is judged only where a deploy ran. A pass that merely started or restarted
// the hub launched the build already on disk, and the host is allowed to keep that
// build: the protocol, not the build label, decides whether it may attach.
var errDeployUnstamped = errors.New("sshconn: deployed build is not stamped by this controller")

// deploy installs a matching build on the host. The cross-compile + push path is
// primary when a build source is configured; otherwise the installer fallback
// runs on the host. It returns the resolved run target the manager must record as
// host.EvenerPath: the canonical path the push installed to, or the installer's
// run target. Both are non-empty on success, so the restart, health re-probe, and
// attach paths address the binary that was actually installed instead of a bare
// `evener` a fresh host's non-interactive PATH may not carry.
func (m *Manager) deploy(ctx context.Context, host hostreg.Host, facts Preflight) (string, error) {
	// A dirty controller can neither install its own build nor verify a host's.
	// deployRequired forces this deploy for an unverifiable version (a dirty
	// "<sha>-dirty" is not an identity: another dirty checkout at the same commit
	// reports it too), and attaching instead of deploying would silently serve a
	// build version auto-match cannot verify. Refuse terminally, naming the
	// cause: retrying is what turned this refusal into an endless cross-compile,
	// because every attempt failed as a retryable ErrDeploy and markDevDeployed
	// was never reached.
	if version := m.opts.controllerVersion(); isDirtyVersion(version) {
		return "", fmt.Errorf("%w: host %q: this controller was built from a dirty tree (version %q), so it cannot install its own build (a dirty tree cannot be reproduced from a checkout, and the installer fallback has no published artifact to pin) or prove that a host's build matches it; rebuild the controller from a clean checkout",
			errControllerDirty, host.Name, version)
	}
	if m.canBuild() {
		return m.deployPush(ctx, host, facts)
	}
	return m.deployInstaller(ctx, host, facts)
}

// deployPush cross-compiles this tree for the host target and atomically
// installs it at the host's evener path. The existing binary is untouched unless
// the push fully succeeds. It returns the canonical install target so the caller
// can record it as host.EvenerPath even when the registry did not configure one:
// on a fresh host that target is the installer default ~/.local/bin/evener, and
// discarding it left the manager probing the literal `evener`, which the
// non-interactive PATH cannot resolve.
func (m *Manager) deployPush(ctx context.Context, host hostreg.Host, facts Preflight) (string, error) {
	target, err := m.deployTarget(ctx, host, facts)
	if err != nil {
		return "", err
	}

	stageDir, err := os.MkdirTemp("", "evener-deploy-")
	if err != nil {
		return "", fmt.Errorf("%w: host %q staging dir: %w", ErrDeploy, host.Name, err)
	}
	defer func() { _ = os.RemoveAll(stageDir) }()
	stage := filepath.Join(stageDir, "evener")

	build := m.opts.BuildBinary
	if build == nil {
		source := m.opts.BuildSource
		build = func(ctx context.Context, goos, goarch, out string) error {
			return localBuild(ctx, source, goos, goarch, out)
		}
	}
	if err := build(ctx, facts.OS, facts.Arch, stage); err != nil {
		// A terminal refusal from the build seam — an operator-supplied
		// -deploy-binary built for another platform (errDeployArtifactUnusable) —
		// names a permanent cause that retrying re-reads unchanged. Keep it out of
		// the retryable ErrDeploy class so the sentinel's own classification, not
		// ErrDeploy, is what the supervisor and the wire mapping see.
		if isTerminal(err) {
			return "", fmt.Errorf("host %q build %s/%s: %w", host.Name, facts.OS, facts.Arch, err)
		}
		return "", fmt.Errorf("%w: host %q build %s/%s: %w", ErrDeploy, host.Name, facts.OS, facts.Arch, err)
	}

	f, err := os.Open(stage)
	if err != nil {
		return "", fmt.Errorf("%w: host %q open staged binary: %w", ErrDeploy, host.Name, err)
	}
	defer func() { _ = f.Close() }()

	info, err := f.Stat()
	if err != nil {
		return "", fmt.Errorf("%w: host %q stat staged binary: %w", ErrDeploy, host.Name, err)
	}
	if err := m.pushBinary(ctx, host, target, f, info.Size()); err != nil {
		return "", err
	}
	return target, nil
}

// errRunTargetUnservable marks a deploy refused because the configured run
// target is not the `evener` binary a host hub can serve — `evener-dev` (the
// development tooling binary: no `hub` subcommand, no `launch-check`) or any
// other basename. The refusal is terminal rather than ErrDeploy: a misconfigured
// run target is an operator configuration defect the host cannot recover from,
// so retrying can never install a binary the hub can run — the supervisor would
// re-refuse the same path forever and discard the cause, the basename to fix
// (the identical mistake round thirteen records for the dirty-controller
// refusal).
var errRunTargetUnservable = errors.New("sshconn: run target cannot serve a hub")

// errDeployArtifactUnusable marks a deploy refused because the operator-supplied
// artifact (-deploy-binary) cannot serve the host: it was built for a different
// GOOS/GOARCH, or it is a Go program that is not evener (the hub's
// copyDeployBinary refuses both before anything is staged). The operator supplies
// the artifact, so either defect is a permanent mistake — the same file is
// re-read on every retry — and the refusal is terminal for the same reason
// errRunTargetUnservable is: the supervisor would re-refuse it forever. deployPush
// also keeps this sentinel out of the retryable ErrDeploy wrap, so terminal is the
// class that reaches both the reconnect loop and the hub's attach handler.
var errDeployArtifactUnusable = errors.New("sshconn: deploy artifact cannot serve the host")

// checkRunTarget refuses a configured evener_path that cannot be the host hub's
// run target. Only the shipped `evener` binary can serve a hub: release archives
// also carry `evener-dev`, but that is the development/test tooling
// binary — no `hub` subcommand and no `launch-check` — so a host configured to
// run it installs "successfully" and then fails preflight, health, and restart
// on a binary that can never serve the hub. Any other basename is no better: the
// manager records this one path as the host's run target and probes, restarts,
// and attaches the binary at it.
//
// It is checked before the target is probed, pushed, or installed, and it is
// terminal (errRunTargetUnservable) with no write. The ordering and the sentinel
// are both the point: the refusal names a configuration defect the host cannot
// recover from, so a retry could only re-refuse it forever, and discovering it
// after the write would only leave the host holding a binary the controller can
// never run. The missing-directory refusal beside it stays a retryable ErrDeploy
// because a directory can appear.
func checkRunTarget(hostName, p string) error {
	if installableEvenerBasename(p) {
		return nil
	}
	base := path.Base(strings.TrimSpace(p))
	if base == "evener-dev" {
		return fmt.Errorf("%w: host %q evener_path %q names %q, the development tooling binary (cmd/evener-dev), which does not provide the hub command and has no launch-check, so it can never serve a hub; configure the evener binary", errRunTargetUnservable, hostName, p, base)
	}
	return fmt.Errorf("%w: host %q evener_path %q has basename %q, which is not the evener binary that serves a hub; configure an evener-named run target", errRunTargetUnservable, hostName, p, base)
}

// deployTarget resolves the absolute remote path the binary is installed to:
// the registry's evener_path when set (whose directory must already exist),
// otherwise the executable the running hub was launched from (when its basename
// is a run target a hub can serve, checkRunTarget), else whatever `evener`
// resolves to on the remote PATH, else the installer's default
// ~/.local/bin/evener. A not-yet-installed file is a creatable target, so a push
// deploy can provision a fresh host.
//
// The running hub's own executable comes first because a push to any other file
// leaves the upgrade inert: the restart path proves the recovered hub executable
// is the target it is about to relaunch (hubExecutableMatches), refuses to
// restart a hub running a different binary, and the on-disk target already
// matches — so the deploy/restart pair loops forever. deployInstaller preserves
// the same location (existingInstallableEvener); this is the push path's half.
func (m *Manager) deployTarget(ctx context.Context, host hostreg.Host, facts Preflight) (string, error) {
	if p := strings.TrimSpace(host.EvenerPath); p != "" {
		// Before even the directory probe: a run target that cannot serve a hub
		// is refused with no remote command at all (checkRunTarget).
		if err := checkRunTarget(host.Name, p); err != nil {
			return "", err
		}
		dir := path.Dir(p)
		out, err := m.runner.Run(ctx, rawCommandArgv(m.opts, host, "test -d "+shellquote.RemoteWord(dir)), nil)
		if err != nil {
			return "", fmt.Errorf("%w: host %q evener_path directory %q does not exist: %w: %s", ErrDeploy, host.Name, dir, err, tail(out))
		}
		// A push deploy must be able to provision a fresh host: an evener_path
		// whose file has not been installed yet is a creatable target, not an
		// error. The resolver still follows an existing symlink and still refuses
		// a directory, but canonicalizes a not-yet-existent file from its
		// (existing) parent directory.
		return m.resolveDeployOrCreateTarget(ctx, host, p)
	}

	if p := m.currentHubExecutableName(ctx, host); installableEvenerBasename(p) {
		return m.resolveDeployOrCreateTarget(ctx, host, p)
	}

	p, err := m.evenerOnPath(ctx, host)
	if err != nil {
		return "", err
	}
	if p == "" {
		// No running hub and no evener on the non-interactive PATH either. Fall
		// back to the installer's own default location and create its directory,
		// so a push deploy can still bring up a host that has no evener installed
		// at all.
		home := strings.TrimSpace(facts.Home)
		if home == "" {
			return "", fmt.Errorf("%w: host %q has no evener_path, no evener on PATH, and preflight found no HOME to resolve the installer default ~/.local/bin/evener; set evener_path", ErrDeploy, host.Name)
		}
		p = path.Join(home, ".local", "bin", "evener")
		dir := path.Dir(p)
		if out, err := m.runner.Run(ctx, rawCommandArgv(m.opts, host, "mkdir -p "+shellquote.RemoteWord(dir)), nil); err != nil {
			return "", fmt.Errorf("%w: host %q create install directory %q: %w: %s", ErrDeploy, host.Name, dir, err, tail(out))
		}
	}
	return m.resolveDeployOrCreateTarget(ctx, host, p)
}

// evenerPathMissingMarker distinguishes "no evener on PATH" from a transport
// failure. `command -v` fails with no output when the name is missing, which is
// otherwise indistinguishable from a failed ssh command.
const evenerPathMissingMarker = "__sshconn_no_evener__"

// evenerOnPath returns the path `command -v evener` resolves to on the host's
// non-interactive PATH, or "" when the host has no evener there. A transport
// failure is surfaced rather than read as an absent binary.
func (m *Manager) evenerOnPath(ctx context.Context, host hostreg.Host) (string, error) {
	out, err := m.runner.Run(ctx, rawCommandArgv(m.opts, host, "command -v evener || echo "+evenerPathMissingMarker), nil)
	if err != nil {
		return "", fmt.Errorf("%w: host %q resolve evener on PATH: %w: %s", ErrDeploy, host.Name, err, tail(out))
	}
	p := firstLine(string(out))
	if p == "" || p == evenerPathMissingMarker {
		return "", nil
	}
	return p, nil
}

// probeInstallerDefaultExecutable reports the host's installer-default evener
// path (~/.local/bin/evener) when a regular file exists there, and ok=false when
// it does not. deployTarget falls back to that location to provision a fresh
// host; preflight consults it too and records what it finds as the host's
// resolved target, which expectedHubExecutable reads, so a host whose binary
// already lives there but is absent from the non-interactive PATH is recognized
// as running the controller's build rather than re-deployed to on every
// reconnect. A probe failure is read as absent: this is best-effort discovery,
// and the deploy path re-resolves (surfacing a real transport error) when
// nothing is found.
func (m *Manager) probeInstallerDefaultExecutable(ctx context.Context, host hostreg.Host) (string, bool) {
	remote := `if [ -n "${HOME-}" ] && [ -f "${HOME-}/.local/bin/evener" ]; then printf '%s\n' "${HOME-}/.local/bin/evener"; fi`
	out, err := m.runner.Run(ctx, rawCommandArgv(m.opts, host, remote), nil)
	if err != nil {
		return "", false
	}
	p := firstLine(string(out))
	return p, p != ""
}

// resolvePathScript is a POSIX-sh path resolver that prints the real path a file
// names, following symlinks. It deliberately avoids `readlink -f`: that flag is a
// GNU/coreutils extension which BSD `readlink` (macOS) rejects with `readlink:
// illegal option -- f`, so every darwin/arm64 deploy failed at path resolution.
// It prints nothing and exits nonzero when the path does not resolve to an
// existing regular file, which the caller reads as a clear error rather than a
// silent fallback to the symlink. A `-f` (not `-e`) test is what makes a
// directory an error: `mv` would happily move the temporary binary *inside* the
// directory and report success, leaving the real executable un-upgraded. The
// traversal is bounded so a symlink cycle returns nonzero like ELOOP instead of
// looping until the ssh command times out.
const resolvePathScript = `evener_resolve() {
  p=$1
  n=0
  while [ -L "$p" ]; do
    n=$((n+1))
    [ "$n" -le 40 ] || return 1
    d=$(cd -P "$(dirname "$p")" 2>/dev/null && pwd) || return 1
    t=$(readlink "$p") || return 1
    case $t in
      /*) p=$t ;;
      *) p=$d/$t ;;
    esac
  done
  [ -f "$p" ] || return 1
  d=$(cd -P "$(dirname "$p")" 2>/dev/null && pwd) || return 1
  printf '%s\n' "$d/${p##*/}"
}
evener_resolve `

// resolveDeployCommand builds the remote command that prints the real path p
// names.
func resolveDeployCommand(p string) string {
	return resolvePathScript + shellquote.RemoteWord(p)
}

// resolveDeployTarget resolves p to the real file it names, so the atomic mv
// replaces the installed executable rather than clobbering a symlink. `make
// install` commonly installs evener as a symlink and `command -v evener` returns
// that symlink; mv-ing onto it would replace the link and break the installed
// layout (and later upgrades). The resolver prints nothing for a path that does
// not exist, which is a clear error rather than a silent fallback to the
// symlink.
func (m *Manager) resolveDeployTarget(ctx context.Context, host hostreg.Host, p string) (string, error) {
	out, err := m.runner.Run(ctx, rawCommandArgv(m.opts, host, resolveDeployCommand(p)), nil)
	if err != nil {
		return "", fmt.Errorf("%w: host %q resolve install path %q: %w: %s", ErrDeploy, host.Name, p, err, tail(out))
	}
	resolved := firstLine(string(out))
	if resolved == "" {
		return "", fmt.Errorf("%w: host %q install path %q does not resolve to a real file", ErrDeploy, host.Name, p)
	}
	return resolved, nil
}

// resolveDeployOrCreateTarget resolves p to the real file it names, or, when p
// does not exist yet, to the canonical path at which a push will create it.
// This is what lets a push deploy provision a fresh host whose evener_path file
// has not been installed yet. An existing directory is still refused: the
// creatable fallback only fires for a path that does not exist at all, so the
// atomic mv can never move the staged binary inside a directory and leave the
// real executable un-upgraded.
func (m *Manager) resolveDeployOrCreateTarget(ctx context.Context, host hostreg.Host, p string) (string, error) {
	resolved, err := m.resolveDeployTarget(ctx, host, p)
	if err == nil {
		return resolved, nil
	}
	out, cerr := m.runner.Run(ctx, rawCommandArgv(m.opts, host, createTargetCommand(p)), nil)
	if cerr != nil {
		// Surface the create attempt's own failure. Returning the resolve error
		// alone reads as "does not resolve to a real file" and hides the
		// actionable cause (a missing parent directory, a permission refusal),
		// so the operator sees no diagnostic for the create that actually
		// failed.
		return "", fmt.Errorf("%w: host %q create install target %q: %w: %s", ErrDeploy, host.Name, p, cerr, tail(out))
	}
	created := firstLine(string(out))
	if created == "" {
		return "", fmt.Errorf("%w: host %q install target %q cannot be created (the parent directory may not exist): %w", ErrDeploy, host.Name, p, err)
	}
	return created, nil
}

// createTargetCommand canonicalizes the path a not-yet-installed evener will be
// created at. It refuses an existing path (a directory, a dangling symlink, or a
// file the resolver could not handle) and a path whose parent directory does not
// exist, so it only ever enables creating a genuinely new file under an existing
// directory.
func createTargetCommand(p string) string {
	q := shellquote.RemoteWord(p)
	return "p=" + q + "; if [ -e \"$p\" ] || [ -L \"$p\" ]; then exit 1; fi; " +
		"d=$(cd -P \"$(dirname \"$p\")\" 2>/dev/null && pwd) || exit 1; " +
		"printf '%s\\n' \"$d/${p##*/}\""
}

// pushBinaryRemote builds the remote command that installs a pushed binary: it
// creates a unique temp name with mktemp (atomic, and unique across manager
// processes), arms a trap to remove it, streams the binary into it, verifies the
// streamed byte count, marks it executable, and mv's it into place. The
// temp-then-mv makes the install atomic: an interrupted push never leaves a
// truncated evener at the final path, and the trap removes the temp file on any
// failure (a failed cat, a byte-count mismatch, chmod, or mv, or a dropped
// connection) so repeated failures do not accumulate evener.tmp.* files in the
// install directory.
//
// The byte-count check is what makes a truncated transfer a failure rather than
// silent corruption: ssh reports a dropped stream as a successful EOF, so `cat`
// exits 0 on a partial transfer and the mv would otherwise install a truncated
// binary over a working one. size is the staged file's length, compared against
// the remote temp file before the rename.
func pushBinaryRemote(target string, size int64) string {
	tmp := "tmp=$(mktemp " + shellquote.RemoteWord(target+deployTempSuffix+"XXXXXX") + ") || exit 1"
	cleanup := "trap 'rm -f \"$tmp\"' EXIT"
	verify := "v=$(wc -c < \"$tmp\" | tr -d '[:space:]') && [ \"$v\" = " + strconv.FormatInt(size, 10) + " ]"
	return tmp + "; " + cleanup + "; cat > \"$tmp\" && " + verify + " && chmod +x \"$tmp\" && mv \"$tmp\" " + shellquote.RemoteWord(target)
}

// pushBinary streams data to target over an ssh `cat`, verifies the streamed
// byte count matches size, then chmod +x and mv it into place. Feeding the
// binary on the Runner.Run stdin keeps argv[0] == "ssh".
func (m *Manager) pushBinary(ctx context.Context, host hostreg.Host, target string, data io.Reader, size int64) error {
	remote := pushBinaryRemote(target, size)
	out, err := m.runner.Run(ctx, rawCommandArgv(m.opts, host, remote), data)
	if err != nil {
		return fmt.Errorf("%w: host %q push %s: %w: %s", ErrDeploy, host.Name, target, err, tail(out))
	}
	return nil
}

// installerRefFor maps this controller's build channel to the artifact
// reference install.sh accepts as EVENER_INSTALL_VERSION. install.sh treats that
// variable as a GitHub *release tag* (`$repo/releases/download/$version`), while
// buildinfo.Version() is a short Git SHA, possibly "-dirty" — passing it
// verbatim would point every normal build at a release that does not exist. The
// mapping is therefore:
//
//   - release: the stamped ReleaseTag (a Git SHA is never a substitute);
//   - snapshot: the mutable `snapshot` tag. It is not pinned by construction: the
//     post-install version check accepts it only while the tag still points at
//     this controller's commit. Once the tag moves past it, the check refuses
//     terminally (ErrVersionMismatch) rather than re-fetching the same artifact
//     forever;
//   - dev/dirty: refused with ErrDeploy — there is no publishable identity to
//     pin, so the refusal appends the remedy clause.
//
// remedy is the remedy clause those refusals append. The caller resolves it from
// Options.DeployHelp, so a hub tells the operator which flags to set while an
// embedder with no flags to name keeps the library's own sentence
// (Manager.installerRemedy). It must be non-empty.
//
// `latest` is never passed.
func installerRefFor(channel, releaseTag, dirty, remedy string) (string, error) {
	if strings.TrimSpace(dirty) == "true" {
		return "", fmt.Errorf("%w: this controller was built from a dirty tree, so the installer fallback has no published artifact to pin; %s", ErrDeploy, remedy)
	}
	switch channel {
	case "release":
		tag := strings.TrimSpace(releaseTag)
		if tag == "" {
			return "", fmt.Errorf("%w: this controller is a release build but carries no stamped release tag (buildinfo.ReleaseTag); refusing to pass the Git SHA %q as a release tag; %s", ErrDeploy, buildinfo.Version(), remedy)
		}
		return tag, nil
	case "snapshot":
		return "snapshot", nil
	default:
		return "", fmt.Errorf("%w: this controller's build channel %q has no publishable artifact to pin (buildinfo.Version() %q is a Git SHA, not a release tag); %s", ErrDeploy, channel, buildinfo.Version(), remedy)
	}
}

// installerDirs resolves the BINDIR / EVENER_SHARE_BINDIR the installer must be
// given so it installs to the target the manager will actually run, plus that
// run target. A host with a custom evener_path otherwise installs cleanly into
// ~/.local/bin and then keeps probing and relaunching the old binary at the
// configured path. When evener_path is empty the installer's own default
// `~/.local/bin/evener` IS the run target, returned so the manager records and
// uses it rather than assuming a separate `command -v evener` result.
//
// The default case passes those two directories explicitly instead of leaving
// the installer to compute them from HOME. install.sh honors inherited PREFIX,
// BINDIR, and EVENER_SHARE_BINDIR (install.sh:7-18), and non-interactive ssh
// still carries whatever the server's environment or the ssh user's login setup
// exports; an inherited PREFIX (say /usr/local) would install the binary
// somewhere the manager never probes, records, or relaunches, while the deploy
// reported success. BINDIR and EVENER_SHARE_BINDIR fully determine where
// install.sh writes (PREFIX is only their fallback), so passing the resolved
// defaults pins the layout to the run target this function returns. A configured
// evener_path whose basename is not `evener` is refused here too (checkRunTarget):
// the installer installs the runtime binary as `evener`, so a path naming
// anything else is a run target the installer cannot produce a hub for.
func installerDirs(host hostreg.Host, facts Preflight) (bindir, shareBindir, runTarget string, err error) {
	p := strings.TrimSpace(host.EvenerPath)
	if p == "" {
		home := strings.TrimSpace(facts.Home)
		if home == "" {
			return "", "", "", fmt.Errorf("%w: host %q has no evener_path and preflight found no HOME to resolve the installer's default ~/.local/bin/evener; set evener_path", ErrDeploy, host.Name)
		}
		bindir = path.Join(home, ".local", "bin")
		shareBindir = path.Join(home, ".local", "share", "evener", "bin")
		return bindir, shareBindir, path.Join(bindir, "evener"), nil
	}
	if err := checkRunTarget(host.Name, p); err != nil {
		return "", "", "", err
	}
	bindir = path.Dir(p)
	shareBindir = path.Join(path.Dir(bindir), "share", "evener", "bin")
	return bindir, shareBindir, p, nil
}

// deployInstaller is the fallback deploy path for a controller with no build
// source: it streams the embedded installer to the host pinned to the
// controller's own build channel, then verifies the installed binary is the
// expected build before any restart or attach. A tag that resolves to a wrong or
// moved artifact is a failed verification (a terminal ErrVersionMismatch), never
// a retry of the same pinned ref and never an attach.
func (m *Manager) deployInstaller(ctx context.Context, host hostreg.Host, facts Preflight) (string, error) {
	ref, err := installerRefFor(buildinfo.BuildChannel(), buildinfo.ReleaseTag, buildinfo.GitDirty, m.installerRemedy())
	if err != nil {
		return "", err
	}
	// With no configured evener_path, preserve the install location the host
	// already uses: install the controller's build over the running hub's own
	// executable (or over whatever `evener` resolves to on PATH) so the relaunch
	// argv and any supervisor unit keep pointing at the upgraded binary. Deploying
	// to an unrelated default instead makes restart identity validation refuse the
	// hub it was meant to replace, while a supervisor keeps restarting the old
	// executable — the upgrade never lands. installerDirs turns the preserved path
	// into BINDIR so install.sh writes exactly there.
	if strings.TrimSpace(host.EvenerPath) == "" {
		if existing := m.existingInstallableEvener(ctx, host); existing != "" {
			host.EvenerPath = existing
		}
	}
	bindir, shareBindir, runTarget, err := installerDirs(host, facts)
	if err != nil {
		return "", err
	}
	out, err := m.runner.Run(ctx, rawCommandArgv(m.opts, host, remoteinstall.Command(ref, "", bindir, shareBindir)), bytes.NewReader(remoteinstall.Script))
	if err != nil {
		return "", fmt.Errorf("%w: host %q installer (%s): %w: %s", ErrDeploy, host.Name, ref, err, tail(out))
	}
	// Verify the binary the installer left at the run target. With an empty
	// evener_path the launch-check must address the installed file directly: the
	// non-interactive PATH may not carry ~/.local/bin, so the literal `evener`
	// could resolve to nothing.
	probeHost := host
	if runTarget != "" {
		probeHost.EvenerPath = runTarget
	}
	lc, err := m.probeLaunchCheck(ctx, probeHost)
	if err != nil {
		return "", fmt.Errorf("%w: host %q installer verification: %w", ErrDeploy, host.Name, err)
	}
	// The installer ran, so the fetched artifact is a definitive answer rather
	// than a transient deploy failure: an installed version other than the
	// controller's means the pinned ref has moved past this controller's commit
	// (for a snapshot build, the mutable `snapshot` tag). Refuse terminally as a
	// version mismatch — ErrDeploy would be retried forever by the supervisor,
	// re-downloading and re-rejecting the same unmatchable artifact. The remedy
	// clause names the path that does carry a provable identity, so the operator
	// is told how to stop depending on the moved tag.
	if want := m.opts.controllerVersion(); lc.Version != want {
		return "", fmt.Errorf("%w: host %q installer installed version %q, want %q (the pinned artifact %q does not match the controller's build; a moved channel tag cannot be resolved by retrying); %s", ErrVersionMismatch, host.Name, lc.Version, want, ref, m.installerRemedy())
	}
	return runTarget, nil
}

// existingInstallableEvener resolves the user-facing install path of the evener
// the host already has — the running hub's own executable first, then whatever
// `evener` resolves to on the remote PATH — or "" when none can be identified or
// the found binary's basename is not one a hub can be run as
// (installableEvenerBasename). It is how a deploy with no configured evener_path
// keeps the existing install location instead of writing to an unrelated default.
//
// It deliberately returns the path the installation names (argv[0] / the
// `command -v` result), not the canonical file a symlink points at. install.sh
// installs `bin/evener` as a symlink to `share/evener/bin/evener`, so feeding the
// canonical target into installerDirs derived BINDIR from the real binary's
// directory and produced nested paths like `share/evener/bin/share/evener/bin`
// on every upgrade. Canonical resolution is used elsewhere (hubExecutableMatches)
// for identity; here the layout must be preserved.
func (m *Manager) existingInstallableEvener(ctx context.Context, host hostreg.Host) string {
	if exe := m.currentHubExecutableName(ctx, host); installableEvenerBasename(exe) {
		return exe
	}
	p, err := m.evenerOnPath(ctx, host)
	if err != nil || p == "" || !installableEvenerBasename(p) {
		return ""
	}
	return p
}
