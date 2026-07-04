package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"

	"primeradiant.com/serf/internal/plugins"
)

func runPlugin(args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		printPluginUsage(stderr)
		return nil
	}

	switch args[0] {
	case "marketplace":
		return runPluginMarketplace(args[1:], stdout, stderr)
	// lifecycle verbs added in Task 4:
	case "install", "remove", "enable", "disable", "list", "upgrade", "gc":
		return runPluginLifecycle(args[0], args[1:], stdin, stdout, stderr)
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
		return errors.New("usage: serf plugin marketplace add|remove|list|refresh")
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
	default:
		return fmt.Errorf("unknown marketplace command %q", args[0])
	}
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
	_, _ = fmt.Fprintf(w, "  marketplace   Manage plugin marketplaces (add, remove, list, refresh)\n")
	_, _ = fmt.Fprintf(w, "  install       Install a plugin\n")
	_, _ = fmt.Fprintf(w, "  remove        Remove an installed plugin\n")
	_, _ = fmt.Fprintf(w, "  enable        Enable a plugin\n")
	_, _ = fmt.Fprintf(w, "  disable       Disable a plugin\n")
	_, _ = fmt.Fprintf(w, "  list          List installed plugins\n")
	_, _ = fmt.Fprintf(w, "  upgrade       Upgrade installed plugins\n")
	_, _ = fmt.Fprintf(w, "  gc            Garbage collect unused plugin cache\n")
}

func renderMarketplaces(w io.Writer, mk plugins.Marketplaces, asJSON bool) error {
	if asJSON {
		return json.NewEncoder(w).Encode(mk)
	}

	if len(mk) == 0 {
		_, _ = fmt.Fprintf(w, "No marketplaces registered.\n")
		return nil
	}

	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	for name, ref := range mk {
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

// TODO(P2-T4): Task 4 replaces this stub with the full implementation.
func runPluginLifecycle(verb string, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	return fmt.Errorf("not implemented: %s", verb)
}
