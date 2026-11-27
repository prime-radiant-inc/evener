package hub

import (
	"context"
	"os"

	"primeradiant.com/evener/agent/skill"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/envvars/userdirs"
	"primeradiant.com/evener/internal/appserver"
)

// skillDirsResponse names the built-in skill roots this host's sessions scan.
// Both are host facts: the browser cannot derive them from its own environment.
// Discovery resolves the same two roots from the process environment, so a root
// that does not resolve is empty here rather than a relative fallback.
func skillDirsResponse() appwire.SkillDirsResponse {
	var response appwire.SkillDirsResponse
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		response.HomeSkillsDir = skill.HomeSkillsDir(home)
	}
	if root := userdirs.DefaultConfigRoot(); root != "" {
		response.UserSkillsDir = userdirs.UserSkills(root)
	}
	return response
}

// registerSkillDirsHandlers serves evener/settings/skillDirs/get, the
// read-only half of Settings -> Skill directories. The editable skills_dirs
// list continues to live in the launch layer.
func registerSkillDirsHandlers(server *appserver.Server) {
	response := skillDirsResponse()
	appserver.HandleTyped(server.Router(), appwire.MethodEvenerSettingsSkillDirsGet,
		func(context.Context, appwire.EmptyParams) (appwire.SkillDirsResponse, error) {
			return response, nil
		})
}
