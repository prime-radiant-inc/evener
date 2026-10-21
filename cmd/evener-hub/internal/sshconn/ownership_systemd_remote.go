package sshconn

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"primeradiant.com/evener/cmd/evener-hub/internal/hostreg"
	"primeradiant.com/evener/internal/hubendpoint"
)

// When /proc/locks cannot locate an owner, a service's current MainPID and
// complete process/config/root association are independent restart authority.
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
