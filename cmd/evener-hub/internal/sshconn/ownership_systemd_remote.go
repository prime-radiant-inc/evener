package sshconn

import (
	"bytes"
	"context"
	"fmt"
	"path"
	"strconv"
	"strings"

	"primeradiant.com/evener/cmd/evener-hub/internal/hostreg"
	"primeradiant.com/evener/execsupport/shellquote"
	"primeradiant.com/evener/internal/hubendpoint"
)

// When /proc/locks cannot locate an owner, MainPID is only a candidate. Current
// TOML cannot prove the root a process started with, so corroborate that root
// with its open lock vnode or a previously verified same-process/root witness.
// Never fall back to a listener or use the report PID as authority by itself.
func (m *Manager) selectedServicePID(ctx context.Context, host hostreg.Host, report hubendpoint.Report) (string, error) {
	var selected string
	for _, scope := range []struct {
		kind   supervisorKind
		remote string
	}{{supervisorSystemd, systemctlListUnits}, {supervisorSystemdUser, systemctlListUnitsUser}} {
		out, err := m.ownerRead(ctx, host, scope.remote)
		if err != nil {
			if supervisorListingAbsent(out) || (scope.kind == supervisorSystemdUser && systemdUserBusAbsent(out)) {
				continue
			}
			return "", err
		}
		live, _ := parseSystemdHubs(out)
		for _, label := range live {
			out, err := m.ownerRead(ctx, host, supervisorShowRemote(supervisor{kind: scope.kind, label: label}))
			if err != nil {
				return "", err
			}
			fields := map[string]string{}
			for line := range strings.SplitSeq(string(out), "\n") {
				if key, value, ok := strings.Cut(line, "="); ok {
					fields[key] = value
				}
			}
			pid := fields["MainPID"]
			if !isNumericUID(pid) || pid == "0" {
				return "", errOwnerEvidence
			}
			if report.Running != nil && strconv.Itoa(report.Running.PID) != pid {
				continue
			}
			// The formatted service argv is only a preliminary selector. The real NUL
			// process argv, user, executable, cwd and context are checked below and again
			// by resolveLinuxOwner. No service command line is executed here.
			_, formatted, ok := strings.Cut(fields["ExecStart"], " ; argv[]=")
			if !ok {
				return "", errOwnerEvidence
			}
			_, _, ok = strings.Cut(formatted, " ; ")
			if !ok {
				return "", errOwnerEvidence
			}
			argv, err := m.recoverHubArgv(ctx, host, pid)
			if err != nil {
				return "", err
			}
			cwdOut, err := m.ownerRead(ctx, host, "readlink /proc/"+pid+"/cwd")
			if err != nil {
				return "", err
			}
			cwd, err := ownerPathOutput(cwdOut)
			if err != nil {
				return "", err
			}
			env, err := m.ownerEnvironment(ctx, host, pid)
			if err != nil {
				return "", err
			}
			if err := m.checkOwnerConfig(ctx, host, argv, cwd, env, report); err != nil {
				continue
			}
			if err := m.checkServiceRoot(ctx, host, pid, report); err != nil {
				return "", err
			}
			if selected != "" && selected != pid {
				return "", fmt.Errorf("%w: multiple selected service PIDs", errOwnerEvidence)
			}
			selected = pid
		}
	}
	if selected == "" {
		return "", fmt.Errorf("%w: no selected lock or service owner", errOwnerEvidence)
	}
	return selected, nil
}

func (m *Manager) checkServiceRoot(ctx context.Context, host hostreg.Host, pid string, report hubendpoint.Report) error {
	m.mu.Lock()
	witness, ok := m.ownerWitnesses[host.Name]
	m.mu.Unlock()
	if ok && witness.pid == pid && checkSelectedRestartRegistration(host, witness.host) == nil && witness.report.ConfigPath == report.ConfigPath && witness.report.HubStateRoot == report.HubStateRoot {
		generation, err := m.ownerGeneration(ctx, host, pid)
		if err != nil {
			return err
		}
		if generation == witness.generation {
			return nil
		}
	}
	if !path.IsAbs(report.HubStateRoot) {
		return errOwnerEvidence
	}
	out, err := m.ownerRead(ctx, host, serviceLockVnodeRemote(report.HubStateRoot, pid))
	if err != nil {
		return err
	}
	if !bytes.Equal(out, []byte("match\n")) {
		return fmt.Errorf("%w: service has no selected root witness", errOwnerEvidence)
	}
	return nil
}

// An open descriptor names the process's actual vnode, not a re-resolution of
// its launch arguments against mutable TOML. Pin the selected vnode on both
// sides of the read. This observes only, without creating or acquiring a lock.
func serviceLockVnodeRemote(root, pid string) string {
	return "lock=" + shellquote.RemoteWord(path.Join(root, "hub.lock")) + "; [ -f \"$lock\" ] || exit 1; identity=$(stat -Lc '%d %i' \"$lock\") || exit; found=; " +
		"[ -r /proc/" + pid + "/fd ] && [ -x /proc/" + pid + "/fd ] || exit 1; for fd in /proc/" + pid + "/fd/*; do current=$(stat -Lc '%d %i' \"$fd\" 2>/dev/null) || continue; " +
		"if [ \"$current\" = \"$identity\" ]; then found=yes; break; fi; done; current=$(stat -Lc '%d %i' \"$lock\") || exit; " +
		"[ \"$current\" = \"$identity\" ] && [ \"$found\" = yes ] || exit 1; printf 'match\\n'"
}
