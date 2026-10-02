package hub

import (
	"context"
	"errors"
	"fmt"
	"os"
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
	var preview launchPreview
	var err error
	if strings.TrimSpace(params.CWD) == "" {
		preview, err = prepareLaunchPreview(hubLaunchConfigRoot(cfg), params.CWD, overrides)
	} else {
		canonical, canonicalErr := hubCanonicalizeDir(params.CWD)
		if canonicalErr != nil {
			// A not-yet-created spawn directory uses a disposable probe with
			// the eventual target's identity, isolating ancestor-local layers.
			if !errors.Is(canonicalErr, os.ErrNotExist) {
				return appwire.SpawnSlashCatalogResponse{}, appwire.InvalidParams("cwd: " + canonicalErr.Error())
			}
			preview, err = prepareLaunchPreview(hubLaunchConfigRoot(cfg), params.CWD, overrides)
		} else {
			// Existing directories retain slash's resolver error classification
			// and injectable canonicalization/resolution seams.
			preview.cwd = canonical
			preview.resolved, err = hubResolveLaunch(hubLaunchConfigRoot(cfg), canonical, overrides)
			preview.cleanup = func() {}
		}
	}
	if err != nil {
		return appwire.SpawnSlashCatalogResponse{}, err
	}
	defer preview.cleanup()
	// thread/start launches with spawnResolved.Effective.PluginDirs carried on
	// the Resolved value (the resolver's own SelectedDirs never reach the
	// child), so the fallthrough below keeps the same dirs: the catalog then
	// shows what the resulting session loads instead of going empty.
	pluginDirs := preview.resolved.Effective.PluginDirs
	if resolution, err := hubResolvePlugins(ctx, cfg.PluginRoot, preview.resolved.Effective.PluginDirs, preview.resolved.Effective.EnabledPlugins, cfg.PluginManager); err != nil {
		// Same admission rule thread/start uses (app_threadlifecycle.go): a
		// resolver failure is fatal when a selection must be honored, and
		// always when the failure IS the caller leaving (canceled/deadline on
		// the error itself, not the ambient context). Everything else falls
		// through with the effective plugin dirs above.
		if preview.resolved.Effective.EnabledPlugins != nil ||
			errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return appwire.SpawnSlashCatalogResponse{}, appwire.HubLaunchError(err.Error())
		}
		_, _ = fmt.Fprintf(os.Stderr, "warning: resolving plugins for slash catalog: %v\n", err)
	} else if err := resolution.ValidateSelection(); err != nil {
		return appwire.SpawnSlashCatalogResponse{}, appwire.InvalidParams(err.Error())
	} else {
		pluginDirs = resolution.SelectedDirs
	}
	loaded, _ := plugin.LoadAllFailSoft(pluginDirs)
	var env execenv.ExecutionEnvironment
	if preview.cwd != "" {
		env = execenv.NewLocalExecutionEnvironment(preview.cwd)
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
	sortCommandDescriptors(commands)
	// Skill advertisement uses session startup's portable discovery (the same
	// builder the past-thread catalog uses) so the pre-session catalog shows
	// exactly what the resulting session loads, with the catalog's real
	// invocation controls and availability verdict copied verbatim — the
	// frontend keeps only available && user-invocable rows, so zero-valued
	// flags would silently advertise nothing.
	home, _ := os.UserHomeDir()
	sources, _ := plugin.SkillSources(pluginDirs)
	catalog := skill.Discover(env, skill.DiscoverOptions{
		HomeDir:       home,
		UserSkillsDir: userdirs.Subdir(userdirs.DefaultConfigRoot(), "skills"),
		ExtraDirs:     preview.resolved.Effective.SkillsDirs,
		Plugins:       sources,
	})
	entries := catalog.UserEntries()
	skills := make([]appwire.EvenerSkillInfo, 0, len(entries))
	for _, entry := range entries {
		skills = append(skills, appwire.EvenerSkillInfo{
			Name:                   entry.CatalogName,
			Description:            entry.Meta.Description,
			DisableModelInvocation: entry.Controls.DisableModelInvocation,
			UserInvocable:          entry.Controls.UserInvocable,
			Available:              !entry.Unavailable,
			AllowedTools:           append([]string(nil), entry.Meta.AllowedTools...),
		})
	}
	return appwire.SpawnSlashCatalogResponse{Commands: commands, Skills: skills}, nil
}
