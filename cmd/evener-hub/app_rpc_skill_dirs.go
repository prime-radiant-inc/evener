package hub

import (
	"context"
	"os"
	"runtime"
	"strings"

	"primeradiant.com/evener/agent/skill"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/launchconfig"
	"primeradiant.com/evener/envvars"
	"primeradiant.com/evener/envvars/userdirs"
	"primeradiant.com/evener/internal/appserver"
)

// skillDirsResponse names the built-in skill roots a host's sessions start
// from. It resolves the host environment with the global launch layer applied
// at request time, so a saved XDG_CONFIG_HOME default changes the User config
// row the next time Settings reads it. Project and per-launch layers can still
// differ for a particular session.
func skillDirsResponse(configRoot string) (appwire.SkillDirsResponse, error) {
	resolved, err := launchconfig.ResolveUserOnly(configRoot, launchconfig.Layer{})
	if err != nil {
		return appwire.SkillDirsResponse{}, err
	}
	env := envvars.ApplyOverlay(os.Environ(), resolved.Effective.Env)
	values := make(map[string]string, len(env))
	for _, entry := range env {
		if key, value, ok := strings.Cut(entry, "="); ok {
			values[key] = value
		}
	}
	lookup := func(name string) (string, bool) {
		value, ok := values[name]
		return value, ok
	}
	home := userdirs.HomeFromLookup(runtime.GOOS, lookup)
	var response appwire.SkillDirsResponse
	if home != "" {
		response.HomeSkillsDir = skill.HomeSkillsDir(home)
	}
	if xdgConfigHome := values[envvars.XDGConfigHome.Name]; xdgConfigHome != "" || home != "" {
		root := userdirs.ConfigRoot(xdgConfigHome, func() (string, error) {
			return home, nil
		})
		response.UserSkillsDir = userdirs.UserSkills(root)
	}
	return response, nil
}

// registerSkillDirsHandlers serves evener/settings/skillDirs/get, the
// read-only half of Settings -> Skill directories. The editable skills_dirs
// list continues to live in the launch layer. The read is resolved per
// request so settings saved after startup are reflected without a restart.
func registerSkillDirsHandlers(server *appserver.Server, configRoot string) {
	appserver.HandleTyped(server.Router(), appwire.MethodEvenerSettingsSkillDirsGet,
		func(context.Context, appwire.EmptyParams) (appwire.SkillDirsResponse, error) {
			return skillDirsResponse(configRoot)
		})
}
