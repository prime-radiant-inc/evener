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
	"runtime"
	"slices"
	"strconv"
	"strings"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/buildinfo"
	"primeradiant.com/evener/cmd/evener-hub/internal/hostreg"
	"primeradiant.com/evener/execsupport/shellquote"
	"primeradiant.com/evener/internal/hubendpoint"
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
// tracked placeholder and serve the documented 503, which build identity
// verification cannot detect.
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
// the controller's version while running something else, passing deployment
// identity checks. When this process carries a GitSHA, the source checkout's
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
	// deployment identity checks cannot verify.
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
	// binary that still reports this controller's clean version and passes
	// deployment identity checks. Require a clean worktree.
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

// ErrControllerDirty marks a deploy refused because this controller was built
// from a dirty tree and the configured deploy source is not the controller's own
// executable. Such a build has no reproducible identity: the push path refuses
// to compile a tree it cannot prove matches this process (verifyBuildRevision),
// the installer fallback has no published artifact to pin (installerRefFor), and
// build labels cannot tell an operator-named artifact built from another
// dirty tree at the same commit apart from this controller's build — "<sha>-dirty"
// is not an identity. Only a source whose bytes ARE this controller's build
// (Options.OwnExecutable: the hub's own executable, the file this process was
// started from) is not refused; its on-host identity is still judged
// (ErrDeployUnstamped). The
// refusal is terminal rather than ErrDeploy: the same install can never succeed
// by being retried, and a supervisor that retried it cross-compiled forever while
// the host was never attached (round thirteen).
var ErrControllerDirty = errors.New("sshconn: controller build is a dirty tree")

// ErrDeployUnstamped marks a deploy that left the host reporting a build other
// than the controller's. Unlike the dirty-tree refusal above, the controller HAD a
// deploy path and used it; the freshly re-read launch contract still disagrees, so
// the artifact that reached the host was not stamped by this controller — the one
// case the pre-push check cannot catch, an operator-supplied -deploy-binary built
// from a different tree that targets the right platform but carries another
// build's identity. The refusal is terminal rather than ErrDeploy: retrying
// re-pushes the same artifact, so the supervisor would loop forever while the host
// was never attached (the same mistake round thirteen records for the
// dirty-controller refusal). Attaching instead would claim success for a
// deployment that did not install the requested build.
//
// It is judged only where a deploy ran. A pass that merely started or restarted
// the hub launched the build already on disk, and the host is allowed to keep that
// build: the protocol, not the build label, decides whether it may attach.
var ErrDeployUnstamped = errors.New("sshconn: deployed build is not stamped by this controller")

// deploy installs a matching build on the host. The cross-compile + push path is
// primary when a build source is configured; otherwise the installer fallback
// runs on the host. It returns the resolved run target the manager must record as
// host.EvenerPath: the canonical path the push installed to, or the installer's
// run target. Both are non-empty on success, so the restart, health re-probe, and
// attach paths address the binary that was actually installed instead of a bare
// `evener` a fresh host's non-interactive PATH may not carry.
func (m *Manager) deploy(ctx context.Context, host hostreg.Host, facts Preflight, beforeWrite func(string) error, retainedTarget ...string) (string, error) {
	// Deploying turned off wins over every deploy source: a hub started with
	// -no-deploy installs nothing on a host, not even through the installer
	// fallback a release or snapshot controller would otherwise use. The attach
	// ladder never gets here (canDeploy is false), so this refusal is the
	// explicit host-deploy operation's answer: terminal, naming the remedy.
	if m.opts.DeployDisabled {
		return "", fmt.Errorf("%w: host %q: this controller was started with deploying disabled, so it will not install a build on any host; %s",
			ErrDeployDisabled, host.Name, m.deployHelp())
	}
	// A dirty controller cannot install a build it cannot reproduce. A dirty
	// "<sha>-dirty" label is not an identity: another dirty checkout at the same
	// commit reports it too. This gate applies only when installation is needed
	// or explicitly requested; compatible Connect does not reach it.
	//
	// Only the controller's own executable (Options.OwnExecutable) is exempt, and
	// it is exempt on identity, not convenience: those bytes ARE the build this
	// controller runs, so installing them needs no reproduction proof and leaves
	// nothing for a reproducibility check to prove. Every other source is refused:
	// compiling a checkout requires proving it reproduces the running "<sha>-dirty"
	// tree, which cannot be done; the installer fallback has no published artifact
	// to pin; and an operator-named artifact cannot be told apart from a foreign
	// dirty build, because its "<sha>-dirty" label is shared by every dirty tree at
	// that commit — the pre-push checks pass, and the post-deploy version
	// comparison (ErrDeployUnstamped) cannot see the difference either, so the host
	// would attach on code that is not this controller's.
	//
	// The accepted residual for the exempt source: its file is read at deploy
	// time, so a hub whose executable is replaced under it deploys the
	// replacement. A cross-commit replacement is caught after the push
	// (ErrDeployUnstamped: the host reports a version this controller did not
	// stamp); a replacement that is itself a dirty build at this controller's own
	// commit reports the same "<sha>-dirty" and is not detectable. That corner —
	// a locally built dirty controller whose executable is replaced while it runs
	// — is accepted rather than closed with hash plumbing: the label could not
	// distinguish it anyway, the default's whole meaning is "deploy the file at
	// this process's executable path", and an operator who wants a different
	// artifact names it for a controller that can verify it. Refuse the rest
	// terminally, naming the cause: retrying is what turned this refusal into an
	// endless cross-compile, because every attempt failed as a retryable ErrDeploy
	// and markDevDeployed was never reached.
	if version := m.opts.controllerVersion(); isDirtyVersion(version) && !m.ownExecutableIsStagingPath() {
		return "", fmt.Errorf("%w: host %q: this controller was built from a dirty tree (version %q), so it cannot install this build: a checkout cannot be proven to reproduce the dirty tree, the installer fallback has no published artifact to pin, and a named artifact cannot be told apart from a foreign dirty build (a %q version names a commit plus uncommitted changes, not the code); rebuild the controller from a clean checkout, or deploy the controller's own executable — the one source a dirty controller can install",
			ErrControllerDirty, host.Name, version, version)
	}
	// The defaulted own executable is this process's build, so it can serve only
	// hosts on this process's own target (runtime.GOOS/GOARCH). For a host on any
	// other target the push cannot work, and before the default existed a
	// flagless controller had no push path at all: it reached the installer
	// fallback here, which is the working path for a differently-architected host
	// with a published artifact to pin. Keep that fallback reachable for exactly
	// this source, so the default does not shadow it; every other source keeps
	// the push — -build-source cross-compiles, and an explicitly named artifact
	// keeps its own terminal mismatch refusal, which names the flag to fix.
	guardedWrite := func(target string) error {
		if err := m.refuseNewerInstalledProtocol(ctx, host, target); err != nil {
			return err
		}
		if beforeWrite != nil {
			return beforeWrite(target)
		}
		return nil
	}
	if m.canBuild() && !m.ownExecutableCannotServe(facts) {
		return m.deployPush(ctx, host, facts, guardedWrite, retainedTarget...)
	}
	if len(retainedTarget) != 0 {
		// The installer writes a share directory and replaces the launch path
		// with a symlink, so its run target cannot pin the retained write.
		return "", fmt.Errorf("%w: host %q: installer layout cannot preserve recorded install target %q; %s", errRetainedInstaller, host.Name, retainedTarget[0], m.installerRemedy())
	}
	if m.ownExecutableCannotServe(facts) {
		// The only source is the own executable, the host runs another target, and
		// the installer fallback is the one remaining path. If it has no published
		// artifact to pin (a dev, dirty, or tag-less release controller), this
		// controller can never provision this host, and the state is permanent: the
		// same host, target, and build re-refuse identically. Refuse terminally
		// with that cause rather than falling into installerRefFor's retryable
		// ErrDeploy — in this position the reconnect loop would retry it forever
		// while the host was never attached (round thirteen).
		if _, err := installerRefFor(buildinfo.BuildChannel(), buildinfo.ReleaseTag, buildinfo.GitDirty, m.installerRemedy()); err != nil {
			return "", fmt.Errorf("%w: host %q: this controller's own executable targets %s/%s but the host runs %s/%s, and the installer fallback has no published artifact to pin for this controller's build (a dev or dirty controller has none, and a release needs a stamped tag); %s",
				ErrOwnExecutableCannotServe, host.Name, runtime.GOOS, runtime.GOARCH, facts.OS, facts.Arch, m.deployHelp())
		}
		return m.deployInstaller(ctx, host, facts, guardedWrite)
	}
	return m.deployInstaller(ctx, host, facts, guardedWrite)
}

// refuseNewerInstalledProtocol re-reads the executable at target immediately
// before a deploy writes over it. Preflight facts can age while a build stages,
// and another controller may install a newer build in that window. The
// executable's own contract is the only current evidence; a read failure,
// including a missing target on a fresh install, is not proof of a newer
// installation and leaves the existing deploy rules in charge.
func (m *Manager) refuseNewerInstalledProtocol(ctx context.Context, host hostreg.Host, target string) error {
	probeHost := host
	probeHost.EvenerPath = target
	described, _, err := m.describeLaunchContract(ctx, probeHost)
	if err == nil && appwire.NewerProtocolVersion(described.Protocol) {
		return fmt.Errorf("%w: host %q's installed executable speaks %q, newer than this controller's %q; update the controller instead of downgrading the host",
			ErrHostProtocolNewer, host.Name, described.Protocol, appwire.ProtocolVersion)
	}
	return nil
}

// TargetMatches reports whether a build for buildOS/buildArch can serve a host
// running hostOS/hostArch. It is the one expression of the target rule: the
// dispatch that chooses between the push and the installer fallback
// (ownExecutableCannotServe) and the hub's staging refusal for an artifact
// (copyDeployBinary) both read it, so the two cannot disagree about what "the
// host's target" means.
func TargetMatches(buildOS, buildArch, hostOS, hostArch string) bool {
	return buildOS == hostOS && buildArch == hostArch
}

// ownExecutableIsStagingPath reports whether this Manager would actually stage
// this controller's own executable: the OwnExecutable marker AND a BuildBinary to
// stage it through. The marker is a claim about BuildBinary, so a Manager that
// would build from BuildSource is judged by that path instead — which is what
// keeps the dirty-controller refusal intact for it.
func (m *Manager) ownExecutableIsStagingPath() bool {
	return m.opts.OwnExecutable && m.opts.BuildBinary != nil
}

// ownExecutableCannotServe reports whether the source this Manager would deploy
// from is the defaulted own executable and the host's target differs from this
// process's own. The defaulted artifact's target is this process's target by
// construction, so this is the one configuration whose push can never serve the
// host. It is deliberately narrow: an explicit artifact is not covered (its
// mismatch is the operator's to fix and the hub refuses it terminally), and
// neither is a build source (it cross-compiles). OwnExecutable is read together
// with BuildBinary because the claim is about the staging path: a Manager that
// would stage through BuildSource is judged by that path, not by this marker.
func (m *Manager) ownExecutableCannotServe(facts Preflight) bool {
	if !m.ownExecutableIsStagingPath() {
		return false
	}
	return !TargetMatches(runtime.GOOS, runtime.GOARCH, facts.OS, facts.Arch)
}

// deployPush cross-compiles this tree for the host target and atomically
// installs it at the host's evener path. The existing binary is untouched unless
// the push fully succeeds. It returns the canonical install target so the caller
// can record it as host.EvenerPath even when the registry did not configure one:
// on a fresh host that target is the installer default ~/.local/bin/evener, and
// discarding it left the manager probing the literal `evener`, which the
// non-interactive PATH cannot resolve.
func (m *Manager) deployPush(ctx context.Context, host hostreg.Host, facts Preflight, beforeWrite func(string) error, retainedTarget ...string) (string, error) {
	var target string
	var err error
	if len(retainedTarget) != 0 {
		// Retained recovery has already resolved and checked this destination.
		// Its beforeWrite callback repeats that proof after staging the binary.
		target, err = m.resolveDeployOrCreateTarget(ctx, host, retainedTarget[0])
	} else {
		target, err = m.deployTarget(ctx, host, facts)
	}
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
		// -deploy-binary built for another platform (ErrDeployArtifactUnusable) —
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
	// Staging can take long enough for either launch intent or the recorded
	// destination to be retargeted. Do not authorize a write with pre-build paths.
	var launchTarget string
	if len(retainedTarget) != 0 && facts.ExecutableMissing {
		out, readErr := m.runner.Run(ctx, rawCommandArgv(m.opts, host, resolveRetainedDeployCommand(host.EvenerPath)), nil)
		if readErr != nil {
			return "", errOwnerSelectionMismatch
		}
		launchTarget, err = ownerPathOutput(out)
	} else {
		launchTarget, err = m.deployTarget(ctx, host, facts)
	}
	if err != nil {
		return "", err
	}
	if launchTarget != target {
		return "", errOwnerSelectionMismatch
	}
	if len(retainedTarget) != 0 {
		currentTarget, err := m.resolveDeployOrCreateTarget(ctx, host, retainedTarget[0])
		if err != nil {
			return "", err
		}
		if currentTarget != retainedTarget[0] || currentTarget != target {
			return "", errOwnerSelectionMismatch
		}
	}
	if beforeWrite != nil {
		if err := beforeWrite(target); err != nil {
			return "", err
		}
	}
	if err := m.pushBinary(ctx, host, target, f, info.Size()); err != nil {
		return "", err
	}
	return target, nil
}

// ErrRunTargetUnservable marks a deploy refused because the configured run
// target is not the `evener` binary a host hub can serve — `evener-dev` (the
// development tooling binary: no `hub` subcommand, no `launch-check`) or any
// other basename. The refusal is terminal rather than ErrDeploy: a misconfigured
// run target is an operator configuration defect the host cannot recover from,
// so retrying can never install a binary the hub can run — the supervisor would
// re-refuse the same path forever and discard the cause, the basename to fix
// (the identical mistake round thirteen records for the dirty-controller
// refusal).
var ErrRunTargetUnservable = errors.New("sshconn: run target cannot serve a hub")

// ErrDeployArtifactUnusable marks a deploy refused because the operator-supplied
// artifact (-deploy-binary) cannot serve the host: it was built for a different
// GOOS/GOARCH, or it is a Go program that is not evener (the hub's
// copyDeployBinary refuses both before anything is staged). The operator supplies
// the artifact, so either defect is a permanent mistake — the same file is
// re-read on every retry — and the refusal is terminal for the same reason
// ErrRunTargetUnservable is: the supervisor would re-refuse it forever. deployPush
// also keeps this sentinel out of the retryable ErrDeploy wrap, so terminal is the
// class that reaches both the reconnect loop and the hub's attach handler.
var ErrDeployArtifactUnusable = errors.New("sshconn: deploy artifact cannot serve the host")

// ErrOwnExecutableCannotServe marks a deploy refused because the controller's
// only deploy source is its own executable, the host runs a different target,
// and the installer fallback cannot pin a published artifact for this build (a
// dev, dirty, or tag-less release controller has none). Such a controller can
// never provision this host, and the state is permanent — the same host, target,
// and build re-refuse identically — so the refusal is terminal rather than the
// retryable ErrDeploy installerRefFor's own refusal carries: in this position
// the reconnect loop would retry it forever while the host was never attached
// (the pattern round thirteen records). It arrives only from the defaulted
// source: an explicitly named artifact keeps its own terminal mismatch refusal
// (ErrDeployArtifactUnusable, which names the flag to fix), and a build source
// cross-compiles.
var ErrOwnExecutableCannotServe = errors.New("sshconn: this controller's own executable cannot serve the host's target")

// ErrDeployDisabled marks a deploy refused because deploying is turned off for
// this Manager (Options.DeployDisabled, the hub's -no-deploy). It is terminal
// rather than ErrDeploy: the setting is a deliberate configuration, so a retry
// re-refuses identically, and the refusal names the remedy through DeployHelp so
// an operator who wants deploys back knows how to get them. canDeploy reports
// false under the same option, so the attach ladder never reaches this refusal;
// the explicit host-deploy operation does, which is why the sentinel exists at
// all.
var ErrDeployDisabled = errors.New("sshconn: deploying is disabled on this controller")

// Retrying an installer cannot preserve the retained deployment's pinned target.
var errRetainedInstaller = fmt.Errorf("%w: retained deployment cannot use the installer fallback", errOwnerSelectionMismatch)

// checkRunTarget refuses a configured evener_path that cannot be the host hub's
// run target. Only the shipped `evener` binary can serve a hub: `evener-dev` is
// the development/test tooling binary — no `hub` subcommand and no
// `launch-check` — so a host configured to run it installs "successfully" and
// then fails preflight, health, and restart on a binary that can never serve
// the hub. Any other basename is no better: the manager records this one path
// as the host's run target and probes, restarts, and attaches the binary at it.
//
// It is checked before the target is probed, pushed, or installed, and it is
// terminal (ErrRunTargetUnservable) with no write. The ordering and the sentinel
// are both the point: the refusal names a configuration defect the host cannot
// recover from, so a retry could only re-refuse it forever, and discovering it
// after the write would only leave the host holding a binary the controller can
// never run. The missing-directory refusal beside it stays a retryable ErrDeploy
// because a directory can appear.
func checkRunTarget(hostName, p string) error {
	if installableEvenerBasename(p) {
		return nil
	}
	base := path.Base(p)
	if base == "evener-dev" {
		return fmt.Errorf("%w: host %q evener_path %q names %q, the development tooling binary (cmd/evener-dev), which does not provide the hub command and has no launch-check, so it can never serve a hub; configure the evener binary", ErrRunTargetUnservable, hostName, p, base)
	}
	return fmt.Errorf("%w: host %q evener_path %q has basename %q, which is not the evener binary that serves a hub; configure an evener-named run target", ErrRunTargetUnservable, hostName, p, base)
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
func (m *Manager) deployTarget(ctx context.Context, host hostreg.Host, facts Preflight, checks ...func(string) error) (string, error) {
	if p := host.EvenerPath; p != "" {
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

	p, err := m.evenerOnPath(ctx, host)
	if err != nil {
		return "", err
	}
	if p == "" {
		// No running hub and no evener on the non-interactive PATH either. Fall
		// back to the installer's own default location and create its directory,
		// so a push deploy can still bring up a host that has no evener installed
		// at all.
		home := facts.Home
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
	if len(out) == 0 || string(out) == evenerPathMissingMarker+"\n" {
		return "", nil
	}
	p, err := commandPathOutput(out)
	if err != nil {
		return "", fmt.Errorf("%w: host %q resolve evener on PATH: %w", ErrDeploy, host.Name, err)
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
// A sentinel protects command output from shell substitution's removal of all
// trailing LFs. Strip exactly the command's one framing LF, and refuse pathname
// LFs before a later substitution could turn them into a different path.
const pathValueScript = `evener_path_value() {
  evener_value=$("$@" && printf '.') || return 1
  evener_value=${evener_value%.}
  case "$evener_value" in
    *'
') evener_value=${evener_value%?} ;;
    *) return 1 ;;
  esac
  case "$evener_value" in ''|*'
'*) return 1 ;; esac
  printf '%s\n' "$evener_value"
}
evener_directory() { CDPATH='' cd -P -- "$1" 2>/dev/null && pwd -P; }
`

const resolvePathPrefix = pathValueScript + `evener_resolve() {
  p=$1
  n=0
  while [ -L "$p" ]; do
    n=$((n+1))
    [ "$n" -le 40 ] || return 1
    d=$(evener_path_value dirname "$p") || return 1
    d=$(evener_path_value evener_directory "$d") || return 1
    t=$(evener_path_value readlink "$p") || return 1
    case $t in
      /*) p=$t ;;
      *) p=$d/$t ;;
    esac
  done
`

const resolvePathSuffix = `  d=$(evener_path_value dirname "$p") || return 1
  d=$(evener_path_value evener_directory "$d") || return 1
  printf '%s\n' "$d/${p##*/}"
}
evener_resolve `

const resolvePathScript = resolvePathPrefix + "  [ -f \"$p\" ] || return 1\n" + resolvePathSuffix

// resolveDeployCommand builds the remote command that prints the real path p
// names.
func resolveDeployCommand(p string) string {
	return resolvePathScript + shellquote.RemoteWord(p)
}

// Only retained recovery may resolve an absent final file. The caller must
// compare the result with its recorded installTarget before every write.
// Parent directories must exist, directories are refused, and the same bounded
// symlink traversal and byte-preserving pathname framing apply as above.
func resolveRetainedDeployCommand(p string) string {
	return resolvePathPrefix + "  [ -f \"$p\" ] || [ ! -e \"$p\" ] || return 1\n" + resolvePathSuffix + shellquote.RemoteWord(p)
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
	resolved, err := ownerPathOutput(out)
	if err != nil {
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
	created, decodeErr := ownerPathOutput(out)
	if decodeErr != nil {
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
	return pathValueScript + "p=" + q + "; if [ -e \"$p\" ] || [ -L \"$p\" ]; then exit 1; fi; " +
		"d=$(evener_path_value dirname \"$p\") || exit 1; " +
		"d=$(evener_path_value evener_directory \"$d\") || exit 1; " +
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
//   - snapshot: the mutable `snapshot` prerelease. It is not pinned by
//     construction: the post-install version check accepts it only while the
//     channel still serves this controller's commit. Once the channel moves
//     past it, the check refuses terminally (ErrVersionMismatch) rather than
//     re-fetching the same artifact forever;
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
	p := host.EvenerPath
	if p == "" {
		home := facts.Home
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

// DeployRunTargetFor resolves the run target a deploy would install to for host,
// as a pure function of its entry and the host's home directory: the configured
// evener_path — refused here when it cannot serve a hub (checkRunTarget) — or
// the installer's default ~/.local/bin/evener under home. It is installerDirs'
// own rule, exported for evener/host/plan, which must name the resolved deploy
// target it would deploy to (deploy pipeline 08b §6 step 1) before anything
// dials: a plan that named a different path than the deploy uses would be a plan
// for a different deploy.
//
// The extra resolutions the deploy paths apply on top — the running hub's own
// executable, then whatever `evener` resolves to on the remote PATH — need
// remote commands and belong to the paths that dial, so a plan resolves the
// entry-and-facts rule and the deploy re-resolves with what it finds live.
//
// home is the only preflight fact this rule reads, so the parameter is the fact
// itself rather than a whole Preflight a caller would have to reconstruct:
// passing a partially-filled probe struct would invite a caller to leave a field
// this rule silently depends on at its zero value.
func DeployRunTargetFor(host hostreg.Host, home string) (string, error) {
	_, _, runTarget, err := installerDirs(host, Preflight{Home: home})
	return runTarget, err
}

// deployInstaller is the fallback deploy path for a controller with no build
// source: it streams the embedded installer to the host pinned to the
// controller's own build channel, then verifies the installed binary is the
// expected build before any restart or attach. A tag that resolves to a wrong or
// moved artifact is a failed verification (a terminal ErrVersionMismatch), never
// a retry of the same pinned ref and never an attach.
func (m *Manager) deployInstaller(ctx context.Context, host hostreg.Host, facts Preflight, beforeWrite func(string) error) (string, error) {
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
	if host.EvenerPath == "" {
		if existing := m.existingInstallableEvener(ctx, host); existing != "" {
			host.EvenerPath = existing
		}
	}
	bindir, shareBindir, runTarget, err := installerDirs(host, facts)
	if err != nil {
		return "", err
	}
	if beforeWrite != nil {
		if err := beforeWrite(runTarget); err != nil {
			return "", err
		}
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
	// (for a snapshot build, the mutable `snapshot` channel). Refuse terminally as a
	// version mismatch — ErrDeploy would be retried forever by the supervisor,
	// re-downloading and re-rejecting the same unmatchable artifact. The remedy
	// clause names the path that does carry a provable identity, so the operator
	// is told how to stop depending on the moved channel.
	if want := m.opts.controllerVersion(); lc.Version != want {
		return "", fmt.Errorf("%w: host %q installer installed version %q, want %q (the pinned artifact %q does not match the controller's build; a moved channel cannot be resolved by retrying); %s", ErrVersionMismatch, host.Name, lc.Version, want, ref, m.installerRemedy())
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
	p, err := m.evenerOnPath(ctx, host)
	if err != nil || p == "" || !installableEvenerBasename(p) {
		return ""
	}
	return p
}

func (m *Manager) deploySelectedWithAuthority(ctx context.Context, host hostreg.Host, facts Preflight, realEndpointAuthority bool, recorders ...func(string) error) (string, error) {
	return m.deploySelectedWithSupersession(ctx, host, facts, realEndpointAuthority, nil, recorders...)
}

func (m *Manager) deploySelectedWithSupersession(ctx context.Context, host hostreg.Host, facts Preflight, realEndpointAuthority bool, supersede func() error, recorders ...func(string) error) (string, error) {
	m.applyResolvedTarget(&host)
	if m.opts.DeployDisabled || (isDirtyVersion(m.opts.controllerVersion()) && !m.ownExecutableIsStagingPath()) {
		return m.deploy(ctx, host, facts, nil)
	}
	retained := m.pendingRestart(host.Name)
	retainedDeployment := realEndpointAuthority && retained.deployment && retained.selected != nil
	if retainedDeployment && (facts.ExecutableMissing || facts.HubEndpointSchema != 1) {
		// A failed executable probe, not unreadable metadata, authorizes only the
		// retained operation's original write. The post-write contract stays strict.
		unusable, probeErr := m.recordedTargetUnusable(ctx, host)
		if unusable {
			return m.reinstallRetainedTarget(ctx, host, facts, retained, recorders...)
		}
		if probeErr != nil {
			return "", probeErr
		}
	}
	readEndpoint := m.resolveHostEndpoint
	if realEndpointAuthority {
		readEndpoint = m.readHostEndpoint
	}
	var owner selectedOwner
	if !facts.ExecutableMissing {
		if host.EvenerPath == "" {
			target, err := m.evenerOnPath(ctx, host)
			if err != nil {
				return "", err
			}
			if target == "" {
				return "", fmt.Errorf("%w: installed executable no longer resolves", errOwnerEvidence)
			}
			host.EvenerPath = target
			m.setResolvedTarget(host.Name, target)
		}
		var err error
		owner, err = m.resolveOwnedEndpointUsing(ctx, host, facts, readEndpoint)
		if err != nil {
			return "", err
		}
	}
	checkRetained := func(now selectedOwner) error {
		if !retainedDeployment {
			return nil
		}
		if err := m.checkEffectBinding(retained.binding); err != nil {
			return err
		}
		prior := *retained.selected
		if now.report.State == "stopped" {
			if !hostreg.SameRegistrationAndPresence(now.host, prior.host) || now.report.ConfigPath != prior.report.ConfigPath || now.report.HubStateRoot != prior.report.HubStateRoot {
				return errOwnerSelectionMismatch
			}
			if err := m.checkOwnerConfig(ctx, host, prior.argv, prior.cwd, prior.env, now.report); err != nil {
				return err
			}
			if prior.supervisor.kind != supervisorNone {
				return m.revalidateStoppedService(ctx, host, facts, prior)
			}
			if now.supervisor.kind != supervisorNone {
				return errOwnerSelectionMismatch
			}
			return nil
		}
		if !sameOwner(now, prior) {
			return errOwnerSelectionMismatch
		}
		// Health can be unavailable even though the independently inspected owner
		// and original-image capability still match. Compare health when both exist.
		current, before := now.report.Running, prior.report.Running
		if current != nil && before != nil && (current.BootID != before.BootID || current.Version != before.Version || current.BackendGitSHA != before.BackendGitSHA) {
			return errOwnerSelectionMismatch
		}
		return nil
	}
	if err := checkRetained(owner); err != nil {
		return "", err
	}
	beforeWrite := func(target string) error {
		if retainedDeployment && (retained.installTarget == "" || target != retained.installTarget) {
			return errOwnerSelectionMismatch
		}
		previous := m.pendingRestart(host.Name)
		var fresh pendingRestartState
		if facts.ExecutableMissing {
			probeHost := host
			if target != "" {
				probeHost.EvenerPath = target
			}
			_, err := m.probeLaunchCheck(ctx, probeHost)
			if !errors.Is(err, ErrExecutableMissing) {
				if err != nil {
					return err
				}
				return fmt.Errorf("%w: executable appeared before fresh install", errOwnerEvidence)
			}
		} else {
			current, err := m.revalidateOwnedEndpointUsing(ctx, host, facts, owner, readEndpoint)
			if err != nil {
				return err
			}
			if err := checkRetained(current); err != nil {
				return err
			}
			if supersede != nil {
				fresh, err = m.selectedRestartIntentUsing(ctx, host, facts, owner, m.opts.controllerVersion(), snapshotPinGitSHA(), readEndpoint)
				if err != nil {
					return err
				}
			} else if m.pendingRestart(host.Name).selected == nil {
				if _, err := m.prepareSelectedRestartUsing(ctx, host, facts, owner, m.opts.controllerVersion(), snapshotPinGitSHA(), readEndpoint); err != nil {
					return err
				}
			}
		}
		if supersede != nil {
			if err := supersede(); err != nil {
				return err
			}
			m.mu.Lock()
			m.pendingRestarts[host.Name] = fresh
			m.mu.Unlock()
		}
		for _, record := range recorders {
			if err := record(target); err != nil {
				m.mu.Lock()
				m.pendingRestarts[host.Name] = previous
				m.mu.Unlock()
				return err
			}
		}
		if supersede == nil && previous.command == "" {
			m.mu.Lock()
			pending := m.pendingRestarts[host.Name]
			pending.command = ""
			m.pendingRestarts[host.Name] = pending
			m.mu.Unlock()
		}
		return nil
	}
	var retainedTarget []string
	if retainedDeployment {
		retainedTarget = []string{retained.installTarget}
	}
	target, err := m.deploy(ctx, host, facts, beforeWrite, retainedTarget...)
	if err != nil {
		return "", err
	}
	if !facts.ExecutableMissing {
		return host.EvenerPath, nil
	}
	return target, nil
}

func (m *Manager) recordedTargetUnusable(ctx context.Context, host hostreg.Host) (bool, error) {
	_, err := m.runner.Run(ctx, rawCommandArgv(m.opts, host, "if test -x "+shellquote.RemoteWord(host.EvenerPath)+"; then exit 0; else exit 1; fi"), nil)
	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.ExitCode() == 1 {
		return true, nil
	}
	return false, err
}

// reinstallRetainedTarget has no discovery or fresh-operation authority. It
// writes only the already-recorded target after checking the retained binding
// and owner context again at the write boundary.
func (m *Manager) reinstallRetainedTarget(ctx context.Context, host hostreg.Host, facts Preflight, pending pendingRestartState, recorders ...func(string) error) (string, error) {
	prior := *pending.selected
	check := func(target string) error {
		if pending.installTarget == "" || target != pending.installTarget || host.EvenerPath != prior.host.EvenerPath || pending.expectedVersion != m.opts.controllerVersion() || !hostreg.SameRegistrationAndPresence(host, prior.host) {
			return errOwnerSelectionMismatch
		}
		if host.EvenerPath != pending.installTarget {
			out, err := m.runner.Run(ctx, rawCommandArgv(m.opts, host, resolveRetainedDeployCommand(host.EvenerPath)), nil)
			if err != nil {
				return errOwnerSelectionMismatch
			}
			resolved, err := ownerPathOutput(out)
			if err != nil || resolved != pending.installTarget {
				return errOwnerSelectionMismatch
			}
		}
		current, ok := m.reg.Get(host.Name)
		m.applyResolvedTarget(&current)
		if !ok || !hostreg.SameRegistrationAndPresence(current, host) {
			return errOwnerSelectionMismatch
		}
		if err := m.checkEffectBinding(pending.binding); err != nil {
			return err
		}
		_, err := m.runner.Run(ctx, rawCommandArgv(m.opts, host, "if test -x "+shellquote.RemoteWord(target)+"; then exit 0; else exit 1; fi"), nil)
		var exit *exec.ExitError
		if !errors.As(err, &exit) || exit.ExitCode() != 1 {
			return fmt.Errorf("%w: retained target is no longer independently missing or nonexecutable", errOwnerEvidence)
		}
		if facts.OS == "darwin" {
			return m.revalidateRetainedDarwin(ctx, host, facts, prior)
		}
		if prior.pid != "0" && facts.OS == "linux" {
			generation, generationErr := m.ownerGeneration(ctx, host, prior.pid)
			if generationErr == nil {
				if generation != prior.generation {
					return errOwnerSelectionMismatch
				}
				if err := m.checkHubOwner(ctx, host, prior.pid); err != nil {
					return err
				}
				// This retained reinstall already proved the target unusable and
				// bound this PID to the recorded owner. Match every recorded argv
				// word, including the exact executable spelling, without resolving
				// the deleted pathname. Ordinary recovery keeps its existing-file
				// executable check; the live image is validated below via /proc.
				argv, err := m.recoverHubArgvRaw(ctx, host, prior.pid)
				if err != nil || !slices.Equal(argv, prior.argv) {
					return errOwnerSelectionMismatch
				}
				cwdOut, err := m.ownerRead(ctx, host, "readlink /proc/"+prior.pid+"/cwd")
				if err != nil {
					return errOwnerSelectionMismatch
				}
				cwd, err := ownerPathOutput(cwdOut)
				if err != nil || cwd != prior.cwd {
					return errOwnerSelectionMismatch
				}
				env, err := m.ownerEnvironment(ctx, host, prior.pid)
				if err != nil || !slices.Equal(env, prior.env) {
					return errOwnerSelectionMismatch
				}
				sup, _, err := m.supervisorForOwner(ctx, host, prior.pid, argv, prior.cwd, prior.env, prior.report)
				if err != nil || sup != prior.supervisor {
					return errOwnerSelectionMismatch
				}
				if sup.kind != supervisorNone {
					definition, err := m.serviceDefinition(ctx, host, sup)
					if err != nil || definition != prior.definition {
						return errOwnerSelectionMismatch
					}
				}
				// Only now is this PID bound to the recorded owner. Its executable
				// remains runnable after the recorded pathname is removed.
				if err := m.checkOwnerConfigUsing(ctx, host, argv, prior.cwd, env, prior.report, "/proc/"+prior.pid+"/exe", func(report hubendpoint.Report) error {
					now, before := report.Running, prior.report.Running
					if now != nil && strconv.Itoa(now.PID) != prior.pid {
						return errOwnerSelectionMismatch
					}
					if now != nil && before != nil && (now.BootID != before.BootID || now.Version != before.Version || now.BackendGitSHA != before.BackendGitSHA) {
						return errOwnerSelectionMismatch
					}
					return nil
				}); err != nil {
					return fmt.Errorf("%w: %w", endpointMigration(host), err)
				}
				after, err := m.ownerGeneration(ctx, host, prior.pid)
				if err != nil || after != generation {
					return errOwnerSelectionMismatch
				}
				return nil
			}
			// A failed generation read is not process absence. Reuse the
			// explicit exit probe, and preserve inconclusive generation errors.
			out, err := m.ownerRead(ctx, host, processAliveRemote(prior.pid))
			if err != nil {
				return err
			}
			if strings.TrimSpace(string(out)) != pidGoneMarker {
				return generationErr
			}
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if prior.supervisor.kind != supervisorNone {
			return m.revalidateStoppedService(ctx, host, facts, prior)
		}
		sup, _, err := m.supervisorForOwner(ctx, host, "0", prior.argv, prior.cwd, prior.env, prior.report)
		if err != nil || sup.kind != supervisorNone {
			return errOwnerSelectionMismatch
		}
		return nil
	}
	if err := check(pending.installTarget); err != nil {
		return "", err
	}
	_, err := m.deploy(ctx, host, facts, func(target string) error {
		if err := check(target); err != nil {
			return err
		}
		for _, record := range recorders {
			if err := record(target); err != nil {
				return err
			}
		}
		return nil
	}, pending.installTarget)
	if err != nil {
		return "", err
	}
	after, err := m.reReadLaunchContract(ctx, host, facts)
	if err != nil {
		return "", err
	}
	if err := m.deployedBuildNotStamped(host.Name, after, pending.expectedVersion); err != nil {
		return "", err
	}
	if err := m.checkOwnerConfig(ctx, host, prior.argv, prior.cwd, prior.env, prior.report); err != nil {
		return "", fmt.Errorf("%w: %w", endpointMigration(host), err)
	}
	return prior.host.EvenerPath, nil
}
