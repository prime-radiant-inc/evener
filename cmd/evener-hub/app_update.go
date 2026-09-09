package hub

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
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
	runHubUpdateCheck            = selfupdate.Check
	scheduleHubRestart           = scheduleHubRestartAfterResponse
	execHubBinary                = selfupdate.Restart
	hubUpdateStderr    io.Writer = os.Stderr
)

// hubRestartDelay is a short grace period for the kernel to flush the apply
// response frame the send loop has just written; the restart itself already
// waits for that write (see scheduleHubRestartAfterResponse). A var, not a
// const, so tests can set it to 0.
var hubRestartDelay = 500 * time.Millisecond

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
		return errors.New("a hub update is already in progress")
	}
	return nil
}

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
		return "", fmt.Errorf("unknown update channel %q", ch)
	}
}

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
	if isDevBuild() {
		return resp, nil
	}
	result, err := runHubUpdateCheck(ctx, selfupdate.CheckOptions{
		Channel:    resp.Channel,
		CurrentSHA: buildinfo.GitSHA,
	})
	if err != nil {
		return appwire.UpdateCheckResponse{}, err
	}
	resp.LatestTag = result.LatestTag
	resp.LatestCommit = result.LatestCommit
	resp.UpdateAvailable = result.UpdateAvailable
	resp.Applicable = true
	return resp, nil
}

// hubUpdateApply answers evener/update/apply: install the channel's build,
// then exec it in place once this response has gone out.
func hubUpdateApply(ctx context.Context, params appwire.UpdateApplyParams) (appwire.UpdateApplyResponse, error) {
	if isDevBuild() {
		return appwire.UpdateApplyResponse{}, errors.New("this hub is a dev build; rebuild with make build-hub instead of self-updating")
	}
	channel, err := validateUpdateChannel(params.Channel)
	if err != nil {
		return appwire.UpdateApplyResponse{}, err
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
func hubRestartArgs(args []string) []string {
	if len(args) == 0 {
		return nil
	}
	return args[1:]
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
		if resolved, err := filepath.EvalSymlinks(entry); err == nil && resolved == installed {
			return entry
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
	data, err := os.ReadFile(path)
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

// result. installExtractedBinaries also installs "evener-dev" alongside it,
// so the entry to exec into can't be assumed to be Installed[0].
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
	restart := func() {
		go func() {
			time.Sleep(hubRestartDelay)
			_, _ = fmt.Fprintf(hubUpdateStderr, "[hub] self-update: restarting as %s\n", binary)
			abort := func(format string, args ...any) {
				hubUpdateMu.Unlock()
				_, _ = fmt.Fprintf(hubUpdateStderr, "[hub] self-update: "+format+"\n", args...)
			}
			// Re-acquire the cross-process install lock for the
			// verify+exec critical section: the install lock released
			// at Upgrade return, and a concurrent installer could swap
			// the binary between a bare re-verify and Exec. The fd is
			// O_CLOEXEC so a successful Exec drops the lock exactly as
			// the new image takes over; on failure release it here.
			// A short timeout: a stuck holder must abort the restart,
			// not strand the hub past its health-poll window.
			relockCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			release, err := selfupdate.AcquireInstallLockForVerify(relockCtx, pin.shareBinDir)
			cancel()
			if err != nil {
				abort("restart aborted: install lock unavailable: %v", err)
				return
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
	if !appserver.AfterResponseWritten(ctx, restart) {
		restart()
	}
}
