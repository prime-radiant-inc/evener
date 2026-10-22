package sshconn

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"primeradiant.com/evener/cmd/evener-hub/internal/hostreg"
	"primeradiant.com/evener/execsupport/shellquote"
	"primeradiant.com/evener/internal/hubendpoint"
)

func serviceWords(out []byte) (map[string]string, []string, error) {
	fields := map[string]string{}
	for line := range strings.SplitSeq(strings.TrimSuffix(string(out), "\n"), "\n") {
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			return nil, nil, errOwnerEvidence
		}
		if _, exists := fields[key]; exists {
			return nil, nil, errOwnerEvidence
		}
		fields[key] = value
	}
	exec := fields["ExecStart"]
	if strings.Count(exec, "{ path=") != 1 {
		return nil, nil, errOwnerEvidence
	}
	_, args, ok := strings.Cut(exec, " ; argv[]=")
	if !ok {
		return nil, nil, errOwnerEvidence
	}
	args, _, ok = strings.Cut(args, " ; ")
	if !ok || strings.ContainsAny(args, "\\\"';$") {
		return nil, nil, errOwnerEvidence
	}
	argv := strings.Fields(args)
	if validateHubArgv(argv) != nil || !strings.HasPrefix(exec, "{ path="+argv[0]+" ; argv[]=") {
		return nil, nil, errOwnerEvidence
	}
	return fields, argv, nil
}

// Only positive selection mismatch makes a launchd candidate unrelated. Unknown
// executable/config evidence cannot authorize either a cold start or a replay.
func (m *Manager) launchdOwnerMatches(ctx context.Context, host hostreg.Host, def launchdDefinition, report hubendpoint.Report) (bool, error) {
	err := m.hubExecutableMatches(ctx, host, def.program)
	if err == nil {
		err = m.checkOwnerConfig(ctx, host, def.argv, def.cwd, def.env, report)
	}
	if errors.Is(err, errOwnerSelectionMismatch) && !errors.Is(err, errOwnerRootMismatch) {
		return false, nil
	}
	return err == nil, err
}

// A stopped service with no retained operation can be started only after the
// service itself is resolved to this config/root. No predecessor capability is
// inferred: there is no predecessor, and disk capability was checked by endpoint.
func (m *Manager) resolveStoppedOwner(ctx context.Context, host hostreg.Host, facts Preflight, report hubendpoint.Report) (selectedOwner, error) {
	if facts.OS == "darwin" {
		defs, err := m.launchdOwners(ctx, host, facts)
		if err != nil {
			return selectedOwner{}, err
		}
		var chosen *selectedOwner
		for sup, def := range defs {
			matches, err := m.launchdOwnerMatches(ctx, host, def, report)
			if err != nil {
				return selectedOwner{}, err
			}
			if !matches {
				continue
			}
			if def.pid != "0" {
				return selectedOwner{}, fmt.Errorf("%w: selected stopped root has a live service", errOwnerEvidence)
			}
			if chosen != nil {
				return selectedOwner{}, errOwnerEvidence
			}
			physicalCwd, err := m.directoryIdentity(ctx, host, def.cwd)
			if err != nil {
				return selectedOwner{}, err
			}
			o := selectedOwner{host: host, pid: "0", supervisor: sup, argv: def.argv, launchArgv: slices.Clone(def.argv), cwd: def.cwd, cwdIdentity: physicalCwd, env: def.env, report: report, definition: launchdIntent(def), domainUID: facts.UID}
			chosen = &o
		}
		if chosen == nil {
			return m.freshSelectedOwner(ctx, host, report)
		}
		return chosen.clone(), nil
	}
	if facts.OS != "linux" {
		return selectedOwner{}, errOwnerEvidence
	}
	var chosen *selectedOwner
	for _, scope := range []struct {
		kind   supervisorKind
		remote string
	}{{supervisorSystemd, systemctlListUnits}, {supervisorSystemdUser, systemctlListUnitsUser}} {
		out, err := m.ownerRead(ctx, host, scope.remote)
		if err != nil {
			if supervisorListingAbsent(out) || (scope.kind == supervisorSystemdUser && systemdUserBusAbsent(out)) {
				continue
			}
			return selectedOwner{}, err
		}
		live, dormant := parseSystemdHubs(out)
		labels := slices.Concat(live, dormant)
		for _, label := range labels {
			sup := supervisor{kind: scope.kind, label: label}
			out, err := m.ownerRead(ctx, host, supervisorShowRemote(sup))
			if err != nil {
				return selectedOwner{}, err
			}
			fields, argv, err := serviceWords(out)
			if err != nil {
				return selectedOwner{}, err
			}
			if fields["Id"] != label {
				return selectedOwner{}, errOwnerEvidence
			}
			if err := m.hubExecutableMatches(ctx, host, argv[0]); err != nil {
				if errors.Is(err, errOwnerSelectionMismatch) {
					continue
				}
				return selectedOwner{}, err
			}
			user, ok := effectiveUserName(host)
			if !ok {
				out, err := m.ownerRead(ctx, host, "id -un")
				if err != nil {
					return selectedOwner{}, err
				}
				user = strings.TrimSpace(string(out))
			}
			serviceUser := fields["User"]
			if serviceUser == "" {
				serviceUser = "root"
				if scope.kind == supervisorSystemdUser {
					serviceUser = user
				}
			}
			userMatches, err := m.sameUser(ctx, host, serviceUser, user)
			if err != nil {
				return selectedOwner{}, err
			}
			if !userMatches {
				continue
			}
			env, err := m.stoppedServiceEnvironment(ctx, host, sup, user)
			if err != nil {
				return selectedOwner{}, err
			}
			cwd := fields["WorkingDirectory"]
			if cwd == "" {
				cwd = "/"
				if scope.kind == supervisorSystemdUser {
					out, err := m.ownerRead(ctx, host, "getent passwd "+shellquote.RemoteWord(user)+" | cut -d: -f6")
					if err != nil {
						return selectedOwner{}, fmt.Errorf("%w: user-service home unavailable", errOwnerEvidence)
					}
					cwd, err = ownerPathOutput(out)
					if err != nil {
						return selectedOwner{}, err
					}
				}
			}
			if err := m.checkOwnerConfig(ctx, host, argv, cwd, env, report); err != nil {
				if errors.Is(err, errOwnerSelectionMismatch) && !errors.Is(err, errOwnerRootMismatch) {
					continue
				}
				return selectedOwner{}, err
			}
			if fields["MainPID"] != "0" {
				return selectedOwner{}, fmt.Errorf("%w: selected stopped root has a live service", errOwnerEvidence)
			}
			definition, err := m.serviceDefinition(ctx, host, sup)
			if err != nil {
				return selectedOwner{}, err
			}
			if chosen != nil {
				return selectedOwner{}, fmt.Errorf("%w: multiple stopped selected services", errOwnerEvidence)
			}
			physicalCwd, err := m.directoryIdentity(ctx, host, cwd)
			if err != nil {
				return selectedOwner{}, err
			}
			o := selectedOwner{host: host, pid: "0", supervisor: sup, argv: argv, launchArgv: slices.Clone(argv), cwd: cwd, cwdIdentity: physicalCwd, env: env, report: report, definition: definition}
			chosen = &o
		}
	}
	if chosen == nil {
		return m.freshSelectedOwner(ctx, host, report)
	}
	return chosen.clone(), nil
}

// systemctl's environment serialization is shell-word-like, not a command to
// eval. The remote awk lexer strips quoting while exporting only the four named
// inputs. A construct it cannot unambiguously decode refuses without disclosure.
const narrowEnvironmentFilter = `awk '
function emit() { if (word ~ /^(HOME|XDG_CONFIG_HOME|XDG_STATE_HOME|EVENER_HUB_LISTEN_ADDR)=/) { total+=length(word)+1; if (total > 65536) exit 1; printf "%s%c", word, 0 } word="" }
{ if (length($0) > 65536) exit 1; for (i=1;i<=length($0);i++) {
 c=substr($0,i,1)
 if (escape) { if (c != "\\" && c != "\042" && c != "\047" && c != " ") exit 1; word=word c; escape=0; continue }
 if (c == "\\") { escape=1; continue }
 if (quote != "") { if (c == quote) quote=""; else word=word c; continue }
 if (c == "\042" || c == "\047") { quote=c; continue }
 if (c == " " || c == "\t") { emit(); continue }
 word=word c
 } if (quote != "" || escape) exit 1; emit() }
END { if (quote != "" || escape) exit 1 }'`

func (m *Manager) stoppedServiceEnvironment(ctx context.Context, host hostreg.Host, sup supervisor, user string) ([]string, error) {
	remote := "systemctl"
	if sup.kind == supervisorSystemdUser {
		remote += " --user"
	}
	// Resolve the next launch independently of the running process. A stable
	// definition may already select a different root than that process serves.
	fileOut, err := m.ownerRead(ctx, host, remote+" show --property=User,EnvironmentFiles,PassEnvironment,UnsetEnvironment,SetLoginEnvironment -- "+shellquote.RemoteWord(sup.label))
	if err != nil {
		return nil, err
	}
	properties := map[string]string{}
	for line := range strings.SplitSeq(strings.TrimSuffix(string(fileOut), "\n"), "\n") {
		key, value, ok := strings.Cut(line, "=")
		_, duplicate := properties[key]
		if !ok || duplicate || !slices.Contains([]string{"User", "EnvironmentFiles", "PassEnvironment", "UnsetEnvironment", "SetLoginEnvironment"}, key) {
			return nil, fmt.Errorf("%w: service environment properties unavailable", errOwnerEvidence)
		}
		properties[key] = value
	}
	// Before systemd 255 this property was absent and login environment was on.
	if _, present := properties["SetLoginEnvironment"]; !present {
		properties["SetLoginEnvironment"] = "yes"
	}
	if len(properties) != 5 {
		return nil, fmt.Errorf("%w: stopped service environment properties unavailable", errOwnerEvidence)
	}
	env := map[string]string{}
	switch properties["SetLoginEnvironment"] {
	case "yes":
		if sup.kind == supervisorSystemd {
			user = properties["User"]
			if user == "" {
				// A system unit without User runs as root but has no HOME seed.
				break
			}
		}
		out, err := m.ownerRead(ctx, host, "getent passwd "+shellquote.RemoteWord(user)+" | cut -d: -f6")
		if err != nil {
			return nil, err
		}
		home, err := ownerPathOutput(out)
		if err != nil {
			return nil, errOwnerEvidence
		}
		env["HOME"] = home
	case "no":
	default:
		return nil, fmt.Errorf("%w: service login environment policy unavailable", errOwnerEvidence)
	}
	merge := func(out []byte, allow func(string) bool) error {
		if len(out) == 0 {
			return nil
		}
		if len(out) > 65536 || out[len(out)-1] != 0 {
			return errOwnerEvidence
		}
		for entry := range strings.SplitSeq(string(out[:len(out)-1]), "\x00") {
			key, value, ok := strings.Cut(entry, "=")
			if !ok || !slices.Contains(ownerEnvKeys, key) || strings.ContainsAny(value, "\r\n") {
				return errOwnerEvidence
			}
			if allow == nil || allow(key) {
				env[key] = value
			}
		}
		return nil
	}
	pass := strings.Fields(properties["PassEnvironment"])
	for _, key := range pass {
		if strings.ContainsAny(key, "=\\\"'%") {
			return nil, fmt.Errorf("%w: unsupported PassEnvironment serialization", errOwnerEvidence)
		}
	}
	if sup.kind == supervisorSystemdUser || len(pass) != 0 {
		inherited, err := m.ownerRead(ctx, host, "values=$("+remote+" show-environment) || exit; printf '%s\\n' \"$values\" | "+narrowEnvironmentFilter)
		if err != nil {
			return nil, err
		}
		if err := merge(inherited, func(key string) bool { return sup.kind == supervisorSystemdUser || slices.Contains(pass, key) }); err != nil {
			return nil, err
		}
	}
	out, err := m.ownerRead(ctx, host, "values=$("+remote+" show --property=Environment --value -- "+shellquote.RemoteWord(sup.label)+") || exit; printf '%s\\n' \"$values\" | "+narrowEnvironmentFilter)
	if err != nil {
		return nil, err
	}
	if strings.ContainsRune(string(out), '%') {
		return nil, fmt.Errorf("%w: unresolved Environment specifier", errOwnerEvidence)
	}
	if err := merge(out, nil); err != nil {
		return nil, err
	}
	files := strings.Fields(properties["EnvironmentFiles"])
	if len(files)%2 != 0 || len(files) > 128 {
		return nil, fmt.Errorf("%w: unsupported EnvironmentFiles serialization", errOwnerEvidence)
	}
	for i := 0; i < len(files); i += 2 {
		path, mode := files[i], files[i+1]
		if !strings.HasPrefix(path, "/") || strings.ContainsAny(path, "\\\"'%*?[]") || (mode != "(ignore_errors=yes)" && mode != "(ignore_errors=no)") {
			return nil, fmt.Errorf("%w: unsupported EnvironmentFile path", errOwnerEvidence)
		}
		out, err := m.ownerRead(ctx, host, systemdEnvironmentFileRemote(path, mode == "(ignore_errors=yes)"))
		if err != nil {
			return nil, fmt.Errorf("%w: cannot resolve EnvironmentFile: %w", errOwnerEvidence, err)
		}
		out, err = parseSystemdEnvironmentFile(out)
		if err != nil {
			return nil, err
		}
		if err := merge(out, nil); err != nil {
			return nil, err
		}
	}
	for unset := range strings.FieldsSeq(properties["UnsetEnvironment"]) {
		if strings.ContainsAny(unset, "\\\"'%") {
			return nil, fmt.Errorf("%w: unsupported UnsetEnvironment serialization", errOwnerEvidence)
		}
		key, value, exact := strings.Cut(unset, "=")
		if !exact || env[key] == value {
			delete(env, key)
		}
	}
	var result []string
	for key, value := range env {
		result = append(result, key+"="+value)
	}
	slices.Sort(result)
	return result, nil
}
