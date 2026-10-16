package sshconn

import (
	"context"
	"errors"
	"fmt"
	"path"
	"slices"
	"strconv"
	"strings"

	"primeradiant.com/evener/cmd/evener-hub/internal/hostreg"
	"primeradiant.com/evener/execsupport/shellquote"
	"primeradiant.com/evener/internal/hubendpoint"
)

// Missing ownership/provenance is not positive evidence of a legacy image.
// Keep this separate from the terminal, positively unsupported capability.
var errOwnerEvidence = errors.New("sshconn: selected owner evidence missing")

// selectedOwner is a read-only live-owner snapshot, not a restart command or
// durable operation record. It grants no authority after any field changes.
// Call revalidateOwnedEndpoint immediately before consuming it for mutation.
// Stopped pending replacements require a separate retained launch/service intent.
type selectedOwner struct {
	host       hostreg.Host
	pid        string
	supervisor supervisor
	generation string
	argv       []string
	cwd        string
	report     hubendpoint.Report
}

// resolveOwnedEndpoint establishes the independent Linux owner of the selected
// root before probing its original image. An endpoint PID alone is never kill
// authority. This bounded seam supports an absolute explicit config selector;
// default/service-environment config selection needs its own context evidence.
// No mutation, listener-port query, authentication or daemon launch occurs here.
func (m *Manager) resolveOwnedEndpoint(ctx context.Context, host hostreg.Host, facts Preflight) (selectedOwner, error) {
	report, err := m.resolveHostEndpoint(ctx, host, facts)
	if err != nil {
		return selectedOwner{}, err
	}
	if facts.OS != "linux" || !path.IsAbs(host.ConfigPath) || report.State == "stopped" {
		return selectedOwner{}, fmt.Errorf("%w: host %q requires live Linux owner and absolute config selection", errOwnerEvidence, host.Name)
	}
	pid, err := m.selectedLockPID(ctx, host, report.HubStateRoot)
	if err != nil {
		return selectedOwner{}, err
	}
	if report.Running != nil && strconv.Itoa(report.Running.PID) != pid {
		return selectedOwner{}, fmt.Errorf("%w: selected record PID differs from independent lock owner", errOwnerEvidence)
	}
	before, err := m.ownerGeneration(ctx, host, pid)
	if err != nil {
		return selectedOwner{}, err
	}
	if err := m.checkHubOwner(ctx, host, pid); err != nil {
		return selectedOwner{}, fmt.Errorf("%w: %w", errOwnerEvidence, err)
	}
	m.applyResolvedTarget(&host)
	argv, err := m.recoverHubArgv(ctx, host, pid)
	if err != nil {
		return selectedOwner{}, fmt.Errorf("%w: %w", errOwnerEvidence, err)
	}
	config, ok := ownerConfigFlag(argv)
	if !ok || config != report.ConfigPath {
		return selectedOwner{}, fmt.Errorf("%w: process config does not match selected config", errOwnerEvidence)
	}
	cwdOut, err := m.ownerRead(ctx, host, "readlink /proc/"+pid+"/cwd")
	if err != nil {
		return selectedOwner{}, err
	}
	cwd := strings.TrimSpace(string(cwdOut))
	if !path.IsAbs(cwd) {
		return selectedOwner{}, fmt.Errorf("%w: process working directory unavailable", errOwnerEvidence)
	}
	sup, err := m.supervisorForOwner(ctx, host, pid, argv, cwd)
	if err != nil {
		return selectedOwner{}, err
	}
	if err := m.probeOwnerImageCapability(ctx, host, pid); err != nil {
		return selectedOwner{}, err
	}
	after, err := m.ownerGeneration(ctx, host, pid)
	if err != nil {
		return selectedOwner{}, err
	}
	currentPID, err := m.selectedLockPID(ctx, host, report.HubStateRoot)
	if err != nil {
		return selectedOwner{}, err
	}
	if before != after || currentPID != pid {
		return selectedOwner{}, fmt.Errorf("%w: selected generation changed during inspection", errOwnerEvidence)
	}
	host.Roots = slices.Clone(host.Roots)
	return selectedOwner{host: host, pid: pid, supervisor: sup, generation: before, argv: slices.Clone(argv), cwd: cwd, report: report}, nil
}

// revalidateOwnedEndpoint redoes ownership and original-image capability rather
// than inheriting authority from a PID or a newly replaced on-disk executable.
// Listener TOML edits are not frozen, but a root, registration, owner, launch or
// generation change invalidates this live snapshot.
func (m *Manager) revalidateOwnedEndpoint(ctx context.Context, host hostreg.Host, facts Preflight, prior selectedOwner) (selectedOwner, error) {
	m.applyResolvedTarget(&host)
	if !host.Equal(prior.host) || host.Generation != prior.host.Generation || host.IncarnationID != prior.host.IncarnationID || host.PresenceEpoch != prior.host.PresenceEpoch {
		return selectedOwner{}, fmt.Errorf("%w: host registration changed", errOwnerEvidence)
	}
	now, err := m.resolveOwnedEndpoint(ctx, host, facts)
	if err != nil {
		return selectedOwner{}, err
	}
	if now.pid != prior.pid || now.generation != prior.generation || now.supervisor != prior.supervisor || now.cwd != prior.cwd || !slices.Equal(now.argv, prior.argv) || now.report.HubStateRoot != prior.report.HubStateRoot || (prior.report.Running != nil && now.report.Running != nil && prior.report.Running.BootID != now.report.Running.BootID) {
		return selectedOwner{}, fmt.Errorf("%w: selected owner changed", errOwnerEvidence)
	}
	return now, nil
}

func (m *Manager) ownerRead(ctx context.Context, host hostreg.Host, remote string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	out, err := m.runner.Run(ctx, rawCommandArgv(m.opts, host, remote), nil)
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if err != nil {
		return nil, fmt.Errorf("%w: host %q owner probe: %w", errOwnerEvidence, host.Name, err)
	}
	return out, nil
}

// Linux /proc/locks identifies the process holding this existing inode, not any
// listener on a guessed port. stat's decimal st_dev is split using Linux dev_t's
// major/minor encoding to match proc_locks' hexadecimal device fields. Never
// create, unlink, replace or acquire hub.lock in an observer.
func selectedLockRemote(root string) string {
	return "set -- $(stat -Lc '%d %i' " + shellquote.RemoteWord(path.Join(root, "hub.lock")) + "); [ \"$#\" -eq 2 ] || exit 1; " +
		"dev=$1; ino=$2; major=$(( ((dev >> 8) & 4095) | ((dev >> 32) & 4294963200) )); minor=$(( (dev & 255) | ((dev >> 12) & 4294967040) )); " +
		"key=$(printf '%02x:%02x:%s' \"$major\" \"$minor\" \"$ino\"); " +
		"awk -v key=\"$key\" '$2 == \"FLOCK\" && $3 == \"ADVISORY\" && $4 == \"WRITE\" && $6 == key { print $5 }' /proc/locks"
}

func (m *Manager) selectedLockPID(ctx context.Context, host hostreg.Host, root string) (string, error) {
	if !path.IsAbs(root) {
		return "", fmt.Errorf("%w: state root is not absolute", errOwnerEvidence)
	}
	out, err := m.ownerRead(ctx, host, selectedLockRemote(root))
	if err != nil {
		return "", err
	}
	fields := strings.Fields(string(out))
	if len(fields) != 1 {
		return "", fmt.Errorf("%w: selected lock has no unique visible owner", errOwnerEvidence)
	}
	pid, err := numericPID(fields[0])
	if err != nil || pid == "0" {
		return "", fmt.Errorf("%w: invalid lock owner", errOwnerEvidence)
	}
	return pid, nil
}

func ownerGenerationRemote(pid string) string {
	return "cat /proc/sys/kernel/random/boot_id && s=$(cat /proc/" + pid + "/stat) && s=${s##*) } && set -- $s && [ \"$#\" -ge 20 ] && shift 19 && printf '%s\\n' \"$1\""
}

func (m *Manager) ownerGeneration(ctx context.Context, host hostreg.Host, pid string) (string, error) {
	if _, err := numericPID(pid); err != nil {
		return "", err
	}
	out, err := m.ownerRead(ctx, host, ownerGenerationRemote(pid))
	if err != nil {
		return "", err
	}
	lines := strings.Fields(string(out))
	if len(lines) != 2 || !isBareSafeLabel(lines[0]) || !isNumericUID(lines[1]) {
		return "", fmt.Errorf("%w: process generation unavailable", errOwnerEvidence)
	}
	return lines[0] + ":" + lines[1], nil
}

// This executes the mapped original Linux image, not argv[0]'s current pathname.
// A completed valid unsupported contract is migration. Denied proc access,
// transport failure or malformed output is missing evidence, never absence or
// proof that the image is old. Callers must bracket this with owner/generation
// checks, as resolveOwnedEndpoint does.
func (m *Manager) probeOwnerImageCapability(ctx context.Context, host hostreg.Host, pid string) error {
	if _, err := numericPID(pid); err != nil {
		return err
	}
	out, err := m.ownerRead(ctx, host, "/proc/"+pid+"/exe launch-check --json")
	if err != nil {
		return err
	}
	if len(out) > 65536 {
		return fmt.Errorf("%w: running image contract unavailable", errOwnerEvidence)
	}
	contract, err := parseLaunchCheck(out)
	if err != nil {
		// The disk preflight taxonomy must not make missing running-image
		// evidence a terminal claim about that image's capability.
		return fmt.Errorf("%w: running image contract unavailable", errOwnerEvidence)
	}
	if contract.HubEndpointSchema != hubendpoint.SchemaVersion {
		return endpointMigration(host)
	}
	return nil
}

// Absolute config flags have unambiguous interpretation independent of service
// HOME, XDG defaults and cwd. Duplicate or absent selectors need more evidence.
func ownerConfigFlag(argv []string) (string, bool) {
	var config string
	count := 0
	for i := 2; i < len(argv); i++ {
		a := argv[i]
		for _, flag := range []string{"--config", "-config"} {
			if a == flag && i+1 < len(argv) {
				config = argv[i+1]
				count++
				i++
				break
			}
			if v, ok := strings.CutPrefix(a, flag+"="); ok {
				config = v
				count++
				break
			}
		}
	}
	return config, count == 1 && path.IsAbs(config)
}

func supervisorShowRemote(s supervisor) string {
	prefix := "systemctl"
	if s.kind == supervisorSystemdUser {
		prefix += " --user"
	}
	return prefix + " show --property=Id,MainPID,User,ExecStart,WorkingDirectory -- " + shellquote.RemoteWord(s.label)
}

// Candidate listings are not authority. Resolve every visible candidate before
// associating the selected lock owner, including the user scope when a system
// service is live. An unresolved listing/definition never means ad-hoc fallback.
func (m *Manager) supervisorForOwner(ctx context.Context, host hostreg.Host, pid string, argv []string, cwd string) (supervisor, error) {
	var candidates []supervisor
	for _, scope := range []struct {
		kind   supervisorKind
		remote string
	}{{supervisorSystemd, systemctlListUnits}, {supervisorSystemdUser, systemctlListUnitsUser}} {
		out, err := m.ownerRead(ctx, host, scope.remote)
		if err != nil {
			return supervisor{}, err
		}
		live, dormant := parseSystemdHubs(out)
		for _, label := range append(live, dormant...) {
			candidates = append(candidates, supervisor{kind: scope.kind, label: label})
		}
	}
	var chosen supervisor
	for _, candidate := range candidates {
		out, err := m.ownerRead(ctx, host, supervisorShowRemote(candidate))
		if err != nil {
			return supervisor{}, err
		}
		fields := map[string]string{}
		for line := range strings.SplitSeq(strings.TrimSpace(string(out)), "\n") {
			key, value, ok := strings.Cut(line, "=")
			if !ok {
				return supervisor{}, fmt.Errorf("%w: malformed service definition", errOwnerEvidence)
			}
			if _, exists := fields[key]; exists {
				return supervisor{}, fmt.Errorf("%w: duplicate service property", errOwnerEvidence)
			}
			fields[key] = value
		}
		for _, key := range []string{"Id", "MainPID", "User", "ExecStart", "WorkingDirectory"} {
			if _, ok := fields[key]; !ok {
				return supervisor{}, fmt.Errorf("%w: incomplete service definition", errOwnerEvidence)
			}
		}
		if fields["Id"] != candidate.label || !isNumericUID(fields["MainPID"]) {
			return supervisor{}, fmt.Errorf("%w: service identity unavailable", errOwnerEvidence)
		}
		if strings.Count(fields["ExecStart"], "{ path=") != 1 {
			return supervisor{}, fmt.Errorf("%w: service executable association is ambiguous", errOwnerEvidence)
		}
		if fields["MainPID"] != pid {
			continue
		}
		// systemctl's formatted argv is not a general shell serialization. Accept
		// only an exact corroboration of the independently recovered NUL argv with
		// simple words. Refuse ambiguous formatting instead of inventing boundaries.
		for _, arg := range argv {
			if strings.ContainsAny(arg, " \t\r\n\\\"';$") {
				return supervisor{}, fmt.Errorf("%w: service argv formatting is ambiguous", errOwnerEvidence)
			}
		}
		prefix := "{ path=" + argv[0] + " ; argv[]=" + strings.Join(argv, " ") + " ; "
		user, ok := effectiveUserName(host)
		if !ok {
			userOut, err := m.ownerRead(ctx, host, "id -un")
			if err != nil {
				return supervisor{}, err
			}
			user = strings.TrimSpace(string(userOut))
		}
		definitionUser := fields["User"]
		if definitionUser == "" {
			definitionUser = "root"
			if candidate.kind == supervisorSystemdUser {
				definitionUser = user
			}
		}
		workingDirectory := fields["WorkingDirectory"]
		if workingDirectory == "" {
			if candidate.kind == supervisorSystemdUser {
				return supervisor{}, fmt.Errorf("%w: default user-service working directory requires home context", errOwnerEvidence)
			}
			workingDirectory = "/"
		}
		if definitionUser != user || workingDirectory != cwd || !strings.HasPrefix(fields["ExecStart"], prefix) || !strings.HasSuffix(fields["ExecStart"], " }") {
			return supervisor{}, fmt.Errorf("%w: service definition disagrees with selected process", errOwnerEvidence)
		}
		if chosen.kind != supervisorNone {
			return supervisor{}, fmt.Errorf("%w: multiple services associate with selected process", errOwnerEvidence)
		}
		chosen = candidate
	}
	if chosen.kind == supervisorNone && len(candidates) != 0 {
		return supervisor{}, fmt.Errorf("%w: supervised candidates do not associate with selected owner", errOwnerEvidence)
	}
	return chosen, nil
}
