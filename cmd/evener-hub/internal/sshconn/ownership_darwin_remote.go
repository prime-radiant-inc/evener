package sshconn

import (
	"bytes"
	"context"
	"fmt"
	"path"
	"slices"
	"strconv"
	"strings"

	"primeradiant.com/evener/cmd/evener-hub/internal/hostreg"
	"primeradiant.com/evener/execsupport/shellquote"
	"primeradiant.com/evener/internal/hubendpoint"
)

// launchctl print is filtered on the remote host. In particular its environment
// block is never returned wholesale across SSH. Arguments remain one per line.
func launchdDefinitionRemote(uid, label string) string {
	return "launchctl print " + shellquote.RemoteWord("gui/"+uid+"/"+label) + " | awk '/^[ \t]*(pid|program|working directory) =/ { print; next } /^[ \t]*arguments = \\{/ { a=1; print; next } a { print; if ($0 ~ /^[ \t]*}/) a=0; next } /^[ \t]*(inherited environment|environment) = \\{/ { e=1; next } e && /^[ \t]*}/ { e=0; next } e && /^[ \t]*(HOME|XDG_CONFIG_HOME|XDG_STATE_HOME|EVENER_HUB_LISTEN_ADDR) =>/ { print }'"
}

type launchdDefinition struct {
	pid, program, cwd string
	argv, env         []string
}

func parseLaunchdDefinition(out []byte) (launchdDefinition, error) {
	var def launchdDefinition
	args := false
	for line := range strings.SplitSeq(string(out), "\n") {
		line = strings.TrimLeft(line, " \t")
		if line == "" {
			continue
		}
		if line == "arguments = {" {
			if args || def.argv != nil {
				return def, errOwnerEvidence
			}
			args = true
			continue
		}
		if args {
			if line == "}" {
				args = false
			} else {
				def.argv = append(def.argv, line)
			}
			continue
		}
		key, value, ok := strings.Cut(line, " = ")
		if ok {
			switch key {
			case "pid":
				def.pid = value
			case "program":
				def.program = value
			case "working directory":
				def.cwd = value
			default:
				return def, errOwnerEvidence
			}
			continue
		}
		key, value, ok = strings.Cut(line, " => ")
		if !ok {
			return def, errOwnerEvidence
		}
		switch key {
		case "HOME", "XDG_CONFIG_HOME", "XDG_STATE_HOME", "EVENER_HUB_LISTEN_ADDR":
			for i, prior := range def.env {
				if strings.HasPrefix(prior, key+"=") {
					def.env = append(def.env[:i], def.env[i+1:]...)
					break
				}
			}
			def.env = append(def.env, key+"="+value)
		default:
			return def, errOwnerEvidence
		}
	}
	if def.pid == "" {
		def.pid = "0"
	}
	if args || !path.IsAbs(def.program) || len(def.argv) < 2 || def.argv[0] != def.program || validateHubArgv(def.argv) != nil {
		return def, errOwnerEvidence
	}
	if def.cwd == "" {
		def.cwd = "/"
	}
	slices.Sort(def.env)
	return def, nil
}

func (m *Manager) launchdOwners(ctx context.Context, host hostreg.Host, facts Preflight, selectedPID ...string) (map[supervisor]launchdDefinition, error) {
	if !isNumericUID(facts.UID) {
		return nil, fmt.Errorf("%w: launchd UID unavailable", errOwnerEvidence)
	}
	out, err := m.ownerRead(ctx, host, "launchctl list")
	if err != nil {
		return nil, err
	}
	live, dormant := parseLaunchdHubs(out)
	listing := string(out)
	defs := map[supervisor]launchdDefinition{}
	for _, label := range append(live, dormant...) {
		if !isBareSafeLabel(label) {
			unrelated := false
			for line := range strings.SplitSeq(listing, "\n") {
				fields := strings.Fields(line)
				if len(fields) == 3 && fields[2] == label && len(selectedPID) == 1 && fields[0] != selectedPID[0] {
					unrelated = true
				}
			}
			if unrelated {
				continue
			}
			return nil, errOwnerEvidence
		}
		out, err := m.ownerRead(ctx, host, launchdDefinitionRemote(facts.UID, label))
		if err != nil {
			return nil, err
		}
		def, err := parseLaunchdDefinition(out)
		if err != nil {
			return nil, fmt.Errorf("%w: launchd definition unavailable", errOwnerEvidence)
		}
		defs[supervisor{kind: supervisorLaunchd, label: label}] = def
	}
	return defs, nil
}

func (m *Manager) darwinGeneration(ctx context.Context, host hostreg.Host, pid string) (string, error) {
	out, err := m.ownerRead(ctx, host, "ps -p "+pid+" -o lstart=")
	if err != nil {
		return "", err
	}
	generation := strings.TrimSpace(string(out))
	if generation == "" {
		return "", errOwnerEvidence
	}
	return generation, nil
}

func (m *Manager) resolveDarwinOwner(ctx context.Context, host hostreg.Host, facts Preflight, report hubendpoint.Report) (selectedOwner, error) {
	var selectedPID []string
	if report.Running != nil {
		selectedPID = []string{strconv.Itoa(report.Running.PID)}
	}
	defs, err := m.launchdOwners(ctx, host, facts, selectedPID...)
	if err != nil {
		return selectedOwner{}, err
	}
	var sup supervisor
	var def launchdDefinition
	// A running report supplies a locator only. A damaged report can still select
	// an unambiguous independently configured launchd service, without HTTP.
	for candidate, d := range defs {
		if d.pid == "0" {
			continue
		}
		if report.Running != nil && strconv.Itoa(report.Running.PID) != d.pid {
			continue
		}
		if err := m.hubExecutableMatches(ctx, host, d.program); err != nil {
			if report.Running != nil {
				return selectedOwner{}, fmt.Errorf("%w: selected launchd executable disagrees", errOwnerEvidence)
			}
			continue
		}
		if err := m.checkOwnerConfig(ctx, host, d.argv, d.cwd, d.env, report); err != nil {
			if report.Running != nil {
				return selectedOwner{}, err
			}
			continue
		}
		if sup.kind != supervisorNone {
			return selectedOwner{}, fmt.Errorf("%w: multiple selected launchd services", errOwnerEvidence)
		}
		sup, def = candidate, d
	}
	pid := def.pid
	if sup.kind == supervisorNone {
		if report.Running == nil {
			// A prior selected ad-hoc PID is only a locator. Re-establish its user,
			// argv, executable, cwd, environment, root and birth below before use.
			m.mu.Lock()
			prior, ok := m.ownerWitnesses[host.Name]
			m.mu.Unlock()
			if !ok || prior.supervisor.kind != supervisorNone || !host.Equal(prior.host) || host.Generation != prior.host.Generation || host.IncarnationID != prior.host.IncarnationID || host.PresenceEpoch != prior.host.PresenceEpoch || report.ConfigPath != prior.report.ConfigPath || report.HubStateRoot != prior.report.HubStateRoot {
				return selectedOwner{}, fmt.Errorf("%w: no selected Darwin owner", errOwnerEvidence)
			}
			pid = prior.pid
		} else {
			pid = strconv.Itoa(report.Running.PID)
		}
	}
	if _, err := numericPID(pid); err != nil || pid == "0" {
		return selectedOwner{}, errOwnerEvidence
	}
	generation, err := m.darwinGeneration(ctx, host, pid)
	if err != nil {
		return selectedOwner{}, err
	}
	if err := m.checkHubOwner(ctx, host, pid); err != nil {
		return selectedOwner{}, fmt.Errorf("%w: %w", errOwnerEvidence, err)
	}
	argv, err := m.recoverHubArgvRaw(ctx, host, pid)
	if err != nil {
		return selectedOwner{}, fmt.Errorf("%w: %w", errOwnerEvidence, err)
	}
	out, err := m.ownerRead(ctx, host, "lsof -a -p "+pid+" -d cwd -Fn")
	if err != nil {
		return selectedOwner{}, err
	}
	var actualCwd string
	for line := range strings.SplitSeq(string(out), "\n") {
		if strings.HasPrefix(line, "n/") {
			actualCwd = strings.TrimPrefix(line, "n")
		}
	}
	if !path.IsAbs(actualCwd) {
		return selectedOwner{}, fmt.Errorf("%w: Darwin process cwd disagrees", errOwnerEvidence)
	}
	if err := m.checkOwnerExecutable(ctx, host, argv[0], actualCwd); err != nil {
		return selectedOwner{}, fmt.Errorf("%w: %w", errOwnerEvidence, err)
	}
	if sup.kind != supervisorNone {
		matches, err := m.sameDirectory(ctx, host, actualCwd, def.cwd)
		if err != nil {
			return selectedOwner{}, err
		}
		if !matches {
			return selectedOwner{}, fmt.Errorf("%w: Darwin process cwd disagrees", errOwnerEvidence)
		}
	}
	if sup.kind == supervisorNone {
		def.cwd = actualCwd
		out, err = m.ownerRead(ctx, host, darwinEnvironmentRemote(pid))
		if err != nil {
			return selectedOwner{}, err
		}
		def.env, err = decodeOwnerEnvironment(out)
		if err != nil {
			return selectedOwner{}, err
		}
		if err := m.checkOwnerConfig(ctx, host, argv, def.cwd, def.env, report); err != nil {
			return selectedOwner{}, err
		}
	} else if !m.supervisorArgvMatches(ctx, host, pid, argv, def.argv, report, def.env, true) {
		return selectedOwner{}, errOwnerEvidence
	}
	owner := selectedOwner{host: host, pid: pid, supervisor: sup, generation: generation, argv: argv, cwd: def.cwd, cwdIdentity: actualCwd, env: def.env, report: report}
	// Ad-hoc launch identity is argv, cwd and environment, just as for a fresh
	// start. Domain and service definition identify launchd ownership only.
	if sup.kind != supervisorNone {
		owner.launchArgv = slices.Clone(def.argv)
		owner.definition, owner.domainUID = launchdIntent(def), facts.UID
		if report.Running == nil && !slices.Equal(argv, def.argv) && !m.hasOwnerWitness(owner) {
			return selectedOwner{}, errOwnerEvidence
		}
	}
	// A witness is scoped to birth plus the entire independently selected launch,
	// never a PID alone. Without continuity, re-establish mapped-image capability.
	if report.Running == nil && !m.hasOwnerWitness(owner) {
		if err := m.checkDarwinRootVnode(ctx, host, pid, report.HubStateRoot); err != nil {
			return selectedOwner{}, err
		}
		if err := m.probeDarwinImage(ctx, host, pid, facts); err != nil {
			return selectedOwner{}, err
		}
	}
	after, err := m.darwinGeneration(ctx, host, pid)
	if err != nil || after != generation {
		return selectedOwner{}, errOwnerEvidence
	}
	m.rememberOwner(owner)
	return owner.clone(), nil
}

// Darwin lsof cannot report flock ownership. Its vnode inventory can still
// independently corroborate that this process opened the selected root's lock,
// rather than infer that root from mutable TOML or executable capability.
func (m *Manager) checkDarwinRootVnode(ctx context.Context, host hostreg.Host, pid, root string) error {
	if !path.IsAbs(root) || strings.ContainsAny(root, "\r\n") {
		return errOwnerEvidence
	}
	lock := shellquote.RemoteWord(path.Join(root, "hub.lock"))
	command := "test ! -L " + lock + " && test -f " + lock + " && stat -f '%d %i' " + lock
	before, err := m.ownerRead(ctx, host, command)
	if err != nil {
		return err
	}
	fields := strings.Fields(string(before))
	if len(fields) != 2 {
		return errOwnerEvidence
	}
	dev, err := strconv.ParseUint(fields[0], 10, 64)
	if err != nil || !isNumericUID(fields[1]) {
		return errOwnerEvidence
	}
	out, err := m.ownerRead(ctx, host, "lsof -a -p "+pid+" -F pftDi")
	if err != nil {
		return err
	}
	if !darwinOpenVnode(out, pid, dev, fields[1]) {
		return fmt.Errorf("%w: selected Darwin root lacks process vnode evidence", errOwnerEvidence)
	}
	after, err := m.ownerRead(ctx, host, command)
	if err != nil || !bytes.Equal(before, after) {
		return errOwnerEvidence
	}
	return nil
}

func darwinOpenVnode(out []byte, pid string, device uint64, inode string) bool {
	var process, fd, kind, dev, ino string
	found, bad := false, false
	finish := func() {
		if !isNumericUID(fd) {
			return // cwd and mapped executable vnodes are not open lock descriptors.
		}
		if kind != "" && kind != "REG" {
			return
		}
		d, err := strconv.ParseUint(strings.TrimPrefix(dev, "0x"), 16, 64)
		if err != nil || !isNumericUID(ino) || kind == "" {
			bad = true
		} else if kind == "REG" && d == device && ino == inode {
			found = true
		}
	}
	for line := range strings.SplitSeq(strings.TrimSuffix(string(out), "\n"), "\n") {
		if len(line) < 2 {
			return false
		}
		switch line[0] {
		case 'p':
			if process != "" || line[1:] != pid {
				return false
			}
			process = line[1:]
		case 'f':
			if process == "" {
				return false
			}
			finish()
			fd, kind, dev, ino = line[1:], "", "", ""
		case 't', 'D', 'i':
			if fd == "" {
				return false
			}
			field := &kind
			switch line[0] {
			case 'D':
				field = &dev
			case 'i':
				field = &ino
			}
			if *field != "" {
				return false
			}
			*field = line[1:]
		default:
			return false
		}
	}
	finish()
	return found && !bad && process == pid
}

// Darwin ps exposes process environment only through its wide command output.
// Filter on-host before returning anything. Whitespace-containing relevant values
// are ambiguous in ps serialization and refused, never truncated into new intent.
func darwinEnvironmentRemote(pid string) string {
	return "ps eww -p " + pid + " -o command= | awk '{ for (i=1; i<=NF; i++) if ($i ~ /^(HOME|XDG_CONFIG_HOME|XDG_STATE_HOME|EVENER_HUB_LISTEN_ADDR)=/) { if (i < NF && $(i+1) !~ /^[A-Za-z_][A-Za-z_0-9]*=/) exit 1; printf \"%s%c\", $i, 0 } }'"
}

func equalStrings(a, b []string) bool { return strings.Join(a, "\x00") == strings.Join(b, "\x00") }
func launchdIntent(def launchdDefinition) string {
	return def.program + "\x00" + def.cwd + "\x00" + strings.Join(def.argv, "\x00") + "\x00" + strings.Join(def.env, "\x00")
}

func (m *Manager) probeDarwinImage(ctx context.Context, host hostreg.Host, pid string, facts Preflight) error {
	return m.probeDarwinImageBuild(ctx, host, pid, facts, "")
}

func (m *Manager) probeDarwinImageBuild(ctx context.Context, host hostreg.Host, pid string, facts Preflight, version string) error {
	exe, err := m.expectedHubExecutable(ctx, host)
	if err != nil {
		return err
	}
	out, err := m.ownerRead(ctx, host, "stat -f '%d %i' "+shellquote.RemoteWord(exe))
	if err != nil {
		return err
	}
	fields := strings.Fields(string(out))
	if len(fields) != 2 {
		return errOwnerEvidence
	}
	dev, err := strconv.ParseUint(fields[0], 10, 64)
	if err != nil {
		return errOwnerEvidence
	}
	// lsof D is a hexadecimal vnode device, i is decimal inode.
	mapped, err := m.ownerRead(ctx, host, "lsof -a -p "+pid+" -d txt -F Di")
	if err != nil {
		return err
	}
	found := false
	var currentDev uint64
	for line := range strings.SplitSeq(string(mapped), "\n") {
		if device, ok := strings.CutPrefix(line, "D"); ok {
			currentDev, _ = strconv.ParseUint(strings.TrimPrefix(device, "0x"), 16, 64)
		}
		if strings.HasPrefix(line, "i") && currentDev == dev && strings.TrimPrefix(line, "i") == fields[1] {
			found = true
		}
	}
	if !found {
		return fmt.Errorf("%w: running Darwin image differs from inspected disk", errOwnerEvidence)
	}
	// Re-read capability only after image association and recheck the vnode after.
	out, err = m.runner.Run(ctx, evenerCommandArgv(m.opts, host, "launch-check", "--json"), nil)
	if err != nil {
		return fmt.Errorf("%w: running image contract unavailable", errOwnerEvidence)
	}
	contract, err := parseLaunchCheck(out)
	if err != nil {
		return errOwnerEvidence
	}
	again, err := m.ownerRead(ctx, host, "stat -f '%d %i' "+shellquote.RemoteWord(exe))
	if err != nil || !bytes.Equal(again, []byte(strings.Join(fields, " ")+"\n")) {
		return errOwnerEvidence
	}
	if contract.HubEndpointSchema != hubendpoint.SchemaVersion {
		return endpointMigration(host)
	}
	if version != "" && contract.Version != version {
		return fmt.Errorf("%w: running image build differs from pending replacement", errOwnerEvidence)
	}
	return ctx.Err()
}

func (m *Manager) revalidateStoppedLaunchd(ctx context.Context, host hostreg.Host, facts Preflight, owner selectedOwner) error {
	if facts.UID != owner.domainUID {
		return fmt.Errorf("%w: selected launchd domain changed", errOwnerEvidence)
	}
	defs, err := m.launchdOwners(ctx, host, facts)
	if err != nil {
		return err
	}
	def, ok := defs[owner.supervisor]
	if !ok || def.pid != "0" || launchdIntent(def) != owner.definition {
		return fmt.Errorf("%w: stopped launchd selection changed", errOwnerEvidence)
	}
	return nil
}
