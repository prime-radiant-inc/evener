package sshconn

import (
	"context"
	"fmt"
	"path"
	"slices"
	"strings"

	"primeradiant.com/evener/cmd/evener-hub/internal/hostreg"
	"primeradiant.com/evener/execsupport/shellquote"
	"primeradiant.com/evener/internal/hubendpoint"
)

func serviceWords(out []byte) (map[string]string, []string, error) {
	fields := map[string]string{}
	for line := range strings.SplitSeq(strings.TrimSpace(string(out)), "\n") {
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
			if def.pid != "0" {
				continue
			}
			if err := m.hubExecutableMatches(ctx, host, def.program); err != nil {
				continue
			}
			if err := m.checkOwnerConfig(ctx, host, def.argv, def.cwd, def.env, report); err != nil {
				continue
			}
			if chosen != nil {
				return selectedOwner{}, errOwnerEvidence
			}
			o := selectedOwner{host: host, pid: "0", supervisor: sup, argv: def.argv, cwd: def.cwd, env: def.env, report: report, definition: launchdIntent(def), domainUID: facts.UID}
			chosen = &o
		}
		if chosen == nil {
			return selectedOwner{}, errOwnerEvidence
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
		_, dormant := parseSystemdHubs(out)
		for _, label := range dormant {
			sup := supervisor{kind: scope.kind, label: label}
			out, err := m.ownerRead(ctx, host, supervisorShowRemote(sup))
			if err != nil {
				return selectedOwner{}, err
			}
			fields, argv, err := serviceWords(out)
			if err != nil {
				return selectedOwner{}, err
			}
			if fields["Id"] != label || fields["MainPID"] != "0" {
				return selectedOwner{}, errOwnerEvidence
			}
			if config, ok := ownerConfigFlag(argv); ok && path.IsAbs(config) && config != report.ConfigPath {
				continue
			}
			if err := m.hubExecutableMatches(ctx, host, argv[0]); err != nil {
				continue
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
			if serviceUser != user {
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
					for _, v := range env {
						if home, ok := strings.CutPrefix(v, "HOME="); ok {
							cwd = home
						}
					}
				}
			}
			if err := m.checkOwnerConfig(ctx, host, argv, cwd, env, report); err != nil {
				continue
			}
			definition, err := m.serviceDefinition(ctx, host, sup)
			if err != nil {
				return selectedOwner{}, err
			}
			if chosen != nil {
				return selectedOwner{}, fmt.Errorf("%w: multiple stopped selected services", errOwnerEvidence)
			}
			o := selectedOwner{host: host, pid: "0", supervisor: sup, argv: argv, cwd: cwd, env: env, report: report, definition: definition}
			chosen = &o
		}
	}
	if chosen == nil {
		return selectedOwner{}, fmt.Errorf("%w: no stopped selected service or retained launch", errOwnerEvidence)
	}
	return chosen.clone(), nil
}

// systemctl's environment serialization is shell-word-like, not a command to
// eval. The remote awk lexer strips quoting while exporting only the four named
// inputs. A construct it cannot unambiguously decode refuses without disclosure.
const narrowEnvironmentFilter = `awk '
function emit() { if (word ~ /^(HOME|XDG_CONFIG_HOME|XDG_STATE_HOME|EVENER_HUB_LISTEN_ADDR)=/) printf "%s%c", word, 0; word="" }
{ for (i=1;i<=length($0);i++) {
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
	out, err := m.ownerRead(ctx, host, "getent passwd "+shellquote.RemoteWord(user)+" | cut -d: -f6")
	if err != nil {
		return nil, err
	}
	home := strings.TrimSpace(string(out))
	if !path.IsAbs(home) {
		return nil, errOwnerEvidence
	}
	remote := "systemctl"
	if sup.kind == supervisorSystemdUser {
		remote += " --user"
	}
	// An EnvironmentFile or nontrivial manager inheritance needs retained live
	// intent, rather than guessing a fresh stopped launch's environment.
	fileOut, err := m.ownerRead(ctx, host, remote+" show --property=EnvironmentFiles,PassEnvironment,UnsetEnvironment -- "+shellquote.RemoteWord(sup.label))
	if err != nil {
		return nil, err
	}
	properties := map[string]bool{}
	for line := range strings.SplitSeq(strings.TrimSpace(string(fileOut)), "\n") {
		key, value, ok := strings.Cut(line, "=")
		if !ok || value != "" || properties[key] || !slices.Contains([]string{"EnvironmentFiles", "PassEnvironment", "UnsetEnvironment"}, key) {
			return nil, fmt.Errorf("%w: stopped service requires retained environment intent", errOwnerEvidence)
		}
		properties[key] = true
	}
	if len(properties) != 3 {
		return nil, fmt.Errorf("%w: stopped service environment properties unavailable", errOwnerEvidence)
	}
	var inherited []byte
	if sup.kind == supervisorSystemdUser {
		inherited, err = m.ownerRead(ctx, host, "values=$("+remote+" show-environment) || exit; printf '%s\\n' \"$values\" | "+narrowEnvironmentFilter)
		if err != nil {
			return nil, err
		}
	}
	out, err = m.ownerRead(ctx, host, "values=$("+remote+" show --property=Environment --value -- "+shellquote.RemoteWord(sup.label)+") || exit; printf '%s\\n' \"$values\" | "+narrowEnvironmentFilter)
	if err != nil {
		return nil, err
	}
	out = append(inherited, out...)
	env := []string{"HOME=" + home}
	if len(out) > 0 {
		for value := range strings.SplitSeq(strings.TrimSuffix(string(out), "\x00"), "\x00") {
			key, _, ok := strings.Cut(value, "=")
			if !ok || !slices.Contains(ownerEnvKeys, key) {
				return nil, errOwnerEvidence
			}
			for i, previous := range env {
				if strings.HasPrefix(previous, key+"=") {
					env = append(env[:i], env[i+1:]...)
					break
				}
			}
			env = append(env, value)
		}
	}
	slices.Sort(env)
	return env, nil
}
