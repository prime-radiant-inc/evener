package sshconn

import (
	"bytes"
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

var errOwnerSelectionMismatch = fmt.Errorf("%w: different selected owner", errOwnerEvidence)

// A service selecting this config under a different root is not unrelated.
// In particular, a cwd-relative root must never permit a second fresh launch.
var errOwnerRootMismatch = fmt.Errorf("%w: same config selects a different state root", errOwnerSelectionMismatch)

// selectedOwner is a read-only live-owner snapshot, not a restart command or
// durable operation record. It grants no authority after any field changes.
// Call revalidateOwnedEndpoint immediately before consuming it for mutation.
// Stopped pending replacements require a separate retained launch/service intent.
type selectedOwner struct {
	host        hostreg.Host
	pid         string
	supervisor  supervisor
	generation  string
	argv        []string
	launchArgv  []string
	cwd         string
	cwdIdentity string
	report      hubendpoint.Report
	env         []string
	definition  string
	logPath     string
	domainUID   string
}

func (o selectedOwner) clone() selectedOwner {
	o.host.Roots = slices.Clone(o.host.Roots)
	o.argv = slices.Clone(o.argv)
	o.launchArgv = slices.Clone(o.launchArgv)
	o.env = slices.Clone(o.env)
	if o.report.Running != nil {
		running := *o.report.Running
		o.report.Running = &running
	}
	return o
}

func (o selectedOwner) launchArguments() []string {
	if o.launchArgv != nil {
		return o.launchArgv
	}
	return o.argv
}

// resolveOwnedEndpoint associates the selected root with independent process and
// service evidence. An endpoint PID alone is never kill authority. Default and
// explicit config selectors are resolved in the owner's cwd and narrow environment.
// No mutation, listener-port query, authentication or daemon launch occurs here.
func (m *Manager) resolveOwnedEndpoint(ctx context.Context, host hostreg.Host, facts Preflight) (selectedOwner, error) {
	return m.resolveOwnedEndpointUsing(ctx, host, facts, m.resolveHostEndpoint)
}

func (m *Manager) resolveOwnedEndpointUsing(ctx context.Context, host hostreg.Host, facts Preflight, read func(context.Context, hostreg.Host, Preflight) (hubendpoint.Report, error)) (selectedOwner, error) {
	// Capture the same effective executable that revalidation and replay compare,
	// including for Darwin and cold stopped services.
	m.applyResolvedTarget(&host)
	report, err := read(ctx, host, facts)
	if err != nil {
		return selectedOwner{}, err
	}
	if report.State == "stopped" {
		return m.resolveStoppedOwner(ctx, host, facts, report)
	}
	if facts.OS == "darwin" {
		return m.resolveDarwinOwner(ctx, host, facts, report)
	}
	if facts.OS != "linux" {
		return selectedOwner{}, fmt.Errorf("%w: host %q requires a live selected owner", errOwnerEvidence, host.Name)
	}
	pid, lockErr := m.selectedLockPID(ctx, host, report.HubStateRoot)
	if lockErr == nil {
		return m.resolveLinuxOwner(ctx, host, report, pid, true)
	}
	if ctx.Err() != nil {
		return selectedOwner{}, ctx.Err()
	}
	pid, err = m.selectedServicePID(ctx, host, report)
	if err != nil {
		return selectedOwner{}, err
	}
	return m.resolveLinuxOwner(ctx, host, report, pid, false)
}

func (m *Manager) resolveLinuxOwner(ctx context.Context, host hostreg.Host, report hubendpoint.Report, pid string, fromLock bool) (selectedOwner, error) {
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
	argv, err := m.recoverHubArgvRaw(ctx, host, pid)
	if err != nil {
		return selectedOwner{}, fmt.Errorf("%w: %w", errOwnerEvidence, err)
	}
	cwdOut, err := m.ownerRead(ctx, host, "readlink /proc/"+pid+"/cwd")
	if err != nil {
		return selectedOwner{}, err
	}
	cwd, err := ownerPathOutput(cwdOut)
	if err != nil {
		return selectedOwner{}, fmt.Errorf("%w: process working directory unavailable", errOwnerEvidence)
	}
	if err := m.checkOwnerExecutable(ctx, host, argv[0], cwd); err != nil {
		return selectedOwner{}, fmt.Errorf("%w: %w", errOwnerEvidence, err)
	}
	env, err := m.ownerEnvironment(ctx, host, pid)
	if err != nil {
		return selectedOwner{}, err
	}
	if err := m.checkOwnerConfig(ctx, host, argv, cwd, env, report); err != nil {
		return selectedOwner{}, err
	}
	sup, launchArgv, err := m.supervisorForOwner(ctx, host, pid, argv, cwd, env, report)
	if err != nil {
		return selectedOwner{}, err
	}
	owner := selectedOwner{host: host, pid: pid, supervisor: sup, generation: before, argv: argv, launchArgv: launchArgv, cwd: cwd, cwdIdentity: cwd, env: env, report: report}
	if sup.kind != supervisorNone {
		owner.definition, err = m.serviceDefinition(ctx, host, sup)
		if err != nil {
			return selectedOwner{}, err
		}
		if report.Running == nil && !slices.Equal(argv, launchArgv) && !m.hasOwnerWitness(owner) {
			return selectedOwner{}, errOwnerEvidence
		}
	} else {
		out, err := m.ownerRead(ctx, host, "cat /proc/"+pid+"/cgroup")
		if err != nil {
			return selectedOwner{}, err
		}
		for _, component := range strings.FieldsFunc(string(out), func(r rune) bool { return r == '/' || r == '\n' }) {
			if strings.HasSuffix(component, ".service") && isEvenerHubName(component) {
				return selectedOwner{}, fmt.Errorf("%w: selected process belongs to an unresolved service", errOwnerEvidence)
			}
		}
	}
	if report.Running == nil && !m.hasOwnerWitness(owner) {
		if err := m.probeOwnerImageCapability(ctx, host, pid); err != nil {
			return selectedOwner{}, err
		}
	}
	after, err := m.ownerGeneration(ctx, host, pid)
	if err != nil {
		return selectedOwner{}, err
	}
	currentPID := pid
	if fromLock {
		currentPID, err = m.selectedLockPID(ctx, host, report.HubStateRoot)
		if err != nil {
			return selectedOwner{}, err
		}
	} else {
		currentSupervisor, _, err := m.supervisorForOwner(ctx, host, pid, argv, cwd, env, report)
		if err != nil || currentSupervisor != sup || sup.kind == supervisorNone {
			return selectedOwner{}, errOwnerEvidence
		}
	}
	if before != after || currentPID != pid {
		return selectedOwner{}, fmt.Errorf("%w: selected generation changed during inspection", errOwnerEvidence)
	}
	m.rememberOwner(owner)
	return owner.clone(), nil
}

// A slash-relative argv[0] is relative to the process, not the SSH login cwd.
// Do not clean the concatenation: symlink/.. segments have filesystem meaning.
// Keep argv itself unchanged as frozen launch intent, and keep bare PATH names
// on the existing executable-name lookup path.
func (m *Manager) checkOwnerExecutable(ctx context.Context, host hostreg.Host, executable, cwd string) error {
	if !path.IsAbs(executable) && strings.Contains(executable, "/") {
		executable = cwd + "/" + executable
	}
	return m.hubExecutableMatches(ctx, host, executable)
}

// revalidateOwnedEndpoint redoes ownership and original-image capability rather
// than inheriting authority from a PID or a newly replaced on-disk executable.
// Listener TOML edits are not frozen, but a root, registration, owner, launch or
// generation change invalidates this live snapshot.
func (m *Manager) revalidateOwnedEndpoint(ctx context.Context, host hostreg.Host, facts Preflight, prior selectedOwner) (selectedOwner, error) {
	return m.revalidateOwnedEndpointUsing(ctx, host, facts, prior, m.resolveHostEndpoint)
}

func (m *Manager) revalidateOwnedEndpointUsing(ctx context.Context, host hostreg.Host, facts Preflight, prior selectedOwner, read func(context.Context, hostreg.Host, Preflight) (hubendpoint.Report, error)) (selectedOwner, error) {
	m.applyResolvedTarget(&host)
	if !host.Equal(prior.host) || host.Generation != prior.host.Generation || host.IncarnationID != prior.host.IncarnationID || host.PresenceEpoch != prior.host.PresenceEpoch {
		return selectedOwner{}, fmt.Errorf("%w: host registration changed", errOwnerEvidence)
	}
	now, err := m.resolveOwnedEndpointUsing(ctx, host, facts, read)
	if err != nil {
		return selectedOwner{}, err
	}
	if !sameOwner(now, prior) || (prior.report.Running != nil && now.report.Running != nil && prior.report.Running.BootID != now.report.Running.BootID) {
		return selectedOwner{}, fmt.Errorf("%w: selected owner changed", errOwnerEvidence)
	}
	return now, nil
}

func sameOwner(now, prior selectedOwner) bool {
	return now.host.Equal(prior.host) && now.host.Generation == prior.host.Generation && now.host.IncarnationID == prior.host.IncarnationID && now.host.PresenceEpoch == prior.host.PresenceEpoch && sameProcess(now, prior) && sameLaunch(now, prior)
}

func sameProcess(now, prior selectedOwner) bool {
	// Birth evidence can have second or tick precision, so it is not unique
	// without the independently associated PID.
	return now.pid == prior.pid && now.generation == prior.generation
}

func sameLaunch(now, prior selectedOwner) bool {
	return sameLaunchWithEnv(now, prior, prior.env)
}

func sameLaunchWithEnv(now, prior selectedOwner, env []string) bool {
	argvMatches := slices.Equal(now.argv, prior.argv)
	if !sameProcess(now, prior) && now.supervisor.kind != supervisorNone && now.launchArgv != nil && prior.launchArgv != nil {
		// Each live argv has independently matched this supervisor definition.
		// A new process starts its original intent, not an in-place bound-port argv.
		argvMatches = slices.Equal(now.launchArgv, prior.launchArgv)
	}
	return now.supervisor == prior.supervisor && now.domainUID == prior.domainUID && now.cwdIdentity == prior.cwdIdentity && argvMatches && slices.Equal(now.env, env) && now.definition == prior.definition && now.report.ConfigPath == prior.report.ConfigPath && now.report.HubStateRoot == prior.report.HubStateRoot
}

func (m *Manager) hasOwnerWitness(owner selectedOwner) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	prior, ok := m.ownerWitnesses[owner.host.Name]
	return ok && sameOwner(owner, prior)
}

func (m *Manager) rememberOwner(owner selectedOwner) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if prior, ok := m.ownerWitnesses[owner.host.Name]; ok && owner.report.Running == nil && sameOwner(owner, prior) {
		return // Preserve previously verified listener evidence through health loss.
	}
	if m.ownerWitnesses == nil {
		m.ownerWitnesses = make(map[string]selectedOwner)
	}
	m.ownerWitnesses[owner.host.Name] = owner.clone()
}

// Filter on the owning host before stdout crosses SSH. No unrelated environment
// value reaches the controller. NUL boundaries preserve both presence and value.
func ownerEnvironmentRemote(pid string) string {
	// Decimal byte tokens avoid optional grep -z and awk NUL-RS support. END
	// cannot be forged by encoded input and is emitted only after a successful
	// read, so the final parser propagates upstream failure without pipefail.
	// Stream values, retaining only a possible selected key, never data in argv.
	return "(exec 3< /proc/" + pid + "/environ || exit\n" + `
    { LC_ALL=C od -An -v -tu1 <&3 && printf 'END\n'; } |
    LC_ALL=C awk '
        $0 == "END" {
            if (done || active) { bad = 1; exit 1 }
            done = 1
            next
        }
        {
            if (done) { bad = 1; exit 1 }
            for (i = 1; i <= NF; i++) {
                if ($i !~ /^[0-9]+$/ || $i + 0 > 255) {
                    bad = 1
                    exit 1
                }
                b = $i + 0
                if (b == 0) {
                    if (mode == 1) printf "%c", 0 | "cat"
                    key = ""
                    mode = 0
                    active = 0
                } else {
                    active = 1
                    if (mode == 1) {
                        printf "%c", b | "cat"
                    } else if (mode == 0) {
                        if (b == 61) {
                            if (key == "HOME" || key == "XDG_CONFIG_HOME" ||
                                key == "XDG_STATE_HOME" || key == "EVENER_HUB_LISTEN_ADDR") {
                                printf "%s=", key | "cat"
                                emitted = 1
                                mode = 1
                            } else mode = 2
                        } else if (length(key) < length("EVENER_HUB_LISTEN_ADDR")) {
                            key = key sprintf("%c", b)
                        } else mode = 2
                    }
                }
            }
        }
        END {
            # BusyBox awk does not reliably report direct stdout write errors.
            # cat has constant argv, receives only selected bytes, and checks writes.
            if (emitted && close("cat") != 0) exit 1
            if (bad || !done || active) exit 1
        }
    '
)`
}

func (m *Manager) ownerEnvironment(ctx context.Context, host hostreg.Host, pid string) ([]string, error) {
	out, err := m.ownerRead(ctx, host, ownerEnvironmentRemote(pid))
	if err != nil {
		return nil, err
	}
	return decodeOwnerEnvironment(out)
}

func decodeOwnerEnvironment(out []byte) ([]string, error) {
	var env []string
	seen := map[string]bool{}
	for value := range bytes.SplitSeq(bytes.TrimSuffix(out, []byte{0}), []byte{0}) {
		if len(value) == 0 {
			continue
		}
		key, _, ok := strings.Cut(string(value), "=")
		if !ok || seen[key] || !slices.Contains(ownerEnvKeys, key) {
			return nil, fmt.Errorf("%w: ambiguous process environment", errOwnerEvidence)
		}
		seen[key] = true
		env = append(env, string(value))
	}
	slices.Sort(env)
	return env, nil
}

var ownerEnvKeys = []string{"HOME", "XDG_CONFIG_HOME", "XDG_STATE_HOME", "EVENER_HUB_LISTEN_ADDR"}

func ownerContext(cwd string, env []string) string {
	var remote strings.Builder
	remote.WriteString("CDPATH='' cd -P -- " + shellquote.RemoteWord(cwd) + " || exit; unset " + strings.Join(ownerEnvKeys, " ") + "; ")
	for _, value := range env {
		remote.WriteString("export " + shellquote.RemoteWord(value) + "; ")
	}
	return remote.String()
}

// Reuse the remote resolver in the process's cwd and narrow relevant environment,
// not the SSH login defaults. It rereads current TOML, including intentional edits.
func (m *Manager) checkOwnerConfig(ctx context.Context, host hostreg.Host, argv []string, cwd string, env []string, selected hubendpoint.Report) error {
	return m.checkOwnerConfigUsing(ctx, host, argv, cwd, env, selected, argv[0])
}

func (m *Manager) checkOwnerConfigUsing(ctx context.Context, host hostreg.Host, argv []string, cwd string, env []string, selected hubendpoint.Report, executable string, checks ...func(hubendpoint.Report) error) error {
	args := []string{executable, "hub", "endpoint", "--json"}
	if config, supplied := ownerConfigFlag(argv); supplied {
		if path.IsAbs(config) && config != selected.ConfigPath {
			// Retain the argv sanity check even if the process resolver reports
			// the selection. Compare actual files, not absolute path spellings.
			matches, err := m.configPathMatches(ctx, host, config, selected.ConfigPath, ownerContext(cwd, env))
			if err != nil {
				return err
			}
			if !matches {
				return fmt.Errorf("%w: process config disagrees with selection", errOwnerSelectionMismatch)
			}
		}
		args = append(args, "--config", config)
	} else if hasConfigFlag(argv) {
		return fmt.Errorf("%w: ambiguous config selector", errOwnerEvidence)
	}
	if addr, present := hubAddrFlag(argv); present {
		args = append(args, "--addr", addr)
	}
	words := make([]string, len(args))
	for i, arg := range args {
		words[i] = shellquote.RemoteWord(arg)
	}
	out, err := m.ownerRead(ctx, host, ownerContext(cwd, env)+strings.Join(words, " "))
	if err != nil {
		return err
	}
	report, err := hubendpoint.DecodeReport(bytes.NewReader(out))
	if err != nil {
		return fmt.Errorf("%w: process selection report is invalid", errOwnerEvidence)
	}
	matches, err := m.configPathMatches(ctx, host, report.ConfigPath, selected.ConfigPath, ownerContext(cwd, env))
	if err != nil {
		return err
	}
	if !matches {
		return fmt.Errorf("%w: process config/root disagrees with selection", errOwnerSelectionMismatch)
	}
	if report.HubStateRoot != selected.HubStateRoot {
		return errOwnerRootMismatch
	}
	for _, check := range checks {
		if err := check(report); err != nil {
			return err
		}
	}
	return nil
}

func hasConfigFlag(argv []string) bool {
	for _, arg := range argv[2:] {
		if arg == "--config" || arg == "-config" || strings.HasPrefix(arg, "--config=") || strings.HasPrefix(arg, "-config=") {
			return true
		}
	}
	return false
}

// A definition checksum avoids exporting arbitrary supervisor environment values.
// MainPID and other runtime properties are deliberately excluded. Changes to a
// unit's launch settings invalidate a pending replacement, not listener TOML edits.
func (m *Manager) serviceDefinition(ctx context.Context, host hostreg.Host, sup supervisor) (string, error) {
	out, err := m.ownerRead(ctx, host, serviceDefinitionRemote(sup))
	if err != nil {
		return "", err
	}
	fields := strings.Fields(string(out))
	if len(fields) < 2 || len(fields)%2 != 0 {
		return "", fmt.Errorf("%w: service definition unavailable", errOwnerEvidence)
	}
	for _, field := range fields {
		if !isNumericUID(field) {
			return "", fmt.Errorf("%w: service definition unavailable", errOwnerEvidence)
		}
	}
	return strings.Join(fields, " "), nil
}

func serviceDefinitionRemote(sup supervisor) string {
	remote := "systemctl"
	if sup.kind == supervisorSystemdUser {
		remote += " --user"
	}
	manager := remote
	remote += " show --property=User,ExecStart,WorkingDirectory,Environment,EnvironmentFiles,PassEnvironment,UnsetEnvironment,SetLoginEnvironment -- " + shellquote.RemoteWord(sup.label)
	// ExecStart's runtime suffix contains changing PID/timestamps. Its path and
	// argv, not that suffix, are definition context. Freeze environment-file
	// contents too, since changing XDG_STATE_HOME can select a different owner.
	return "definition=$(" + remote + ") || exit; printf '%s\\n' \"$definition\" | sed 's/ ; start_time=.*$/ ; }/' | cksum; " +
		"manager_env=$(" + manager + " show-environment) || exit; printf '%s\\n' \"$manager_env\" | grep -E '^(HOME|XDG_CONFIG_HOME|XDG_STATE_HOME|EVENER_HUB_LISTEN_ADDR)=' | LC_ALL=C sort | cksum; " +
		"files=$(printf '%s\\n' \"$definition\" | sed -n 's/^EnvironmentFiles=//p'); set -f; set -- $files; " +
		"while [ \"$#\" -gt 0 ]; do [ \"$#\" -ge 2 ] || exit 1; file=$1; mode=$2; shift 2; " +
		"case \"$file\" in /*) ;; *) exit 1 ;; esac; " +
		"case \"$mode\" in '(ignore_errors=yes)'|'(ignore_errors=no)') ;; *) exit 1 ;; esac; " +
		"if [ -f \"$file\" ]; then cksum < \"$file\" || exit; " +
		"elif [ \"$mode\" = '(ignore_errors=yes)' ]; then printf '0 0\\n'; else exit 1; fi; done"
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
		return out, fmt.Errorf("%w: host %q owner probe: %w", errOwnerEvidence, host.Name, err)
	}
	return out, nil
}

// Linux /proc/locks identifies the process holding this existing inode, not any
// listener on a guessed port. stat's decimal st_dev is split using Linux dev_t's
// major/minor encoding to match proc_locks' hexadecimal device fields. Never
// create, unlink, replace or acquire hub.lock in an observer.
// Unrelated lock churn can hide a held lock in a successful global-list read.
// Retry only byte-empty results, at most three reads in this one SSH command.
// This mitigates availability, not snapshot completeness. Pin the original
// identity before/after each read, including immediately before acceptance.
func selectedLockRemote(root string) string {
	return "lock=" + shellquote.RemoteWord(path.Join(root, "hub.lock")) + "; identity=$(test ! -L \"$lock\" && test -f \"$lock\" && stat -Lc '%d %i' \"$lock\") || exit; set -- $identity; [ \"$#\" -eq 2 ] || exit 1; " +
		"dev=$1; ino=$2; major=$(( ((dev >> 8) & 4095) | ((dev >> 32) & 4294963200) )); minor=$(( (dev & 255) | ((dev >> 12) & 4294967040) )); " +
		"key=$(printf '%02x:%02x:%s' \"$major\" \"$minor\" \"$ino\"); " +
		"for attempt in 1 2 3; do " +
		"current=$(test ! -L \"$lock\" && test -f \"$lock\" && stat -Lc '%d %i' \"$lock\") || exit; [ \"$current\" = \"$identity\" ] || exit 1; " +
		// The sentinel preserves trailing newlines so malformed nonempty output
		// cannot become empty through command substitution and trigger a retry.
		"owners=$(awk -v key=\"$key\" '$2 == \"FLOCK\" && $3 == \"ADVISORY\" && $4 == \"WRITE\" && $6 == key { print $5 }' /proc/locks && printf '.') || exit; owners=${owners%.}; " +
		"current=$(test ! -L \"$lock\" && test -f \"$lock\" && stat -Lc '%d %i' \"$lock\") || exit; [ \"$current\" = \"$identity\" ] || exit 1; " +
		"if [ -n \"$owners\" ]; then printf '%s' \"$owners\"; exit; fi; done"
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
	return m.probeOwnerImageBuild(ctx, host, pid, "")
}

func (m *Manager) probeOwnerImageBuild(ctx context.Context, host hostreg.Host, pid, version string) error {
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
	if version != "" && contract.Version != version {
		return fmt.Errorf("%w: running image build differs from pending replacement", errOwnerEvidence)
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
	return config, count == 1 && config != ""
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
func (m *Manager) supervisorForOwner(ctx context.Context, host hostreg.Host, pid string, argv []string, cwd string, env []string, report hubendpoint.Report, only ...supervisor) (supervisor, []string, error) {
	var candidates []supervisor
	for _, scope := range []struct {
		kind   supervisorKind
		remote string
	}{{supervisorSystemd, systemctlListUnits}, {supervisorSystemdUser, systemctlListUnitsUser}} {
		out, err := m.ownerRead(ctx, host, scope.remote)
		if err != nil {
			if !supervisorListingAbsent(out) && (scope.kind != supervisorSystemdUser || !systemdUserBusAbsent(out)) {
				return supervisor{}, nil, err
			}
			continue
		}
		live, dormant := parseSystemdHubs(out)
		for _, label := range append(live, dormant...) {
			candidate := supervisor{kind: scope.kind, label: label}
			if len(only) == 0 || candidate == only[0] {
				candidates = append(candidates, candidate)
			}
		}
	}
	var chosen supervisor
	var chosenArgv []string
	for _, candidate := range candidates {
		out, err := m.ownerRead(ctx, host, supervisorShowRemote(candidate))
		if err != nil {
			return supervisor{}, nil, err
		}
		fields := map[string]string{}
		for line := range strings.SplitSeq(strings.TrimSuffix(string(out), "\n"), "\n") {
			key, value, ok := strings.Cut(line, "=")
			if !ok {
				return supervisor{}, nil, fmt.Errorf("%w: malformed service definition", errOwnerEvidence)
			}
			if _, exists := fields[key]; exists {
				return supervisor{}, nil, fmt.Errorf("%w: duplicate service property", errOwnerEvidence)
			}
			fields[key] = value
		}
		for _, key := range []string{"Id", "MainPID", "User", "ExecStart", "WorkingDirectory"} {
			if _, ok := fields[key]; !ok {
				return supervisor{}, nil, fmt.Errorf("%w: incomplete service definition", errOwnerEvidence)
			}
		}
		if fields["Id"] != candidate.label || !isNumericUID(fields["MainPID"]) {
			return supervisor{}, nil, fmt.Errorf("%w: service identity unavailable", errOwnerEvidence)
		}
		if fields["MainPID"] != pid {
			continue
		}
		if strings.Count(fields["ExecStart"], "{ path=") != 1 {
			return supervisor{}, nil, fmt.Errorf("%w: service executable association is ambiguous", errOwnerEvidence)
		}
		// systemctl's formatted argv is not a general shell serialization. Accept
		// only an exact corroboration of the independently recovered NUL argv with
		// simple words. Refuse ambiguous formatting instead of inventing boundaries.
		for _, arg := range argv {
			if strings.ContainsAny(arg, " \t\r\n\\\"';$") {
				return supervisor{}, nil, fmt.Errorf("%w: service argv formatting is ambiguous", errOwnerEvidence)
			}
		}
		_, definitionArgv, err := serviceWords(out)
		if err != nil {
			return supervisor{}, nil, err
		}
		definitionPrefix := "{ path=" + definitionArgv[0] + " ; argv[]=" + strings.Join(definitionArgv, " ") + " ; "
		if !strings.HasPrefix(fields["ExecStart"], definitionPrefix) {
			return supervisor{}, nil, fmt.Errorf("%w: service argv formatting is ambiguous", errOwnerEvidence)
		}
		argvMatches := m.supervisorArgvMatches(ctx, host, pid, argv, definitionArgv, report, env, false)
		if pid == "0" && !argvMatches {
			continue
		}
		user, ok := effectiveUserName(host)
		if !ok {
			userOut, err := m.ownerRead(ctx, host, "id -un")
			if err != nil {
				return supervisor{}, nil, err
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
			workingDirectory = "/"
			if candidate.kind == supervisorSystemdUser {
				out, err := m.ownerRead(ctx, host, "getent passwd "+shellquote.RemoteWord(user)+" | cut -d: -f6")
				if err != nil {
					return supervisor{}, nil, fmt.Errorf("%w: user-service home unavailable", errOwnerEvidence)
				}
				workingDirectory, err = ownerPathOutput(out)
				if err != nil {
					return supervisor{}, nil, err
				}
			}
		}
		userMatches, err := m.sameUser(ctx, host, definitionUser, user)
		if err != nil {
			return supervisor{}, nil, err
		}
		directoryMatches, err := m.sameDirectory(ctx, host, workingDirectory, cwd)
		if err != nil {
			return supervisor{}, nil, err
		}
		if !userMatches || !directoryMatches || !argvMatches || !strings.HasSuffix(fields["ExecStart"], " }") {
			return supervisor{}, nil, fmt.Errorf("%w: service definition disagrees with selected process", errOwnerEvidence)
		}
		if chosen.kind != supervisorNone {
			return supervisor{}, nil, fmt.Errorf("%w: multiple services associate with selected process", errOwnerEvidence)
		}
		chosen, chosenArgv = candidate, definitionArgv
	}
	return chosen, chosenArgv, nil
}
