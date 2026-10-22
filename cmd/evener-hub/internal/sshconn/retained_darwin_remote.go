package sshconn

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"path"
	"slices"
	"strconv"
	"strings"

	"primeradiant.com/evener/cmd/evener-hub/internal/hostreg"
	"primeradiant.com/evener/execsupport/shellquote"
	"primeradiant.com/evener/internal/hubendpoint"
)

// darwinSelectedLockPID observes the selected lock vnode without acquiring it.
// A successful global lsof snapshot is required: lsof <file>'s exit 1 cannot
// distinguish a free file from an observation failure. Even an unlocked open
// descriptor is inconclusive, not permission to overwrite a retained target.
func (m *Manager) darwinSelectedLockPID(ctx context.Context, host hostreg.Host, root string) (string, error) {
	if !path.IsAbs(root) {
		return "", errOwnerEvidence
	}
	stat := "stat -L -f '%d %i %p' " + shellquote.RemoteWord(path.Join(root, "hub.lock"))
	before, err := m.ownerRead(ctx, host, stat)
	if err != nil {
		return "", err
	}
	fields := strings.Fields(string(before))
	if len(fields) != 3 {
		return "", errOwnerEvidence
	}
	dev, err := strconv.ParseUint(fields[0], 10, 64)
	if err != nil {
		return "", errOwnerEvidence
	}
	ino, err := strconv.ParseUint(fields[1], 10, 64)
	if err != nil || ino == 0 {
		return "", errOwnerEvidence
	}
	mode, err := strconv.ParseUint(fields[2], 8, 64)
	if err != nil || mode&0170000 != 0100000 {
		return "", errOwnerEvidence
	}
	out, err := m.ownerRead(ctx, host, "lsof -nP -F pftDik 2>&1")
	if err != nil {
		return "", err
	}
	pid, err := darwinLockSnapshot(out, dev, ino)
	if err != nil {
		return "", err
	}
	after, err := m.ownerRead(ctx, host, stat)
	if err != nil || !bytes.Equal(before, after) {
		return "", errOwnerEvidence
	}
	return pid, nil
}

func darwinLockSnapshot(out []byte, dev, ino uint64) (string, error) {
	var pid, selected, lock, kind string
	var file, observed bool
	var deviceSeen, inodeSeen bool
	var fileDev, fileIno uint64
	finish := func() error {
		if file && (kind == "" || kind == "unknown" || (kind == "REG" && (!deviceSeen || !inodeSeen))) {
			return errOwnerEvidence
		}
		if file && kind == "REG" && fileDev == dev && fileIno == ino {
			// W denotes a whole-file write lock. Any other open descriptor
			// refuses both live continuity and a claim that this root is free.
			if lock != "W" || (selected != "" && selected != pid) {
				return errOwnerEvidence
			}
			selected = pid
		}
		return nil
	}
	for line := range strings.SplitSeq(string(out), "\n") {
		if line == "" {
			continue
		}
		var err error
		switch line[0] {
		case 'p', 'f':
			if err := finish(); err != nil {
				return "", err
			}
			file, fileDev, fileIno, lock = false, 0, 0, ""
			kind, deviceSeen, inodeSeen = "", false, false
			if line[0] == 'p' {
				pid = line[1:]
				if _, err := numericPID(pid); err != nil || pid == "0" {
					return "", errOwnerEvidence
				}
			} else {
				if pid == "" || len(line) == 1 {
					return "", errOwnerEvidence
				}
				file, observed = true, true
			}
		case 'D':
			fileDev, err = strconv.ParseUint(strings.TrimPrefix(line[1:], "0x"), 16, 64)
			deviceSeen = true
		case 'i':
			fileIno, err = strconv.ParseUint(line[1:], 10, 64)
			inodeSeen = true
		case 't':
			kind = line[1:]
		case 'k':
			lock = line[1:]
		default:
			return "", errOwnerEvidence
		}
		if err != nil || (line[0] != 'p' && line[0] != 'f' && !file) {
			return "", errOwnerEvidence
		}
	}
	if err := finish(); err != nil {
		return "", err
	}
	if !observed {
		return "", errOwnerEvidence
	}
	return selected, nil
}

// A retained witness is not authority to write a different Darwin generation.
// The deleted image cannot run its resolver, so bind its still-live process to
// the previously verified record, launch context, lock vnode and birth instead.
func (m *Manager) revalidateRetainedDarwin(ctx context.Context, host hostreg.Host, facts Preflight, prior selectedOwner) error {
	pid, err := m.darwinSelectedLockPID(ctx, host, prior.report.HubStateRoot)
	if err != nil {
		return err
	}
	live := prior.pid != "0"
	var generation string
	if live {
		generation, err = m.darwinGeneration(ctx, host, prior.pid)
		if err != nil {
			out, probeErr := m.ownerRead(ctx, host, processAliveRemote(prior.pid))
			if probeErr != nil {
				return probeErr
			}
			if strings.TrimSpace(string(out)) != pidGoneMarker {
				return err
			}
			live = false
		} else if generation != prior.generation {
			return errOwnerSelectionMismatch
		}
	}
	if (!live && pid != "") || (live && pid != prior.pid) {
		return errOwnerSelectionMismatch
	}
	defs, err := m.launchdOwners(ctx, host, facts, prior.pid)
	if err != nil {
		return err
	}
	if prior.supervisor.kind != supervisorNone {
		def, ok := defs[prior.supervisor]
		expectedPID := "0"
		if live {
			expectedPID = prior.pid
		}
		if !ok || facts.UID != prior.domainUID || def.pid != expectedPID || launchdIntent(def) != prior.definition {
			return errOwnerSelectionMismatch
		}
	} else {
		for _, def := range defs {
			// An ad-hoc witness cannot authorize a newly supervised process.
			if live && def.pid == prior.pid {
				return errOwnerSelectionMismatch
			}
			matches, err := m.launchdOwnerMatches(ctx, host, def, prior.report)
			if err != nil {
				return err
			}
			if matches {
				return errOwnerSelectionMismatch
			}
		}
	}
	if !live {
		return ctx.Err()
	}
	if err := m.checkHubOwner(ctx, host, prior.pid); err != nil {
		return err
	}
	argv, err := m.recoverHubArgvRaw(ctx, host, prior.pid)
	if err != nil || !slices.Equal(argv, prior.argv) {
		return errOwnerSelectionMismatch
	}
	out, err := m.ownerRead(ctx, host, "lsof -a -p "+prior.pid+" -d cwd -Fn")
	if err != nil {
		return err
	}
	var cwd string
	for line := range strings.SplitSeq(string(out), "\n") {
		if strings.HasPrefix(line, "n/") {
			if cwd != "" {
				return errOwnerEvidence
			}
			cwd = line[1:]
		}
	}
	if cwd == "" || cwd != prior.cwdIdentity {
		return errOwnerSelectionMismatch
	}
	out, err = m.ownerRead(ctx, host, darwinEnvironmentRemote(prior.pid))
	if err != nil {
		return err
	}
	env, err := decodeOwnerEnvironment(out)
	if err != nil || !slices.Equal(env, prior.env) {
		return errOwnerSelectionMismatch
	}
	if err := m.retainedDarwinRecord(ctx, host, prior); err != nil {
		return err
	}
	after, err := m.darwinGeneration(ctx, host, prior.pid)
	if err != nil || after != generation {
		return errOwnerSelectionMismatch
	}
	return ctx.Err()
}

func (m *Manager) retainedDarwinRecord(ctx context.Context, host hostreg.Host, prior selectedOwner) error {
	if prior.report.Running == nil {
		return fmt.Errorf("%w: retained Darwin boot identity unavailable", errOwnerEvidence)
	}
	file := shellquote.RemoteWord(path.Join(prior.report.HubStateRoot, "hub-endpoint.json"))
	out, err := m.ownerRead(ctx, host, "if test -f "+file+" && test ! -L "+file+"; then head -c 65537 < "+file+"; else exit 1; fi")
	if err != nil {
		return err
	}
	if len(out) > 64<<10 {
		return errOwnerEvidence
	}
	var record hubendpoint.Record
	decoder := json.NewDecoder(bytes.NewReader(out))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&record); err != nil {
		return errOwnerEvidence
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return errOwnerEvidence
	}
	// Compare the entire credential-free record, not a PID or boot ID alone.
	before, err := json.Marshal(prior.report.Running)
	if err != nil {
		return err
	}
	after, err := json.Marshal(record)
	if err != nil || !bytes.Equal(before, after) {
		return errOwnerSelectionMismatch
	}
	return nil
}
