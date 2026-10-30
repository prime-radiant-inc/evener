package hub

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/buildinfo"
)

// hubLaunchEntrypoint is the path the process was started through, resolved
// once at startup. Restart uses it instead of resolving argv[0] again, so a
// changed PATH or working directory cannot redirect the exec.
var hubLaunchEntrypoint string
var errHubLaunchEntrypoint error
var hubLaunchShareBinDir string
var hubLaunchBinarySHA256 string

var (
	launchDigestMu   sync.Mutex
	launchDigestInfo os.FileInfo
	launchDigest     string
)

var errHubRestartLaunchPath = errors.New("hub launch path is unavailable")
var errHubProbeTimeout = errors.New("version probe timed out")

// hubProcessStartedAt is the process incarnation every client compares to
// confirm a restart. The health handler reports the same value.
var hubProcessStartedAt = time.Now().UTC()

// scheduleHubRestartOnly is the post-response exec seam. Tests replace it to
// observe the scheduled target without replacing the process.
var scheduleHubRestartOnly = scheduleHubRestartOnlyAfterResponse

// probeHubBinaryFn is the version-probe seam. Tests replace it to model a
// target changing between the pin and the probe.
var probeHubBinaryFn = probeHubBinary

func scheduleHubRestartOnlyAfterResponse(ctx context.Context, pin restartPin, binary string, args []string) {
	scheduleRestartAfterResponse(ctx, pin, binary, args, "restart", pin.shareBinDir != "")
}

// hubProbeTimeout bounds the version probe so a hanging target cannot hold a
// Settings read or a restart request.
var hubProbeTimeout = 2 * time.Second

const hubProbeOutputLimit = 4 << 10

// hubRestart answers evener/hub/restart: exec the binary at the hub's recorded
// launch path in place, after verifying it is a working Evener binary.
func hubRestart(ctx context.Context, _ appwire.HubRestartParams) (appwire.HubRestartResponse, error) {
	if !hubRestartSupported() {
		return appwire.HubRestartResponse{}, errInPlaceRestartUnsupported
	}
	if errHubLaunchEntrypoint != nil {
		return appwire.HubRestartResponse{}, errHubLaunchEntrypoint
	}
	if hubLaunchEntrypoint == "" {
		return appwire.HubRestartResponse{}, errHubRestartLaunchPath
	}
	if _, err := regularLaunchTarget(hubLaunchEntrypoint); err != nil {
		return appwire.HubRestartResponse{}, err
	}
	pin, err := pinInstalledBinary(hubLaunchEntrypoint)
	if err != nil {
		return appwire.HubRestartResponse{}, err
	}
	targetVersion, err := probeHubBinaryFn(hubLaunchEntrypoint)
	if err != nil {
		return appwire.HubRestartResponse{}, err
	}
	// The probe runs the file, so a concurrent build can replace it between
	// the pin and the probe. The response must name the bytes the scheduler
	// will later exec.
	if err := verifyPinnedBinary(hubLaunchEntrypoint, pin); err != nil {
		return appwire.HubRestartResponse{}, err
	}
	pin.shareBinDir = hubLaunchShareBinDir
	if err := tryLockHubUpdate(); err != nil {
		return appwire.HubRestartResponse{}, err
	}
	scheduleHubRestartOnly(ctx, pin, hubLaunchEntrypoint, hubRestartArgs(hubProcessArgs()))
	return appwire.HubRestartResponse{
		Restarting:     true,
		Incarnation:    hubProcessStartedAt,
		CurrentVersion: buildinfo.Version(),
		TargetVersion:  targetVersion,
	}, nil
}

// resolveLaunchEntrypoint records the path the process was started through and
// proves it names the running executable. The returned path preserves a
// symlinked entrypoint so a later restart still launches through it.
func resolveLaunchEntrypoint(args []string, executable func() (string, error)) (string, error) {
	if len(args) == 0 || strings.TrimSpace(args[0]) == "" {
		return "", errors.New("empty argv[0]")
	}
	entry := canonicalInvocationPath(args[0])
	entryInfo, err := os.Stat(entry)
	if err != nil {
		return "", fmt.Errorf("launch path: %w", err)
	}
	if !entryInfo.Mode().IsRegular() {
		return "", errors.New("launch path is not a regular file")
	}
	if entryInfo.Mode().Perm()&0o111 == 0 {
		return "", errors.New("launch path is not executable")
	}
	exe, err := executable()
	if err != nil {
		return "", fmt.Errorf("running executable: %w", err)
	}
	if strings.TrimSpace(exe) == "" {
		return "", errors.New("running executable path is empty")
	}
	exeInfo, err := os.Stat(exe)
	if err != nil {
		return "", fmt.Errorf("running executable: %w", err)
	}
	if !os.SameFile(entryInfo, exeInfo) {
		return "", errors.New("launch path does not name the running executable")
	}
	return entry, nil
}

// regularLaunchTarget refuses a path that is no longer a regular executable
// before any read or exec. A FIFO or device swapped in after startup must not
// block a check or a restart.
func regularLaunchTarget(path string) (os.FileInfo, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("launch path: %w", err)
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("launch path is not a regular file")
	}
	if info.Mode().Perm()&0o111 == 0 {
		return nil, errors.New("launch path is not executable")
	}
	return info, nil
}

// launchBinaryDigest hashes the launch path once per unchanged file. The check
// runs on every Settings load; reading and hashing a large binary each time is
// avoidable when its size and mtime have not moved.
func launchBinaryDigest(path string) (string, error) {
	f, err := openInstalledRead(path)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return "", err
	}
	// Validate the opened descriptor, not an earlier path lookup: a target
	// swapped for a FIFO between the stat and the read must not block here
	// while launchDigestMu is held.
	if !info.Mode().IsRegular() {
		return "", errors.New("launch path is not a regular file")
	}
	launchDigestMu.Lock()
	defer launchDigestMu.Unlock()
	// Compare file identity as well as size and mtime: a replacement through a
	// new inode with the same size and preserved mtime must be rehashed.
	if launchDigestInfo != nil && os.SameFile(launchDigestInfo, info) &&
		launchDigestInfo.Size() == info.Size() && launchDigestInfo.ModTime().Equal(info.ModTime()) {
		return launchDigest, nil
	}
	data, err := io.ReadAll(f)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	launchDigestInfo, launchDigest = info, hex.EncodeToString(sum[:])
	return launchDigest, nil
}

// probeHubBinary runs the target's --version with a deadline, a closed stdin,
// and a bounded combined output, and returns the short commit token. A target
// that does not identify itself as Evener is refused: the hub must not exec an
// arbitrary program that happens to answer --version.
func probeHubBinary(path string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), hubProbeTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, "--version")
	cmd.Stdin = nil
	// A target that forks a child inheriting stdout keeps the copy pipe open
	// after the command is killed. WaitDelay closes it so the probe honors its
	// deadline instead of waiting for the child.
	cmd.WaitDelay = 250 * time.Millisecond
	stdout := &cappedBuffer{limit: hubProbeOutputLimit}
	stderr := &cappedBuffer{limit: hubProbeOutputLimit}
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	err := cmd.Run()
	if ctx.Err() != nil {
		return "", errHubProbeTimeout
	}
	if err != nil {
		return "", fmt.Errorf("version probe: %w", err)
	}
	line := versionLine(stdout.String())
	if line == "" {
		line = versionLine(stderr.String())
	}
	const prefix = "evener "
	if !strings.HasPrefix(line, prefix) {
		return "", errors.New("target did not identify itself as evener")
	}
	fields := strings.Fields(strings.TrimPrefix(line, prefix))
	if len(fields) == 0 {
		return "", errors.New("target reported no version")
	}
	token := fields[0]
	if token == "" {
		return "", errors.New("target reported an empty version")
	}
	return token, nil
}

// versionLine returns the first line when it is an Evener version banner, and
// the empty string otherwise. The probe reads stdout first, then stderr, so a
// target that writes diagnostics to stderr does not have to interleave safely
// with the banner.
func versionLine(out string) string {
	out = strings.TrimSpace(out)
	if i := strings.IndexByte(out, '\n'); i >= 0 {
		out = strings.TrimSpace(out[:i])
	}
	if !strings.HasPrefix(out, "evener ") {
		return ""
	}
	return out
}

// cappedBuffer keeps writes bounded while reporting success, so a target that
// floods stdout cannot grow the probe's memory.
type cappedBuffer struct {
	buf   bytes.Buffer
	limit int
}

func (b *cappedBuffer) Write(p []byte) (int, error) {
	written := len(p)
	if remaining := b.limit - b.buf.Len(); remaining > 0 {
		if len(p) > remaining {
			p = p[:remaining]
		}
		_, _ = b.buf.Write(p)
	}
	return written, nil
}

func (b *cappedBuffer) String() string { return b.buf.String() }
