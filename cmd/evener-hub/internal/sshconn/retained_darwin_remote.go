package sshconn

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"path"
	"slices"
	"strings"

	"primeradiant.com/evener/cmd/evener-hub/internal/hostreg"
	"primeradiant.com/evener/execsupport/shellquote"
	"primeradiant.com/evener/internal/hubendpoint"
)

// A retained witness is not authority to write a different Darwin generation.
// The deleted image cannot run its resolver, so bind its still-live process to
// the previously verified record, launch context and birth instead.
func (m *Manager) revalidateRetainedDarwin(ctx context.Context, host hostreg.Host, facts Preflight, prior selectedOwner) error {
	var err error
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
	// Discovery enriches its copy with health-derived build fields, while the
	// startup publication legitimately omits them. Compare available build
	// evidence separately, then require every persisted identity field to match.
	expected := *prior.report.Running
	if (expected.Version != "" && record.Version != "" && expected.Version != record.Version) ||
		(expected.BackendGitSHA != "" && record.BackendGitSHA != "" && expected.BackendGitSHA != record.BackendGitSHA) {
		return errOwnerSelectionMismatch
	}
	expected.Version, record.Version = "", ""
	expected.BackendGitSHA, record.BackendGitSHA = "", ""
	before, err := json.Marshal(expected)
	if err != nil {
		return err
	}
	after, err := json.Marshal(record)
	if err != nil || !bytes.Equal(before, after) {
		return errOwnerSelectionMismatch
	}
	return nil
}
