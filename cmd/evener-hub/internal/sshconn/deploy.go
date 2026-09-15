package sshconn

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"

	"primeradiant.com/evener/buildinfo"
	"primeradiant.com/evener/cmd/evener-hub/internal/hostreg"
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

// deploy installs a matching build on the host. The cross-compile + push path is
// primary when a build source is configured; otherwise the installer fallback
// runs on the host. It returns the run target the manager must use afterwards,
// which is non-empty only for the installer fallback with an empty evener_path
// (the installer's own default location, so the restart and attach paths address
// the binary that was actually installed).
func (m *Manager) deploy(ctx context.Context, host hostreg.Host, facts Preflight) (string, error) {
	if m.canBuild() {
		return "", m.deployPush(ctx, host, facts)
	}
	return m.deployInstaller(ctx, host, facts)
}

// deployPush cross-compiles this tree for the host target and atomically
// installs it at the host's evener path. The existing binary is untouched unless
// the push fully succeeds.
func (m *Manager) deployPush(ctx context.Context, host hostreg.Host, facts Preflight) error {
	target, err := m.deployTarget(ctx, host, facts)
	if err != nil {
		return err
	}

	stageDir, err := os.MkdirTemp("", "evener-deploy-")
	if err != nil {
		return fmt.Errorf("%w: host %q staging dir: %w", ErrDeploy, host.Name, err)
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
		return fmt.Errorf("%w: host %q build %s/%s: %w", ErrDeploy, host.Name, facts.OS, facts.Arch, err)
	}

	f, err := os.Open(stage)
	if err != nil {
		return fmt.Errorf("%w: host %q open staged binary: %w", ErrDeploy, host.Name, err)
	}
	defer func() { _ = f.Close() }()

	return m.pushBinary(ctx, host, target, f)
}

// deployTarget resolves the absolute remote path the binary is installed to:
// the registry's evener_path when set (whose directory must already exist),
// otherwise whatever `evener` resolves to on the remote PATH, else the
// installer's default ~/.local/bin/evener. A not-yet-installed file is a
// creatable target, so a push deploy can provision a fresh host.
func (m *Manager) deployTarget(ctx context.Context, host hostreg.Host, facts Preflight) (string, error) {
	if p := strings.TrimSpace(host.EvenerPath); p != "" {
		dir := path.Dir(p)
		out, err := m.runner.Run(ctx, rawCommandArgv(m.opts, host, "test -d "+shellQuote(dir)), nil)
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

	p, err := m.evenerOnPath(ctx, host)
	if err != nil {
		return "", err
	}
	if p == "" {
		// No evener on the non-interactive PATH either. Fall back to the
		// installer's own default location and create its directory, so a push
		// deploy can still bring up a host that has no evener installed at all.
		home := strings.TrimSpace(facts.Home)
		if home == "" {
			return "", fmt.Errorf("%w: host %q has no evener_path, no evener on PATH, and preflight found no HOME to resolve the installer default ~/.local/bin/evener; set evener_path", ErrDeploy, host.Name)
		}
		p = path.Join(home, ".local", "bin", "evener")
		dir := path.Dir(p)
		if out, err := m.runner.Run(ctx, rawCommandArgv(m.opts, host, "mkdir -p "+shellQuote(dir)), nil); err != nil {
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
	return resolvePathScript + shellQuote(p)
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
		return "", err
	}
	created := firstLine(string(out))
	if created == "" {
		return "", err
	}
	return created, nil
}

// createTargetCommand canonicalizes the path a not-yet-installed evener will be
// created at. It refuses an existing path (a directory, a dangling symlink, or a
// file the resolver could not handle) and a path whose parent directory does not
// exist, so it only ever enables creating a genuinely new file under an existing
// directory.
func createTargetCommand(p string) string {
	q := shellQuote(p)
	return "p=" + q + "; if [ -e \"$p\" ] || [ -L \"$p\" ]; then exit 1; fi; " +
		"d=$(cd -P \"$(dirname \"$p\")\" 2>/dev/null && pwd) || exit 1; " +
		"printf '%s\\n' \"$d/${p##*/}\""
}

// pushBinaryRemote builds the remote command that installs a pushed binary: it
// creates a unique temp name with mktemp (atomic, and unique across manager
// processes), arms a trap to remove it, streams the binary into it, marks it
// executable, and mv's it into place. The temp-then-mv makes the install atomic:
// an interrupted push never leaves a truncated evener at the final path, and the
// trap removes the temp file on any failure (a failed cat, chmod, or mv, or a
// dropped connection) so repeated failures do not accumulate evener.tmp.* files
// in the install directory.
func pushBinaryRemote(target string) string {
	tmp := "tmp=$(mktemp " + shellQuote(target+deployTempSuffix+"XXXXXX") + ") || exit 1"
	cleanup := "trap 'rm -f \"$tmp\"' EXIT"
	return tmp + "; " + cleanup + "; cat > \"$tmp\" && chmod +x \"$tmp\" && mv \"$tmp\" " + shellQuote(target)
}

// pushBinary streams data to target over an ssh `cat`, then chmod +x and mv it
// into place. Feeding the binary on the Runner.Run stdin keeps argv[0] == "ssh".
func (m *Manager) pushBinary(ctx context.Context, host hostreg.Host, target string, data io.Reader) error {
	remote := pushBinaryRemote(target)
	out, err := m.runner.Run(ctx, rawCommandArgv(m.opts, host, remote), data)
	if err != nil {
		return fmt.Errorf("%w: host %q push %s: %w: %s", ErrDeploy, host.Name, target, err, tail(out))
	}
	return nil
}

// installScriptURL is where the installer fallback fetches install.sh on the
// host: the documented quickstart one-liner
// (docs/getting-started.md:39-41) pins the repository's main branch. It is
// fetched on the host, so the fallback needs host network access that the push
// path does not.
const installScriptURL = "https://raw.githubusercontent.com/prime-radiant-inc/evener/main/install.sh"

// installerRefFor maps this controller's build channel to the artifact
// reference install.sh accepts as EVENER_INSTALL_VERSION. install.sh treats that
// variable as a GitHub *release tag* (`$repo/releases/download/$version`), while
// buildinfo.Version() is a short Git SHA, possibly "-dirty" — passing it
// verbatim would point every normal build at a release that does not exist. The
// mapping is therefore:
//
//   - release: the stamped ReleaseTag (a Git SHA is never a substitute);
//   - snapshot: the mutable `snapshot` tag, whose commit is pinned afterwards by
//     the post-install version check;
//   - dev/dirty: refused with ErrDeploy — there is no publishable identity to
//     pin, so the operator must use the push path or Options.BuildBinary.
//
// `latest` is never passed.
func installerRefFor(channel, releaseTag, dirty string) (string, error) {
	if strings.TrimSpace(dirty) == "true" {
		return "", fmt.Errorf("%w: this controller was built from a dirty tree, so the installer fallback has no published artifact to pin; use the atomic push path or Options.BuildBinary", ErrDeploy)
	}
	switch channel {
	case "release":
		tag := strings.TrimSpace(releaseTag)
		if tag == "" {
			return "", fmt.Errorf("%w: this controller is a release build but carries no stamped release tag (buildinfo.ReleaseTag); refusing to pass the Git SHA %q as a release tag — use the atomic push path", ErrDeploy, buildinfo.Version())
		}
		return tag, nil
	case "snapshot":
		return "snapshot", nil
	default:
		return "", fmt.Errorf("%w: this controller's build channel %q has no publishable artifact to pin (buildinfo.Version() %q is a Git SHA, not a release tag); use the atomic push path or Options.BuildBinary", ErrDeploy, channel, buildinfo.Version())
	}
}

// installerDirs resolves the BINDIR / EVENER_SHARE_BINDIR the installer must be
// given so it installs to the target the manager will actually run, plus that
// run target. A host with a custom evener_path otherwise installs cleanly into
// ~/.local/bin and then keeps probing and relaunching the old binary at the
// configured path. When evener_path is empty the installer's own default
// `~/.local/bin/evener` IS the run target, returned so the manager records and
// uses it rather than assuming a separate `command -v evener` result.
func installerDirs(host hostreg.Host, facts Preflight) (bindir, shareBindir, runTarget string, err error) {
	p := strings.TrimSpace(host.EvenerPath)
	if p == "" {
		home := strings.TrimSpace(facts.Home)
		if home == "" {
			return "", "", "", fmt.Errorf("%w: host %q has no evener_path and preflight found no HOME to resolve the installer's default ~/.local/bin/evener; set evener_path", ErrDeploy, host.Name)
		}
		return "", "", path.Join(home, ".local", "bin", "evener"), nil
	}
	base := path.Base(p)
	if base != "evener" && base != "evener-dev" {
		return "", "", "", fmt.Errorf("%w: host %q evener_path %q has basename %q, which install.sh does not ship (it installs evener and evener-dev); use the atomic push path", ErrDeploy, host.Name, p, base)
	}
	bindir = path.Dir(p)
	shareBindir = path.Join(path.Dir(bindir), "share", "evener", "bin")
	return bindir, shareBindir, p, nil
}

// installerCommand builds the remote command that runs install.sh pinned to
// ref, installing into bindir/shareBindir when a custom run target is given.
// The documented one-liner runs the installer on the host with the variables
// passed to `env` (not to curl), and every value is shell-quoted.
func installerCommand(ref, bindir, shareBindir string) string {
	env := "EVENER_INSTALL_VERSION=" + shellQuote(ref)
	if bindir != "" {
		env += " BINDIR=" + shellQuote(bindir) + " EVENER_SHARE_BINDIR=" + shellQuote(shareBindir)
	}
	return "curl -fsSL " + shellQuote(installScriptURL) + " | env " + env + " sh"
}

// deployInstaller is the fallback deploy path for a controller with no build
// source: it runs the installer on the host pinned to the controller's own build
// channel, then verifies the installed binary is the expected build before any
// restart or attach. A tag that resolves to a wrong or moved artifact is a
// failed verification (ErrDeploy), never an attach.
func (m *Manager) deployInstaller(ctx context.Context, host hostreg.Host, facts Preflight) (string, error) {
	ref, err := installerRefFor(buildinfo.BuildChannel(), buildinfo.ReleaseTag, buildinfo.GitDirty)
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
	out, err := m.runner.Run(ctx, rawCommandArgv(m.opts, host, installerCommand(ref, bindir, shareBindir)), nil)
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
	if want := m.opts.controllerVersion(); lc.Version != want {
		return "", fmt.Errorf("%w: host %q installer installed version %q, want %q (the pinned artifact does not match the controller's build)", ErrDeploy, host.Name, lc.Version, want)
	}
	return runTarget, nil
}

// existingInstallableEvener resolves the canonical path of the evener the host
// already has installed — the running hub's own executable first, then whatever
// `evener` resolves to on the remote PATH — or "" when none can be identified or
// the found binary has a basename install.sh does not ship. It is how a deploy
// with no configured evener_path keeps the existing install location instead of
// writing to an unrelated default.
func (m *Manager) existingInstallableEvener(ctx context.Context, host hostreg.Host) string {
	if exe := m.currentHubExecutable(ctx, host); installableEvenerBasename(exe) {
		return exe
	}
	p, err := m.evenerOnPath(ctx, host)
	if err != nil || p == "" {
		return ""
	}
	resolved, err := m.resolveRemotePath(ctx, host, p)
	if err != nil || !installableEvenerBasename(resolved) {
		return ""
	}
	return resolved
}
