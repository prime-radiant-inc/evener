package hub

// evener/host/running (deploy pipeline 08b §6 step 2, §10): one hub's own
// running build and authoritative health, served locally by every hub to the
// controller that probes it over an attached session. The plan probe reaches it
// through sshManager.ChannelIfAttached over the live bridge channel — never a
// dial, never the one-shot preflight — presenting the durable probe epoch the
// plan persisted first.
//
// Direction-scoped admission: unlike every other evener/host/* handler, which
// is controller-local and refuses a remote origin, this one is admitted only
// over the attached session the bridge presents — the controller-originated
// probe — and refuses a browser-origin or forwarded request. It never forwards
// onward to a third hub: the answer is this process's own state.
//
// EPOCH ADMISSION. The serving hub validates the presented epoch against the
// guard epoch it last admitted, refuses a stale same-boot epoch without
// persisting it or probing, persists the admitted epoch, then runs the write
// probe authorized by that epoch under the caller's host gate. The
// crash-fencing execution that once sat behind this boundary (the remote
// guard-file compare-and-advance and the takeover/kill/wait steps) was removed
// with the rest of the program (comp08).

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/buildinfo"
	"primeradiant.com/evener/cmd/evener-hub/internal/fsdurability"
	"primeradiant.com/evener/cmd/evener-hub/internal/hostops"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/internal/appserver"
)

// hostStateRootProbePrefix names the state-root write probe's temp and target
// files. Every probe file carries it, so boot can prune the strays a crash
// between the rename and the remove leaves behind without ever touching an
// operator file (§10: "a probe temp orphaned by a crash carries the probe-name
// prefix and boot prunes prefix-matching strays before serving").
const hostStateRootProbePrefix = ".evener-host-running-probe-"

// hostRunningConfig is everything the running handler answers from: the build
// identity this process serves, the health predicate's inputs, and the durable
// store the presented fencing epoch is persisted in.
type hostRunningConfig struct {
	// buildRevision is this hub's own build revision, from the same source as
	// the controllerBuild plan input (buildinfo.Version()).
	buildRevision string
	// processStart is this hub's own process start instant; its zero value
	// leaves HostRunningResponse.processStartTime absent.
	processStart time.Time
	// stateRoots are the durable state roots the health predicate checks: free
	// space first, then the write probe.
	stateRoots []string
	// minFreeSpaceBytes is the owner-set minimum-free-space knob.
	minFreeSpaceBytes int64
	// roster is the hub's local controller roster, the direct local input to
	// the restart-required predicate — never the authenticated-probe path.
	roster *hubcore.Roster
	// freeSpace and writeProbe are the health predicate's seams; nil uses the
	// production implementations.
	freeSpace  func(path string) (uint64, error)
	writeProbe func(dir string) error
}

// registerRunningHandler installs evener/host/running on the host surface. It
// is called beside the add/list/status/remove/update/plan registrations, so
// every hub — the controller and each remote host hub alike — serves it.
func (m *hubHostManager) registerRunningHandler(server *appserver.Server) {
	appserver.HandleTyped(server.Router(), appwire.MethodEvenerHostRunning, m.HostRunning)
}

// HostRunning implements evener/host/running. The order is §10's:
//
//  1. admit only over the attached session the probe arrived on — a
//     browser-origin or forwarded request is refused before anything is read;
//  2. require the presented fencing epoch: absent or malformed is a typed
//     `probe-failed` refusal ("no epoch presented"), never an unfenced write;
//  3. persist the presented epoch (refusing a stale one without probing);
//  4. answer this hub's own revision, its authoritative health, and its process
//     start time when known.
//
// The health flag is data, never a probe failure: a forced-false condition
// (restart-required outstanding, free space below the knob, an unwritable state
// root) still answers the full response with `healthy: false`.
func (m *hubHostManager) HostRunning(ctx context.Context, params appwire.HostRunningParams) (appwire.HostRunningResponse, error) {
	if err := guardAttachedProbeSession(ctx); err != nil {
		return appwire.HostRunningResponse{}, err
	}
	// The admission-plus-probe window is serialized on this hub: the guard row
	// is hub-wide (v1 admits one calling controller), so a second call must not
	// advance the admitted epoch while the first is still probing with the
	// epoch it admitted. This mutex is the admitted posture: the per-host remote
	// lease and guard-file compare-and-advance that once would have replaced it
	// were withdrawn with the crash-fencing program (comp08), and this
	// serialization keeps "persisted before the write half" true for the probe
	// that actually runs.
	m.cfg.runningProbeMu.Lock()
	defer m.cfg.runningProbeMu.Unlock()
	epoch := params.FencingEpoch
	if err := m.admitPresentedEpoch(epoch); err != nil {
		return appwire.HostRunningResponse{}, err
	}
	response := appwire.HostRunningResponse{
		BuildRevision: m.cfg.running.buildRevision,
		Healthy:       m.runningHealthy(),
	}
	if !m.cfg.running.processStart.IsZero() {
		// RFC3339Nano, not RFC3339: this instant is the sole same-clock proof
		// that a restart replaced the process (deploy pipeline 08b §6's
		// process-instance verification, §10's absent-when-unknown field), and a
		// second-resolution string makes two incarnations started within the same
		// second indistinguishable. The Nano form is still RFC3339-conformant, and
		// Go's time.Parse(time.RFC3339, …) accepts its fractional seconds.
		response.ProcessStartTime = m.cfg.running.processStart.UTC().Format(time.RFC3339Nano)
	}
	return response, nil
}

// admitPresentedEpoch validates the epoch the caller presented and persists the
// admitted one before the probe's write half runs (§10: a stale same-boot epoch
// is refused without persisting or probing, and only an admitted epoch is
// written). An absent or malformed epoch — what a defaulted or omitted wire
// field decodes to — is refused with the typed `probe-failed` family before
// anything is persisted, and a stale epoch is refused without probing.
//
// A hub with no operation store cannot persist the epoch, so it refuses rather
// than authorizing an unfenced write; the same is true of a store write that
// fails.
func (m *hubHostManager) admitPresentedEpoch(epoch appwire.FencingEpoch) error {
	if epoch.BootID == "" || epoch.OpSeq == 0 {
		return appwire.Unavailable("evener/host/running: no epoch presented: a fencing epoch (bootId, opSeq) is required and this call carried none")
	}
	if m.cfg.ops == nil {
		return appwire.Unavailable("evener/host/running: no operation store is configured, so the presented fencing epoch cannot be persisted; refusing rather than authorizing an unfenced write")
	}
	err := m.cfg.ops.AdmitGuardEpoch(hostops.GuardEpoch{BootID: epoch.BootID, OpSeq: epoch.OpSeq})
	switch {
	case err == nil:
		return nil
	case errors.Is(err, hostops.ErrStaleGuardEpoch):
		return appwire.Unavailable(fmt.Sprintf("evener/host/running: stale fencing epoch refused without probing: %v", err))
	case hostops.RenameLanded(err):
		// The rename landed and the store adopted the row, so the guard epoch is
		// durable and memory agrees — only its directory sync failed. Same
		// posture as the plan's probe-epoch persist and the mint's token write
		// (both branch on RenameLanded): admit and log, never refuse a host
		// whose durable state already holds the presented epoch.
		m.logf("evener/host/running: the guard epoch's directory sync failed after its rename landed; the epoch is durable: %v", err)
		return nil
	default:
		return appwire.Unavailable(fmt.Sprintf("evener/host/running: the presented fencing epoch could not be persisted: %v", err))
	}
}

// runningHealthy is §10's authoritative health predicate, and nothing else
// feeds it: (1) liveness — the handler runs on the live request path, so
// reaching it proves the serving process is serving; (2) no restart-required
// condition outstanding under this hub's dedicated local predicate, evaluated
// from the local controller roster directly, never through the
// restartRequiredDaemon authenticated-probe path; (3) the durable state roots
// are writable and not critically full — the free-space query first, and only
// above the owner-set minimum-free-space knob the state-root write probe.
//
// Everything else — session counts, load, peer reachability — is deliberately
// not an input.
func (m *hubHostManager) runningHealthy() bool {
	if m.localRestartRequiredOutstanding() {
		return false
	}
	for _, root := range m.cfg.running.stateRoots {
		free, err := m.cfg.running.freeSpaceFor(root)
		if err != nil {
			// An unanswerable free-space query cannot prove the root is not
			// critically full, so it fails closed.
			return false
		}
		if free < uint64(m.cfg.running.minFreeSpaceBytes) {
			return false
		}
		if err := m.cfg.running.writeProbeIn(root); err != nil {
			return false
		}
	}
	return true
}

// localRestartRequiredOutstanding reports whether the hub's local roster holds
// a live daemon that needs a restart (an incompatible daemon: thread status
// restart-required). It reads the roster's local snapshot directly — the
// dedicated local predicate §10 requires — and never the authenticated-probe
// path (restartRequiredDaemon), which dials and verifies ownership.
func (m *hubHostManager) localRestartRequiredOutstanding() bool {
	if m.cfg.running.roster == nil {
		return false
	}
	return m.cfg.running.roster.RestartRequired()
}

// freeSpaceFor runs the free-space query for one state root.
func (c hostRunningConfig) freeSpaceFor(root string) (uint64, error) {
	if c.freeSpace != nil {
		return c.freeSpace(root)
	}
	return hostStateRootFreeSpace(root)
}

// writeProbeIn runs the write probe for one state root.
func (c hostRunningConfig) writeProbeIn(root string) error {
	if c.writeProbe != nil {
		return c.writeProbe(root)
	}
	return hostStateRootWriteProbe(root)
}

// hostStateRootWriteProbe is §10's write probe: a real atomic temp-plus-rename
// probe inside the state dir, with a uniquely named temp per probe, renamed to
// a distinct probe target in the same dir, the dir fsynced, then the target
// removed. No probe temp survives the probe window past its remove except a
// crash orphan the boot prune owns.
//
// The complete fencing protocol an earlier revision anticipated before this
// write half — lease takeover, bounded kill/wait, guard advance — was withdrawn
// with the crash-fencing program (comp08). This probe is authorized by the
// caller's presented epoch, which the serving hub admitted before calling this
// function, and serialized by the caller's host gate.
func hostStateRootWriteProbe(dir string) error {
	suffix, err := probeNonce()
	if err != nil {
		return err
	}
	temp := filepath.Join(dir, hostStateRootProbePrefix+"tmp-"+suffix)
	target := filepath.Join(dir, hostStateRootProbePrefix+"target-"+suffix)
	file, err := os.OpenFile(temp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("open probe temp in %s: %w", dir, err)
	}
	cleanup := func() {
		_ = os.Remove(temp)
		_ = os.Remove(target)
	}
	if _, err := file.WriteString("evener state-root write probe\n"); err != nil {
		_ = file.Close()
		cleanup()
		return fmt.Errorf("write probe temp in %s: %w", dir, err)
	}
	if err := file.Sync(); err != nil && !fsdurability.SyncUnsupported(err) {
		_ = file.Close()
		cleanup()
		return fmt.Errorf("sync probe temp in %s: %w", dir, err)
	}
	if err := file.Close(); err != nil {
		cleanup()
		return fmt.Errorf("close probe temp in %s: %w", dir, err)
	}
	if err := os.Rename(temp, target); err != nil {
		cleanup()
		return fmt.Errorf("rename probe in %s: %w", dir, err)
	}
	if err := syncProbeDir(dir); err != nil {
		cleanup()
		return fmt.Errorf("sync probe directory %s: %w", dir, err)
	}
	if err := os.Remove(target); err != nil {
		cleanup()
		return fmt.Errorf("remove probe target in %s: %w", dir, err)
	}
	return nil
}

// syncProbeDir fsyncs the directory the probe's rename landed in, tolerating
// filesystems that cannot sync a directory (the same tolerance the hub's other
// durable stores share).
func syncProbeDir(dir string) error {
	handle, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer func() { _ = handle.Close() }()
	if err := handle.Sync(); err != nil && !fsdurability.SyncUnsupported(err) {
		return err
	}
	return nil
}

// probeNonce renders one probe file name's random suffix.
func probeNonce() (string, error) {
	var raw [8]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("draw probe nonce: %w", err)
	}
	return hex.EncodeToString(raw[:]), nil
}

// pruneHostRunningProbeStrays removes the probe temp/target files a crash
// between the rename and the remove left behind, and reports how many it
// removed, for the boot log. It matches only the probe prefix, so no operator
// file is ever touched.
func pruneHostRunningProbeStrays(dir string) int {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}
	removed := 0
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasPrefix(entry.Name(), hostStateRootProbePrefix) {
			continue
		}
		if err := os.Remove(filepath.Join(dir, entry.Name())); err == nil {
			removed++
		}
	}
	return removed
}

// runningStateRoots is the set of durable state roots the running-health
// predicate checks: the hub's machine state root and the projects state root,
// each once, skipping unset roots.
func runningStateRoots(hubStateRoot, stateDir string) []string {
	var roots []string
	for _, root := range []string{strings.TrimSpace(hubStateRoot), strings.TrimSpace(stateDir)} {
		if root == "" {
			continue
		}
		if !slices.Contains(roots, root) {
			roots = append(roots, root)
		}
	}
	return roots
}

// newHostRunningConfig builds the running handler's config from the hub's web
// configuration, applying the documented knob defaults.
func newHostRunningConfig(cfg hubcore.WebConfig) hostRunningConfig {
	minFree := cfg.HostMinFreeSpaceBytes
	if minFree <= 0 {
		minFree = DefaultHostMinFreeSpaceBytes
	}
	return hostRunningConfig{
		buildRevision:     buildinfo.Version(),
		processStart:      cfg.HubProcessStart,
		roster:            cfg.Roster,
		stateRoots:        runningStateRoots(cfg.HubStateRoot, cfg.StateDir),
		minFreeSpaceBytes: minFree,
	}
}
