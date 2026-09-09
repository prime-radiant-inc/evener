package hub

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"sort"
	"strings"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/plugin"
	"primeradiant.com/evener/agent/skill"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/cmd/evener-hub/internal/launchconfig"
	"primeradiant.com/evener/envvars/userdirs"
)

// hubSpawnSlashCatalog answers evener/spawn/slashCatalog: the slash inventory
// (commands + skills) a session started with these params would offer. It
// resolves launch config the way thread/start does (minus model handling: no
// scalars exist on these params and no model is required), then runs the same
// discovery session init runs: plugin dirs fail-soft, evener-wide project +
// user-global commands, and the embedded -> user -> project+extra -> plugin
// skill layering. Discovery only reads manifests, markdown, and SKILL.md
// frontmatter: it starts no session and executes nothing.
func hubSpawnSlashCatalog(ctx context.Context, cfg hubcore.WebConfig, params appwire.SpawnSlashCatalogParams) (appwire.SpawnSlashCatalogResponse, error) {
	harness := strings.TrimSpace(params.Harness)
	if harness != "" && harness != "evener" {
		return appwire.SpawnSlashCatalogResponse{Commands: []appwire.CommandDescriptor{}, Skills: []appwire.EvenerSkillInfo{}}, nil
	}
	var overrides launchconfig.Layer
	if params.LaunchOverrides != nil {
		overrides = launchconfig.FromWire(*params.LaunchOverrides)
	}
	var (
		resolved     launchconfig.Resolved
		canonicalCWD string
	)
	if strings.TrimSpace(params.CWD) == "" {
		userResolved, err := launchconfig.ResolveUserOnly(hubLaunchConfigRoot(cfg), overrides)
		if err != nil {
			return appwire.SpawnSlashCatalogResponse{}, err
		}
		resolved = userResolved
	} else {
		canonical, err := hubCanonicalizeDir(params.CWD)
		if err != nil {
			return appwire.SpawnSlashCatalogResponse{}, appwire.InvalidParams("cwd: " + err.Error())
		}
		canonicalCWD = canonical
		fullResolved, err := hubResolveLaunch(hubLaunchConfigRoot(cfg), canonical, overrides)
		if err != nil {
			return appwire.SpawnSlashCatalogResponse{}, err
		}
		resolved = fullResolved
	}
	pluginDirs := resolved.Effective.PluginDirs
	if resolution, err := hubResolvePlugins(ctx, cfg.PluginRoot, resolved.Effective.PluginDirs, resolved.Effective.EnabledPlugins); err != nil {
		// Same admission rule thread/start uses (app_threadlifecycle.go): a
		// resolver failure is fatal when a selection must be honored, and
		// always when the failure IS the caller leaving (canceled/deadline on
		// the error itself, not the ambient context). Everything else falls
		// through with whatever the resolver could list.
		if resolved.Effective.EnabledPlugins != nil ||
			errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return appwire.SpawnSlashCatalogResponse{}, appwire.HubLaunchError(err.Error())
		}
		_, _ = fmt.Fprintf(os.Stderr, "warning: resolving plugins for slash catalog: %v\n", err)
		pluginDirs = nil
	} else if err := resolution.ValidateSelection(); err != nil {
		return appwire.SpawnSlashCatalogResponse{}, appwire.InvalidParams(err.Error())
	} else {
		pluginDirs = resolution.SelectedDirs
	}
	loaded, _ := plugin.LoadAllFailSoft(pluginDirs)
	var env execenv.ExecutionEnvironment
	if canonicalCWD != "" {
		env = execenv.NewLocalExecutionEnvironment(canonicalCWD)
	}
	evenerwide, _ := plugin.DiscoverEvenerWideCommands(env)
	merged := plugin.MergeCommands(loaded, evenerwide)
	commands := make([]appwire.CommandDescriptor, 0, len(merged))
	for _, cmd := range merged {
		commands = append(commands, appwire.CommandDescriptor{
			Name: cmd.Name, PluginName: cmd.PluginName,
			Description: cmd.Description, ArgumentHint: cmd.ArgumentHint, Source: cmd.Source,
		})
	}
	sort.Slice(commands, func(i, j int) bool {
		if commands[i].Name != commands[j].Name {
			return commands[i].Name < commands[j].Name
		}
		if commands[i].PluginName != commands[j].PluginName {
			return commands[i].PluginName < commands[j].PluginName
		}
		return commands[i].Source < commands[j].Source
	})
	all := make(map[string]skill.SkillMeta)
	if embedded, err := skill.EmbeddedSkills(); err == nil {
		maps.Copy(all, embedded)
	}
	if userSkillsDir := userdirs.Subdir(userdirs.DefaultConfigRoot(), "skills"); userSkillsDir != "" {
		skill.ScanSkillsDir(userSkillsDir, all)
	}
	maps.Copy(all, skill.DiscoverSkills(env, resolved.Effective.SkillsDirs...))
	for _, inst := range loaded {
		maps.Copy(all, inst.Skills)
	}
	entries := skill.CatalogEntries(all)
	skills := make([]appwire.EvenerSkillInfo, 0, len(entries))
	for _, entry := range entries {
		skills = append(skills, appwire.EvenerSkillInfo{Name: entry.Name, Description: entry.Description})
	}
	return appwire.SpawnSlashCatalogResponse{Commands: commands, Skills: skills}, nil
}
