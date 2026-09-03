package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"sort"
	"strings"
	"text/tabwriter"

	"primeradiant.com/evener/cmdutil"
	"primeradiant.com/evener/internal/plugins"
)

type pluginManager interface {
	SeedDefaultMarketplaces(context.Context) (bool, error)
	ListMarketplaces() (plugins.Marketplaces, error)
	AddMarketplace(context.Context, string, plugins.Source) (plugins.MarketplaceRef, error)
	RemoveMarketplace(context.Context, string) error
	RefreshMarketplace(context.Context, string) error
	Browse(context.Context, string) (plugins.Catalog, error)
	List() ([]plugins.ListItem, error)
	Install(context.Context, string, string) (plugins.InstallEntry, error)
	Remove(context.Context, string, string) error
	SetEnabled(context.Context, string, string, bool) error
	UpdateAll(context.Context) ([]plugins.InstallEntry, error)
	Upgrade(context.Context, string, string) (plugins.InstallEntry, error)
	SetAutoUpgrade(context.Context, string, string, bool) error
	Gc(context.Context) ([]string, error)
	Doctor() ([]plugins.DoctorFinding, error)
	UpdateAutoUpgrade(context.Context) ([]plugins.UpgradedPlugin, error)
}

type pluginLaunchResolver interface {
	ResolveForLaunch(context.Context, []string, *[]string) (plugins.LaunchPluginResolution, error)
}

var newPluginManager = func() pluginManager { return plugins.NewManager("") }
var parsePluginMarketplaceSource = parseMarketplaceSourceArg

func runPlugin(args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	// doctor is a read-only diagnostic (Manager.Doctor's contract: never
	// mutates store state) and must not trigger first-run seeding the way
	// every other verb does.
	if len(args) == 0 || args[0] != "doctor" {
		if _, err := newPluginManager().SeedDefaultMarketplaces(context.Background()); err != nil {
			_, _ = fmt.Fprintf(stderr, "warning: seeding default marketplaces: %v\n", err)
		}
	}
	if len(args) == 0 {
		printPluginUsage(stderr)
		return nil
	}

	switch args[0] {
	case "marketplace":
		return runPluginMarketplace(args[1:], stdout, stderr)
	// lifecycle verbs added in Task 4:
	case "install", "remove", "enable", "disable", "list", "upgrade", "gc", "auto-upgrade":
		return runPluginLifecycle(args[0], args[1:], stdin, stdout, stderr)
	case "check-now":
		return runPluginCheckNow(args[1:], stdout, stderr)
	case "doctor":
		return runPluginDoctor(args[1:], stdout, stderr)
	case "help", "-h", "--help":
		printPluginUsage(stderr)
		return nil
	default:
		printPluginUsage(stderr)
		return fmt.Errorf("unknown plugin command %q", args[0])
	}
}

func runPluginMarketplace(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return errors.New("usage: evener plugin marketplace add|remove|list|refresh|browse")
	}
	m := newPluginManager()
	switch args[0] {
	case "list":
		fs := flag.NewFlagSet("marketplace list", flag.ContinueOnError)
		fs.SetOutput(stderr)
		asJSON := fs.Bool("json", false, "emit JSON")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		mk, err := m.ListMarketplaces()
		if err != nil {
			return err
		}
		return renderMarketplaces(stdout, mk, *asJSON)
	case "add":
		fs := flag.NewFlagSet("marketplace add", flag.ContinueOnError)
		fs.SetOutput(stderr)
		yes := fs.Bool("yes", false, "skip the trust confirmation")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if fs.NArg() < 1 {
			return errors.New("usage: evener plugin marketplace add <url|owner/repo|path> [--yes]")
		}
		src, err := parsePluginMarketplaceSource(fs.Arg(0))
		if err != nil {
			return err
		}
		if !*yes {
			_, _ = fmt.Fprintf(stderr, "Add marketplace from %s? Marketplaces are arbitrary code repositories.\nPass --yes to confirm.\n", fs.Arg(0))
			return errors.New("confirmation required")
		}
		ref, err := m.AddMarketplace(context.Background(), "", src)
		if err != nil {
			return err
		}
		_, _ = fmt.Fprintf(stdout, "Added marketplace at %s\n", ref.InstallLocation)
		return nil
	case "remove":
		fs := flag.NewFlagSet("marketplace remove", flag.ContinueOnError)
		fs.SetOutput(stderr)
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if fs.NArg() < 1 {
			return errors.New("usage: evener plugin marketplace remove <name>")
		}
		name := fs.Arg(0)
		if err := m.RemoveMarketplace(context.Background(), name); err != nil {
			return err
		}
		_, _ = fmt.Fprintf(stdout, "Removed marketplace %q\n", name)
		return nil
	case "refresh":
		fs := flag.NewFlagSet("marketplace refresh", flag.ContinueOnError)
		fs.SetOutput(stderr)
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if fs.NArg() < 1 {
			return errors.New("usage: evener plugin marketplace refresh <name>")
		}
		name := fs.Arg(0)
		if err := m.RefreshMarketplace(context.Background(), name); err != nil {
			return err
		}
		_, _ = fmt.Fprintf(stdout, "Refreshed marketplace %q\n", name)
		return nil
	case "browse":
		fs := flag.NewFlagSet("marketplace browse", flag.ContinueOnError)
		fs.SetOutput(stderr)
		asJSON := fs.Bool("json", false, "emit JSON")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if fs.NArg() < 1 {
			return errors.New("usage: evener plugin marketplace browse <name> [--json]")
		}
		name := fs.Arg(0)
		cat, err := m.Browse(context.Background(), name)
		if err != nil {
			return err
		}
		return renderCatalog(stdout, cat, *asJSON)
	default:
		return fmt.Errorf("unknown marketplace command %q", args[0])
	}
}

// renderCatalog renders a browsed marketplace's plugin catalog as a human
// table or JSON. Plugins skipped during parsing (Fix for design spec §7 —
// e.g. an unsupported/unknown source kind) are not silently dropped: they are
// listed by name so the user knows the marketplace has more entries than what
// installs.
func renderCatalog(w io.Writer, cat plugins.Catalog, asJSON bool) error {
	if asJSON {
		return json.NewEncoder(w).Encode(cat)
	}

	if len(cat.Plugins) == 0 {
		_, _ = fmt.Fprintf(w, "No installable plugins in marketplace %q.\n", cat.Name)
	} else {
		tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
		_, _ = fmt.Fprintf(tw, "PLUGIN\tDESCRIPTION\n")
		for _, p := range cat.Plugins {
			_, _ = fmt.Fprintf(tw, "%s\t%s\n", p.Name, p.Description)
		}
		_ = tw.Flush()
	}
	if len(cat.SkippedPlugins) > 0 {
		_, _ = fmt.Fprintf(w, "\nSkipped (unsupported source): %s\n", strings.Join(cat.SkippedPlugins, ", "))
	}
	return nil
}

// parseMarketplaceSourceArg maps a CLI arg to a Source: "owner/repo" → github,
// "https://…"/"git@…" → url, an existing local path → directory.
func parseMarketplaceSourceArg(arg string) (plugins.Source, error) {
	switch {
	case strings.HasPrefix(arg, "https://"), strings.HasPrefix(arg, "http://"), strings.HasPrefix(arg, "git@"):
		return plugins.Source{Kind: plugins.SourceURL, URL: arg}, nil
	case strings.Count(arg, "/") == 1 && !strings.Contains(arg, ":") && !strings.HasPrefix(arg, "."):
		return plugins.Source{Kind: plugins.SourceGitHub, Repo: arg}, nil
	default:
		// treat as a local directory path
		return plugins.Source{Kind: plugins.SourceDirectory, Path: arg}, nil
	}
}

func printPluginUsage(w io.Writer) {
	_, _ = fmt.Fprintf(w, "Usage: evener plugin <command> [flags]\n\n")
	_, _ = fmt.Fprintf(w, "Manage plugins and plugin marketplaces.\n\n")
	_, _ = fmt.Fprintf(w, "Commands:\n")
	_, _ = fmt.Fprintf(w, "  marketplace   Manage plugin marketplaces (add, remove, list, refresh, browse)\n")
	_, _ = fmt.Fprintf(w, "  install       Install a plugin\n")
	_, _ = fmt.Fprintf(w, "  remove        Remove an installed plugin\n")
	_, _ = fmt.Fprintf(w, "  enable        Enable a plugin\n")
	_, _ = fmt.Fprintf(w, "  disable       Disable a plugin\n")
	_, _ = fmt.Fprintf(w, "  list          List installed plugins\n")
	_, _ = fmt.Fprintf(w, "  upgrade       Upgrade installed plugins\n")
	_, _ = fmt.Fprintf(w, "  auto-upgrade  Toggle a plugin's auto-upgrade flag (--off to disable)\n")
	_, _ = fmt.Fprintf(w, "  check-now     Run one auto-upgrade pass now for every opted-in plugin\n")
	_, _ = fmt.Fprintf(w, "  gc            Garbage collect unused plugin cache\n")
	_, _ = fmt.Fprintf(w, "  doctor        Run plugin-store health checks\n")
}

func renderMarketplaces(w io.Writer, mk plugins.Marketplaces, asJSON bool) error {
	if asJSON {
		return json.NewEncoder(w).Encode(mk)
	}

	if len(mk) == 0 {
		_, _ = fmt.Fprintf(w, "No marketplaces registered.\n")
		return nil
	}

	names := make([]string, 0, len(mk))
	for name := range mk {
		names = append(names, name)
	}
	sort.Strings(names)

	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	for _, name := range names {
		ref := mk[name]
		sourceDesc := sourceDescription(ref.Source)
		_, _ = fmt.Fprintf(tw, "  %s\t%s\t%s\n", name, sourceDesc, ref.LastUpdated.Format("2006-01-02 15:04"))
	}
	_ = tw.Flush()
	return nil
}

func sourceDescription(src plugins.Source) string {
	switch src.Kind {
	case plugins.SourceDirectory:
		return "directory: " + src.Path
	case plugins.SourceGitHub:
		return "github: " + src.Repo
	case plugins.SourceURL:
		return "url: " + src.URL
	case plugins.SourceGitSubdir:
		return "git-subdir: " + src.URL + "/" + src.Path
	default:
		return "unknown: " + string(src.Kind)
	}
}

// splitPluginRef splits arg on the LAST "@" into plugin@marketplace.
// Plugin names may contain "@", so we split on the last occurrence.
func splitPluginRef(arg string) (plugin, marketplace string, err error) {
	i := strings.LastIndex(arg, "@")
	if i < 0 {
		return "", "", errors.New("expected <plugin>@<marketplace>")
	}
	return arg[:i], arg[i+1:], nil
}

// renderPluginList renders a list of installed plugins as a human table or JSON.
func renderPluginList(w io.Writer, items []plugins.ListItem, asJSON bool) error {
	if asJSON {
		return json.NewEncoder(w).Encode(items)
	}

	if len(items) == 0 {
		_, _ = fmt.Fprintf(w, "No plugins installed.\n")
		return nil
	}

	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintf(tw, "PLUGIN@MARKETPLACE\tVERSION\tENABLED\tAUTO-UPGRADE\tBROKEN\n")
	for _, item := range items {
		enabled := "no"
		if item.Enabled {
			enabled = "yes"
		}
		autoUpgrade := "no"
		if item.AutoUpgrade {
			autoUpgrade = "yes"
		}
		broken := "no"
		if item.Broken {
			broken = "yes"
		}
		ref := item.Plugin + "@" + item.Marketplace
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", ref, item.Version, enabled, autoUpgrade, broken)
	}
	_ = tw.Flush()
	return nil
}

func renderEffectivePluginList(w io.Writer, resolution plugins.LaunchPluginResolution, asJSON bool) error {
	if asJSON {
		result := effectivePluginListJSON{
			Plugins:     make([]effectivePluginJSON, 0, len(resolution.Candidates)),
			Diagnostics: resolution.Diagnostics,
		}
		for _, candidate := range resolution.Candidates {
			result.Plugins = append(result.Plugins, effectivePluginJSON{
				Name: candidate.Name, Version: candidate.Version, Description: candidate.Description,
				Source: candidate.Source, Marketplace: candidate.Marketplace, Path: candidate.Path,
				SkillCount: candidate.SkillCount, AgentCount: candidate.AgentCount,
				CommandCount: candidate.CommandCount, HookCount: candidate.HookCount, MCPCount: candidate.MCPCount,
			})
		}
		return json.NewEncoder(w).Encode(result)
	}

	if len(resolution.Candidates) == 0 {
		_, _ = fmt.Fprintln(w, "No effective plugins.")
		renderLaunchPluginDiagnostics(w, resolution.Diagnostics)
		return nil
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "PLUGIN\tVERSION\tSOURCE\tSKILLS\tAGENTS\tCOMMANDS\tHOOKS\tMCP")
	for _, candidate := range resolution.Candidates {
		source := string(candidate.Source)
		if candidate.Marketplace != "" {
			source += ":" + candidate.Marketplace
		}
		if candidate.Path != "" {
			source += ":" + candidate.Path
		}
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%d\t%d\t%d\t%d\t%d\n", candidate.Name, candidate.Version, source,
			candidate.SkillCount, candidate.AgentCount, candidate.CommandCount, candidate.HookCount, candidate.MCPCount)
	}
	_ = tw.Flush()
	renderLaunchPluginDiagnostics(w, resolution.Diagnostics)
	return nil
}

func runPluginLifecycle(verb string, args []string, _ io.Reader, stdout, stderr io.Writer) error {
	ctx := context.Background()
	m := newPluginManager()

	switch verb {
	case "list":
		fs := flag.NewFlagSet("list", flag.ContinueOnError)
		fs.SetOutput(stderr)
		asJSON := fs.Bool("json", false, "emit JSON")
		effective := fs.Bool("effective", false, "list the effective launch plugin inventory")
		var pluginDirs cmdutil.StringSliceFlag
		fs.Var(&pluginDirs, "plugin-dir", "plugin directory (repeatable)")
		if err := fs.Parse(args); err != nil {
			return err
		}
		if *effective {
			resolver, ok := m.(pluginLaunchResolver)
			if !ok {
				return errors.New("plugin manager does not support effective listing")
			}
			// context.Background: runPlugin is a one-shot command with no
			// context of its own, and nothing cancels this listing but the
			// process ending.
			resolution, err := resolver.ResolveForLaunch(context.Background(), []string(pluginDirs), nil)
			if renderErr := renderEffectivePluginList(stdout, resolution, *asJSON); renderErr != nil {
				return renderErr
			}
			return err
		}
		items, err := m.List()
		if err != nil {
			return err
		}
		return renderPluginList(stdout, items, *asJSON)

	case "install":
		fs := flag.NewFlagSet("install", flag.ContinueOnError)
		fs.SetOutput(stderr)
		yes := fs.Bool("yes", false, "skip the confirmation prompt")
		if err := fs.Parse(args); err != nil {
			return err
		}
		if fs.NArg() < 1 {
			return errors.New("usage: evener plugin install <plugin>@<marketplace> [--yes]")
		}
		plugin, marketplace, err := splitPluginRef(fs.Arg(0))
		if err != nil {
			return err
		}
		if !*yes {
			_, _ = fmt.Fprintf(stderr, "Install plugin %s from %s? Plugins are arbitrary code.\nPass --yes to confirm.\n", plugin, marketplace)
			return errors.New("confirmation required")
		}
		entry, err := m.Install(ctx, plugin, marketplace)
		if err != nil {
			return err
		}
		_, _ = fmt.Fprintf(stdout, "Installed %s@%s version %s at %s\n", plugin, marketplace, entry.Version, entry.InstallPath)
		if entry.Note != "" {
			_, _ = fmt.Fprintf(stdout, "Note: %s\n", entry.Note)
		}
		return nil

	case "remove":
		fs := flag.NewFlagSet("remove", flag.ContinueOnError)
		fs.SetOutput(stderr)
		if err := fs.Parse(args); err != nil {
			return err
		}
		if fs.NArg() < 1 {
			return errors.New("usage: evener plugin remove <plugin>@<marketplace>")
		}
		plugin, marketplace, err := splitPluginRef(fs.Arg(0))
		if err != nil {
			return err
		}
		if err := m.Remove(ctx, plugin, marketplace); err != nil {
			return err
		}
		_, _ = fmt.Fprintf(stdout, "Removed %s@%s\n", plugin, marketplace)
		return nil

	case "enable":
		fs := flag.NewFlagSet("enable", flag.ContinueOnError)
		fs.SetOutput(stderr)
		if err := fs.Parse(args); err != nil {
			return err
		}
		if fs.NArg() < 1 {
			return errors.New("usage: evener plugin enable <plugin>@<marketplace>")
		}
		plugin, marketplace, err := splitPluginRef(fs.Arg(0))
		if err != nil {
			return err
		}
		if err := m.SetEnabled(ctx, plugin, marketplace, true); err != nil {
			return err
		}
		_, _ = fmt.Fprintf(stdout, "Enabled %s@%s\n", plugin, marketplace)
		return nil

	case "disable":
		fs := flag.NewFlagSet("disable", flag.ContinueOnError)
		fs.SetOutput(stderr)
		if err := fs.Parse(args); err != nil {
			return err
		}
		if fs.NArg() < 1 {
			return errors.New("usage: evener plugin disable <plugin>@<marketplace>")
		}
		plugin, marketplace, err := splitPluginRef(fs.Arg(0))
		if err != nil {
			return err
		}
		if err := m.SetEnabled(ctx, plugin, marketplace, false); err != nil {
			return err
		}
		_, _ = fmt.Fprintf(stdout, "Disabled %s@%s\n", plugin, marketplace)
		return nil

	case "upgrade":
		fs := flag.NewFlagSet("upgrade", flag.ContinueOnError)
		fs.SetOutput(stderr)
		all := fs.Bool("all", false, "upgrade all installed plugins")
		if err := fs.Parse(args); err != nil {
			return err
		}

		if *all {
			entries, err := m.UpdateAll(ctx)
			if err != nil {
				return err
			}
			if len(entries) == 0 {
				_, _ = fmt.Fprintf(stdout, "No plugins to upgrade.\n")
			} else {
				_, _ = fmt.Fprintf(stdout, "Upgraded %d plugin(s).\n", len(entries))
				for _, entry := range entries {
					_, _ = fmt.Fprintf(stdout, "  %s\n", entry.Version)
				}
			}
			return nil
		}

		if fs.NArg() < 1 {
			return errors.New("usage: evener plugin upgrade <plugin>@<marketplace> | upgrade --all")
		}
		plugin, marketplace, err := splitPluginRef(fs.Arg(0))
		if err != nil {
			return err
		}
		entry, err := m.Upgrade(ctx, plugin, marketplace)
		if err != nil {
			return err
		}
		_, _ = fmt.Fprintf(stdout, "Upgraded %s@%s to version %s\n", plugin, marketplace, entry.Version)
		return nil

	case "auto-upgrade":
		fs := flag.NewFlagSet("auto-upgrade", flag.ContinueOnError)
		fs.SetOutput(stderr)
		off := fs.Bool("off", false, "disable auto-upgrade instead of enabling it")
		if err := fs.Parse(args); err != nil {
			return err
		}
		if fs.NArg() < 1 {
			return errors.New("usage: evener plugin auto-upgrade <plugin>@<marketplace> [--off]")
		}
		plugin, marketplace, err := splitPluginRef(fs.Arg(0))
		if err != nil {
			return err
		}
		on := !*off
		if err := m.SetAutoUpgrade(ctx, plugin, marketplace, on); err != nil {
			return err
		}
		state := "enabled"
		if !on {
			state = "disabled"
		}
		_, _ = fmt.Fprintf(stdout, "Auto-upgrade %s for %s@%s\n", state, plugin, marketplace)
		return nil

	case "gc":
		fs := flag.NewFlagSet("gc", flag.ContinueOnError)
		fs.SetOutput(stderr)
		asJSON := fs.Bool("json", false, "emit JSON")
		if err := fs.Parse(args); err != nil {
			return err
		}
		removed, err := m.Gc(context.Background())
		if err != nil {
			return err
		}
		if *asJSON {
			return json.NewEncoder(stdout).Encode(removed)
		}
		if len(removed) == 0 {
			_, _ = fmt.Fprintf(stdout, "Nothing to remove.\n")
		} else {
			_, _ = fmt.Fprintf(stdout, "Removed %d cache dir(s):\n", len(removed))
			for _, p := range removed {
				_, _ = fmt.Fprintf(stdout, "  %s\n", p)
			}
		}
		return nil

	default:
		return fmt.Errorf("unknown plugin lifecycle command %q", verb)
	}
}

// runPluginDoctor is the `evener plugin doctor` alias for `evener-doctor plugins`
// (design spec §13): the same read-only Manager.Doctor health check, rendered
// or JSON-encoded the same way, reachable without a separate binary.
func runPluginDoctor(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("doctor", flag.ContinueOnError)
	fs.SetOutput(stderr)
	asJSON := fs.Bool("json", false, "emit JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	findings, err := newPluginManager().Doctor()
	if err != nil {
		return err
	}
	if *asJSON {
		return json.NewEncoder(stdout).Encode(findings)
	}
	_, err = fmt.Fprint(stdout, plugins.RenderDoctorFindings(findings))
	return err
}

// checkNowResult is runPluginCheckNow's --json shape. It intentionally does
// not reuse appwire.PluginCheckNowResponse: that type's Updated is a flat
// "<plugin>@<marketplace>" ref list matching the hub RPC's wire contract,
// which this CLI-only verb has no need to match.
type checkNowResult struct {
	Updated []string `json:"updated"`
	Error   string   `json:"error,omitempty"`
}

// runPluginCheckNow is `evener plugin check-now` (design spec §9.1): manually
// triggers one auto-upgrade pass — upgrading every installed, git-backed
// plugin with autoUpgrade enabled — right now instead of waiting for the hub
// daemon's timer. The hub already exposes this on demand via the
// evener/plugin/checkNow RPC, but nothing reachable from the CLI did; a user
// running evener without the hub (or wanting a scriptable check) had no way to
// trigger it. Failures are reported alongside successes rather than aborting
// the report, matching Manager.UpdateAutoUpgrade's own failure-isolation.
func runPluginCheckNow(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("check-now", flag.ContinueOnError)
	fs.SetOutput(stderr)
	asJSON := fs.Bool("json", false, "emit JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}

	updated, upgradeErr := newPluginManager().UpdateAutoUpgrade(context.Background())
	refs := make([]string, len(updated))
	for i, u := range updated {
		refs[i] = u.Plugin + "@" + u.Marketplace
	}

	if *asJSON {
		result := checkNowResult{Updated: refs}
		if upgradeErr != nil {
			result.Error = upgradeErr.Error()
		}
		return json.NewEncoder(stdout).Encode(result)
	}

	if len(updated) == 0 {
		_, _ = fmt.Fprintf(stdout, "No plugins upgraded.\n")
	} else {
		_, _ = fmt.Fprintf(stdout, "Upgraded %d plugin(s):\n", len(updated))
		for _, u := range updated {
			_, _ = fmt.Fprintf(stdout, "  %s@%s -> %s\n", u.Plugin, u.Marketplace, u.Entry.Version)
		}
	}
	if upgradeErr != nil {
		_, _ = fmt.Fprintf(stderr, "warning: %v\n", upgradeErr)
	}
	return nil
}
