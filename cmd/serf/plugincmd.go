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

	"primeradiant.com/serf/internal/plugins"
)

func runPlugin(args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	// doctor is a read-only diagnostic (Manager.Doctor's contract: never
	// mutates store state) and must not trigger first-run seeding the way
	// every other verb does.
	if len(args) == 0 || args[0] != "doctor" {
		if _, err := plugins.NewManager("").SeedDefaultMarketplaces(); err != nil {
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
		return errors.New("usage: serf plugin marketplace add|remove|list|refresh|browse")
	}
	m := plugins.NewManager("")
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
			return errors.New("usage: serf plugin marketplace add <url|owner/repo|path> [--yes]")
		}
		src, err := parseMarketplaceSourceArg(fs.Arg(0))
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
			return errors.New("usage: serf plugin marketplace remove <name>")
		}
		name := fs.Arg(0)
		if err := m.RemoveMarketplace(name); err != nil {
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
			return errors.New("usage: serf plugin marketplace refresh <name>")
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
			return errors.New("usage: serf plugin marketplace browse <name> [--json]")
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
	_, _ = fmt.Fprintf(w, "Usage: serf plugin <command> [flags]\n\n")
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

func runPluginLifecycle(verb string, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	ctx := context.Background()
	m := plugins.NewManager("")

	switch verb {
	case "list":
		fs := flag.NewFlagSet("list", flag.ContinueOnError)
		fs.SetOutput(stderr)
		asJSON := fs.Bool("json", false, "emit JSON")
		if err := fs.Parse(args); err != nil {
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
			return errors.New("usage: serf plugin install <plugin>@<marketplace> [--yes]")
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
		return nil

	case "remove":
		fs := flag.NewFlagSet("remove", flag.ContinueOnError)
		fs.SetOutput(stderr)
		if err := fs.Parse(args); err != nil {
			return err
		}
		if fs.NArg() < 1 {
			return errors.New("usage: serf plugin remove <plugin>@<marketplace>")
		}
		plugin, marketplace, err := splitPluginRef(fs.Arg(0))
		if err != nil {
			return err
		}
		if err := m.Remove(plugin, marketplace); err != nil {
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
			return errors.New("usage: serf plugin enable <plugin>@<marketplace>")
		}
		plugin, marketplace, err := splitPluginRef(fs.Arg(0))
		if err != nil {
			return err
		}
		if err := m.SetEnabled(plugin, marketplace, true); err != nil {
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
			return errors.New("usage: serf plugin disable <plugin>@<marketplace>")
		}
		plugin, marketplace, err := splitPluginRef(fs.Arg(0))
		if err != nil {
			return err
		}
		if err := m.SetEnabled(plugin, marketplace, false); err != nil {
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
			return errors.New("usage: serf plugin upgrade <plugin>@<marketplace> | upgrade --all")
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
			return errors.New("usage: serf plugin auto-upgrade <plugin>@<marketplace> [--off]")
		}
		plugin, marketplace, err := splitPluginRef(fs.Arg(0))
		if err != nil {
			return err
		}
		on := !*off
		if err := m.SetAutoUpgrade(plugin, marketplace, on); err != nil {
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
		removed, err := m.Gc()
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

// runPluginDoctor is the `serf plugin doctor` alias for `serf-doctor plugins`
// (design spec §13): the same read-only Manager.Doctor health check, rendered
// or JSON-encoded the same way, reachable without a separate binary.
func runPluginDoctor(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("doctor", flag.ContinueOnError)
	fs.SetOutput(stderr)
	asJSON := fs.Bool("json", false, "emit JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	findings, err := plugins.NewManager("").Doctor()
	if err != nil {
		return err
	}
	if *asJSON {
		return json.NewEncoder(stdout).Encode(findings)
	}
	_, err = fmt.Fprint(stdout, plugins.RenderDoctorFindings(findings))
	return err
}
