package hub

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/buildinfo"
	"primeradiant.com/evener/envvars"
	"primeradiant.com/evener/internal/appserver"
	"primeradiant.com/evener/internal/selfupdate"
)

// Test seams: the GitHub check, the exec-in-place restart, and the restart
// goroutine's log destination.
var (
	runHubUpdateCheck             = selfupdate.Check
	scheduleHubRestart            = scheduleHubRestartAfterResponse
	execHubBinary                 = selfupdate.Restart
	hubRestartSupported           = selfupdate.RestartSupported
	hubUpdateStderr     io.Writer = os.Stderr
)

// hubRestartDelay is a short grace period for the kernel to flush the apply
// response frame the send loop has just written; the restart itself already
// waits for that write (see scheduleHubRestartAfterResponse). A var, not a
// const, so tests can set it to 0.
var hubRestartDelay = 500 * time.Millisecond

// hubRestartLockTimeout bounds a restart's wait for the cross-process install
// lock. A stuck holder must abort the restart, not strand the hub past its
// health-poll window; the bound stays well under the frontend's 30s restart
// poll. A var, not a const, so tests can shrink it.
var hubRestartLockTimeout = 10 * time.Second

// hubUpgradeTimeout is the one overall deadline for a complete upgrade
// operation: archive download + checksums download + verify + install.
// The per-request http.Client timeout (selfupdate.defaultUpgradeTimeout)
// applies to each download separately and the two can add up, so without
// an operation bound a valid update can outlive the frontend's apply
// timeout and strand the page. Both hubUpdateApply and hubUpgrade funnel
// through runHubSelfUpgradeWithTimeout. A var so tests can shrink it.
var hubUpgradeTimeout = 4 * time.Minute

// hubUpdateMu serializes evener/update/apply: copyExecutable in
// internal/selfupdate writes a fixed dst+".tmp" path, so a second apply
// racing the first would corrupt the binary a restart is about to exec.
// It is deliberately left locked after a successful upgrade schedules a
// restart -- the process is about to be replaced, so a second apply
// afterward must also be refused, not merely serialized.
var hubUpdateMu sync.Mutex

// tryLockHubUpdate acquires hubUpdateMu for the two RPCs that can install a
// new hub binary (evener/update/apply and evener/upgrade), returning the
// shared "already in progress" error when the other one already holds it.
func tryLockHubUpdate() error {
	if !hubUpdateMu.TryLock() {
		return errHubUpdateInProgress
	}
	return nil
}

// The refusals the self-update RPCs give before installing anything.
var (
	errHubUpdateInProgress       = errors.New("a hub update is already in progress")
	errDevBuildUpdate            = errors.New("this hub is a dev build; rebuild with make build-hub instead of self-updating")
	errInPlaceRestartUnsupported = errors.New("in-place restart is not supported on this platform; use evener/upgrade and restart manually")
)

// isDevBuild reports whether this hub was built without a release channel
// (a worktree build). Such a hub is never self-updated: replacing it with a
// release binary would silently discard whatever the developer is running.
func isDevBuild() bool { return buildinfo.BuildChannel() == "dev" }

// validateUpdateChannel resolves requested (defaulting to the build's own
// upgrade channel) and rejects anything but "release" or "snapshot" --
// unlike selfupdate.Upgrade's ResolveTarget, which also accepts "current",
// "latest", and arbitrary "v*" tags. Applying an arbitrary release tag would
// let evener/update/apply install and exec whatever the caller names.
func validateUpdateChannel(requested string) (string, error) {
	ch := envvars.FirstNonEmpty(requested, buildinfo.UpgradeChannel())
	switch ch {
	case "release", "snapshot":
		return ch, nil
	default:
		return "", fmt.Errorf("%w %q", errUnknownUpdateChannel, ch)
	}
}

// errUnknownUpdateChannel refuses an update channel other than release or
// snapshot.
var errUnknownUpdateChannel = errors.New("unknown update channel")

// hubUpdateCheck answers evener/update/check: compare the running build to
// the channel's current commit. Dev builds answer locally.
func hubUpdateCheck(ctx context.Context, params appwire.UpdateCheckParams) (appwire.UpdateCheckResponse, error) {
	channel, err := validateUpdateChannel(params.Channel)
	if err != nil {
		return appwire.UpdateCheckResponse{}, err
	}
	resp := appwire.UpdateCheckResponse{
		Channel:        channel,
		BuildChannel:   buildinfo.BuildChannel(),
		CurrentVersion: buildinfo.Version(),
		CurrentCommit:  buildinfo.GitSHA,
	}
	onDiskCommit, onDiskErr := probeLaunchBuild()
	resp.OnDiskCommit = onDiskCommit
	if onDiskErr != nil {
		resp.OnDiskError = onDiskErr.Error()
	}
	resp.StartedAt = hubProcessStartedAt
	resp.RestartSupported = hubRestartUsable() && onDiskErr == nil
	resp.InstallTarget = expectedInstallTarget()
	_, installBinDir := expectedInstallDirs()
	resp.InstallTargetMatchesLaunch = installTargetsLaunch(resp.InstallTarget, hubLaunchEntrypoint, installBinDir)
	binaryChanged := false
	if hubLaunchBinarySHA256 != "" {
		if digest, err := launchBinaryDigest(hubLaunchEntrypoint); err == nil {
			binaryChanged = digest != hubLaunchBinarySHA256
		}
	}
	resp.RestartPending = onDiskCommit != "" && (onDiskCommit != resp.CurrentVersion || binaryChanged)
	if isDevBuild() {
		resp.InstallAvailable = false
		return resp, nil
	}
	result, err := runHubUpdateCheck(ctx, selfupdate.CheckOptions{
		Channel:    resp.Channel,
		CurrentSHA: buildinfo.GitSHA,
	})
	if err != nil {
		// A GitHub outage must not hide the local restart state: the clients
		// still need the on-disk build, the incarnation, and whether restart is
		// supported. Report the channel failure beside those facts.
		resp.CheckError = err.Error()
		resp.Applicable = true
		resp.UpdateAvailable = false
		resp.InstallAvailable = false
		return resp, nil //nolint:nilerr // the channel failure is reported in the response
	}
	resp.LatestTag = result.LatestTag
	resp.LatestCommit = result.LatestCommit
	resp.UpdateAvailable = result.UpdateAvailable
	resp.Applicable = true
	resp.InstallAvailable = result.LatestCommit != "" &&
		onDiskCommit != "" &&
		!selfupdate.CommitIsCurrent(result.LatestCommit, commitToken(onDiskCommit)) &&
		resp.InstallTargetMatchesLaunch
	return resp, nil
}

// expectedInstallDirs is the managed layout selfupdate.Upgrade will write for
// this hub's launch path. An unrecognized launch path falls back to the CLI's
// default ~/.local prefix.
func expectedInstallDirs() (shareBinDir, binDir string) {
	_, binDir, shareBinDir = hubInstallDirs()
	if shareBinDir != "" {
		return shareBinDir, binDir
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", ""
	}
	return filepath.Join(home, ".local", "share", "evener", "bin"), filepath.Join(home, ".local", "bin")
}

// expectedInstallTarget is where selfupdate.Upgrade will write the evener
// binary for this hub's launch layout.
func expectedInstallTarget() string {
	shareBinDir, _ := expectedInstallDirs()
	if shareBinDir == "" {
		return ""
	}
	return filepath.Join(shareBinDir, "evener")
}

// installTargetsLaunch reports whether Upgrade updates the binary this hub
// launched from. A launch through <prefix>/bin/evener may be a symlink to the
// managed binary, or a copy or hardlink, which Upgrade replaces with that
// symlink on install. The share binary may not exist before the first
// install, so the entrypoint directory is compared as well.
func installTargetsLaunch(target, launch, binDir string) bool {
	if sameInstallTarget(target, launch) {
		return true
	}
	if target == "" || launch == "" || binDir == "" {
		return false
	}
	if filepath.Base(launch) != filepath.Base(target) {
		return false
	}
	return sameDirectory(filepath.Dir(launch), binDir)
}

func sameDirectory(a, b string) bool {
	resolvedA, err := filepath.EvalSymlinks(a)
	if err != nil {
		return false
	}
	resolvedB, err := filepath.EvalSymlinks(b)
	if err != nil {
		return false
	}
	return resolvedA == resolvedB
}

func sameInstallTarget(target, launch string) bool {
	if target == "" || launch == "" {
		return false
	}
	resolvedTarget, err := filepath.EvalSymlinks(target)
	if err != nil {
		return false
	}
	resolvedLaunch, err := filepath.EvalSymlinks(launch)
	if err != nil {
		return false
	}
	return resolvedTarget == resolvedLaunch
}

// commitToken strips a dirty suffix for commit comparison, preserving the
// distinction between clean and dirty builds elsewhere.
func commitToken(version string) string {
	return strings.TrimSuffix(version, "-dirty")
}

// probeLaunchBuild reads the on-disk build at the recorded launch path. The
// probe is bounded by probeHubBinary's deadline.
func probeLaunchBuild() (string, error) {
	if errHubLaunchEntrypoint != nil {
		return "", errHubLaunchEntrypoint
	}
	if hubLaunchEntrypoint == "" {
		return "", errHubRestartLaunchPath
	}
	if _, err := regularLaunchTarget(hubLaunchEntrypoint); err != nil {
		return "", err
	}
	return probeHubBinary(hubLaunchEntrypoint)
}

// hubRestartUsable reports whether a restart could be attempted at all.
func hubRestartUsable() bool {
	return hubRestartSupported() && errHubLaunchEntrypoint == nil && hubLaunchEntrypoint != ""
}

// hubUpdateApply answers evener/update/apply: install the channel's build,
// then exec it in place once this response has gone out.
func hubUpdateApply(ctx context.Context, params appwire.UpdateApplyParams) (appwire.UpdateApplyResponse, error) {
	if isDevBuild() {
		return appwire.UpdateApplyResponse{}, errDevBuildUpdate
	}
	channel, err := validateUpdateChannel(params.Channel)
	if err != nil {
		return appwire.UpdateApplyResponse{}, err
	}
	// Fail fast where the in-place restart cannot run: without this the
	// apply would download and install, report Restarting:true, and only
	// then fail in the post-response goroutine -- leaving the frontend to
	// poll a hub that never restarts. hubUpgrade (no exec) stays allowed.
	if !hubRestartSupported() {
		return appwire.UpdateApplyResponse{}, errInPlaceRestartUnsupported
	}
	// Re-check server-side before installing: the store refuses stale
	// checks, but a direct RPC (or a check that went stale in flight)
	// would otherwise download, reinstall, and exec an identical build --
	// and the frontend poll keys on a version change, so a no-op restart
	// ends in a misleading restart-timeout. Compose through hubUpdateCheck
	// (the typed handler) so the option-build and result interpretation
	// stay in one place. Runs before the lock: there is nothing to
	// serialize when no install follows.
	fresh, err := hubUpdateCheck(ctx, appwire.UpdateCheckParams{Channel: channel})
	if err != nil {
		return appwire.UpdateApplyResponse{}, err
	}
	if fresh.CheckError != "" {
		return appwire.UpdateApplyResponse{}, errors.New(fresh.CheckError)
	}
	if !fresh.UpdateAvailable {
		return appwire.UpdateApplyResponse{
			Release:    fresh.LatestTag,
			Channel:    fresh.Channel,
			Restarting: false,
		}, nil
	}
	if err := tryLockHubUpdate(); err != nil {
		return appwire.UpdateApplyResponse{}, err
	}
	unlockOnReturn := true
	defer func() {
		if unlockOnReturn {
			hubUpdateMu.Unlock()
		}
	}()

	prefix, binDir, shareBinDir := hubInstallDirs()
	result, err := runHubSelfUpgradeWithTimeout(ctx, selfupdate.Options{
		Requested:      channel,
		CurrentChannel: buildinfo.UpgradeChannel(),
		Prefix:         prefix,
		BinDir:         binDir,
		ShareBinDir:    shareBinDir,
	})
	if err != nil {
		return appwire.UpdateApplyResponse{}, err
	}
	binary, err := evenerBinaryFrom(channel, result.Installed)
	if err != nil {
		return appwire.UpdateApplyResponse{}, err
	}
	// Pin the installed binary to the digest Upgrade computed while the
	// install lock was held: the lock releases at Upgrade return but the
	// exec runs ~500ms later after the response flush, and a concurrent
	// installer could swap the binary in between. The restart goroutine
	// re-verifies and aborts rather than exec'ing a different release
	// than reported. A missing digest (or unreadable binary) aborts the
	// apply: silently skipping verification would exec an unverified
	// binary, and fictional stub paths must not reach a real restart.
	entry := restartBinary(binary)
	digest, ok := result.InstalledSHA256[binary]
	if !ok || digest == "" {
		return appwire.UpdateApplyResponse{}, fmt.Errorf("upgrade to %s reported no digest for %s; refusing to restart unverified", channel, binary)
	}
	pin := restartPin{path: entry, sha256Hex: digest, shareBinDir: result.ShareBinDir}
	if _, err := os.Stat(entry); err != nil {
		return appwire.UpdateApplyResponse{}, err
	}
	// The process is about to be replaced: leave hubUpdateMu held so a
	// second apply after this one is also refused.
	unlockOnReturn = false
	scheduleHubRestart(ctx, pin, entry, hubRestartArgs(hubProcessArgs()))
	return appwire.UpdateApplyResponse{
		Release:    result.Release,
		Channel:    result.Channel,
		Installed:  result.Installed,
		Restarting: true,
	}, nil
}

// runHubSelfUpgradeWithTimeout runs one upgrade operation under the
// overall hubUpgradeTimeout deadline, so stacked per-request timeouts can
// never push a valid update past what the frontend waits for.
func runHubSelfUpgradeWithTimeout(ctx context.Context, opts selfupdate.Options) (selfupdate.Result, error) {
	ctx, cancel := context.WithTimeout(ctx, hubUpgradeTimeout)
	defer cancel()
	return runHubSelfUpgrade(ctx, opts)
}

// hubInstallDirs derives the install layout for a self-update from the
// running hub binary, so a hub installed under /usr/local (or any other
// prefix) upgrades that installation instead of the ~/.local default.
// It prefers the INVOCATION path (argv[0]): on Linux os.Executable
// pre-resolves /proc/self/exe to the share target, making a custom-BINDIR
// symlink invisible, while argv[0] still names the launched entrypoint.
// Falls back to os.Executable when argv is empty. Unknown layouts
// (worktree builds, ad-hoc paths) return empty strings and
// selfupdate.Upgrade falls back to its own defaults.
func hubInstallDirs() (prefix, binDir, shareBinDir string) {
	if args := hubProcessArgs(); len(args) > 0 && args[0] != "" {
		if prefix, binDir, shareBinDir := selfupdate.InstallDirsFromExecutable(canonicalInvocationPath(args[0])); prefix != "" || binDir != "" {
			return prefix, binDir, shareBinDir
		}
	}
	exe, err := hubExecutable()
	if err != nil || exe == "" {
		return "", "", ""
	}
	return selfupdate.InstallDirsFromExecutable(exe)
}

// canonicalInvocationPath normalizes argv[0] for layout derivation: a bare
// name (`evener`, as launched via PATH) resolves through exec.LookPath, and
// a relative path absolutizes against the working directory -- so symlink
// resolution and layout comparisons below always run on a canonical path
// instead of comparing relative against absolute inconsistently.
func canonicalInvocationPath(argv0 string) string {
	if !strings.Contains(argv0, string(filepath.Separator)) {
		if resolved, err := exec.LookPath(argv0); err == nil {
			argv0 = resolved
		}
	}
	if abs, err := filepath.Abs(argv0); err == nil {
		argv0 = abs
	}
	return argv0
}

// hubRestartArgs returns argv[1:] for the exec-in-place restart, guarding
// the empty-argv case the same way currentExecutable does: slicing an
// empty slice panics, and a hub launched with no args restarts bare.
var hubBoundAddr string
var hubBindWasEphemeral bool

// isEphemeralAddr reports a listen address that asks the OS for a port: an
// empty port or port 0, for a host name, IPv4, or bracketed IPv6.
func isEphemeralAddr(addr string) bool {
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	return port == "" || port == "0"
}

func hubRestartArgs(args []string) []string {
	if len(args) == 0 {
		return nil
	}
	out := append([]string(nil), args[1:]...)
	if hubBoundAddr == "" {
		return out
	}
	// A hub started with an ephemeral port (":0") must restart on the port it
	// bound, or every reconnecting client keeps dialing the old one.
	rewrote := false
	for i, arg := range out {
		switch {
		case (arg == "-addr" || arg == "--addr") && i+1 < len(out):
			if isEphemeralAddr(out[i+1]) {
				out[i+1] = hubBoundAddr
				rewrote = true
			}
		case strings.HasPrefix(arg, "-addr=") && isEphemeralAddr(strings.TrimPrefix(arg, "-addr=")):
			out[i] = "-addr=" + hubBoundAddr
			rewrote = true
		case strings.HasPrefix(arg, "--addr=") && isEphemeralAddr(strings.TrimPrefix(arg, "--addr=")):
			out[i] = "--addr=" + hubBoundAddr
			rewrote = true
		}
	}
	if !rewrote && hubBindWasEphemeral {
		// The ephemeral address came from configuration, so there is no
		// argument to rewrite; supply the bound one explicitly.
		out = append(out, "-addr", hubBoundAddr)
	}
	return out
}

// restartBinary picks the path to exec for the in-place restart: the
// canonical original argv[0] entrypoint when it resolves to the newly
// installed binary, else the installed path itself. Exec'ing the
// entrypoint preserves a custom BINDIR across restarts -- after the first
// update the process image is the new binary either way, but argv[0] as
// the user launched it keeps the NEXT update's derivation anchored to
// the configured symlink dir instead of the standard <prefix>/bin.
func restartBinary(installed string) string {
	if args := hubProcessArgs(); len(args) > 0 && args[0] != "" {
		entry := canonicalInvocationPath(args[0])
		if resolved, err := filepath.EvalSymlinks(entry); err == nil {
			installedResolved, installedErr := filepath.EvalSymlinks(installed)
			if installedErr == nil && resolved == installedResolved {
				return entry
			}
		}
	}
	return installed
}

// restartPin identifies one installed binary across the lock-release to
// exec window: the exec path plus the SHA-256 of its bytes as committed
// under the install lock. (Not an fd: Go opens O_CLOEXEC, so a held lock
// drops at exec.) The restart goroutine re-verifies immediately before
// Exec and aborts loudly instead of exec'ing a concurrent installer's
// release.
type restartPin struct {
	path        string
	sha256Hex   string
	shareBinDir string
}

// pinInstalledBinary records the identity of the just-installed binary.
func pinInstalledBinary(path string) (restartPin, error) {
	f, err := openInstalledRead(path)
	if err != nil {
		return restartPin{}, err
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return restartPin{}, err
	}
	if !info.Mode().IsRegular() {
		return restartPin{}, errors.New("installed binary is not a regular file")
	}
	data, err := io.ReadAll(f)
	if err != nil {
		return restartPin{}, err
	}
	sum := sha256.Sum256(data)
	return restartPin{path: path, sha256Hex: hex.EncodeToString(sum[:])}, nil
}

// verifyPinnedBinary reports whether path still holds the pinned bytes.
// Always verifies: there is no skip path, so a pinning failure aborts
// the restart instead of silently disabling the check.
func verifyPinnedBinary(path string, pin restartPin) error {
	got, err := pinInstalledBinary(path)
	if err != nil {
		return err
	}
	if got.sha256Hex != pin.sha256Hex {
		return errors.New("installed binary changed since upgrade; refusing to exec a different release")
	}
	return nil
}

// evenerBinaryFrom returns the installed path of the evener binary in an
// upgrade's Installed list, found by name rather than by position.
func evenerBinaryFrom(channel string, installed []string) (string, error) {
	for _, path := range installed {
		if filepath.Base(path) == "evener" {
			return path, nil
		}
	}
	return "", fmt.Errorf("upgrade to %s installed no evener binary", channel)
}

// scheduleHubRestartAfterResponse execs binary with the hub's own arguments
// once the apply response has been written to the websocket -- the frontend
// needs that success result to start its health poll, so replacing the
// process image before the frame goes out would strand the page. When there
// is no frame to wait for -- ctx carries no appserver connection (a direct
// call in a test, a non-websocket caller), or the socket died during the
// install and the send loop has already stopped -- the restart runs
// immediately, because a hub that never restarts also never releases
// hubUpdateMu.
// hubUpdateMu is held across the exec attempt (see its doc comment); on
// failure the old hub keeps running, so the lock is released here too, or
// every later apply/upgrade would be refused forever.
func scheduleHubRestartAfterResponse(ctx context.Context, pin restartPin, binary string, args []string) {
	scheduleRestartAfterResponse(ctx, pin, binary, args, "self-update", true)
}

// scheduleRestartAfterResponse execs binary once the response has been
// written. useInstallLock is false for an ad-hoc or worktree launch path where
// no managed install directory exists; the pinned digest is still re-verified
// immediately before Exec.
func scheduleRestartAfterResponse(ctx context.Context, pin restartPin, binary string, args []string, label string, useInstallLock bool) {
	restart := func() {
		go func() {
			time.Sleep(hubRestartDelay)
			_, _ = fmt.Fprintf(hubUpdateStderr, "[hub] %s: restarting as %s\n", label, binary)
			abort := func(format string, args ...any) {
				hubUpdateMu.Unlock()
				_, _ = fmt.Fprintf(hubUpdateStderr, "[hub] "+label+": "+format+"\n", args...)
			}
			// Re-acquire the cross-process install lock for the
			// verify+exec critical section: the install lock released
			// at Upgrade return, and a concurrent installer could swap
			// the binary between a bare re-verify and Exec. The fd is
			// O_CLOEXEC so a successful Exec drops the lock exactly as
			// the new image takes over; on failure release it here.
			// A short timeout: a stuck holder must abort the restart,
			// not strand the hub past its health-poll window. The bound
			// stays well under the frontend's 30s restart poll (plus the
			// post-response delay above) so a contended-but-successful
			// restart still lands before the UI declares a timeout.
			release := func() {}
			if useInstallLock {
				relockCtx, cancel := context.WithTimeout(context.Background(), hubRestartLockTimeout)
				var err error
				release, err = selfupdate.AcquireInstallLockForVerify(relockCtx, pin.shareBinDir)
				cancel()
				if err != nil {
					if label == "restart" && errors.Is(err, selfupdate.ErrInstallDirReadOnly) {
						// The hub may run from a root-owned installation it can
						// read and exec but not write, so no lock file can
						// exist for another installer to hold. The digest
						// re-verify below still catches a changed target.
						// Contention, deadline expiry, and other errors abort:
						// a live installer could swap the binary after the
						// verify and before the exec.
						release = func() {}
						_, _ = fmt.Fprintf(hubUpdateStderr, "[hub] restart: install lock unavailable (%v); verifying the digest without it\n", err)
					} else {
						abort("restart aborted: install lock unavailable: %v", err)
						return
					}
				}
			}
			// The install lock released at Upgrade return; re-verify the
			// binary is still the one just installed before exec'ing --
			// a concurrent installer may have swapped it meanwhile.
			// Abort loudly (old hub keeps running, lock released) rather
			// than exec a different release than the response reported.
			if err := verifyPinnedBinary(binary, pin); err != nil {
				release()
				abort("restart aborted: %v", err)
				return
			}
			if err := execHubBinary(binary, args); err != nil {
				release()
				abort("restart failed, still running the previous binary: %v", err)
			}
			// On success Exec replaces the image (dropping the O_CLOEXEC
			// lock fd); release() is intentionally not called.
		}()
	}
	appserver.RunAfterResponseWritten(ctx, restart)
}
