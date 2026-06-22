package main

import (
	"context"
	"flag"
	"fmt"
	"io"

	"primeradiant.com/serf/buildinfo"
	"primeradiant.com/serf/envvars"
	"primeradiant.com/serf/internal/selfupdate"
)

func runUpgrade(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("upgrade", flag.ContinueOnError)
	fs.SetOutput(stderr)
	prefix := fs.String("prefix", "", "install prefix (default: "+envvars.Home.Name+"/.local)")
	binDir := fs.String("bin-dir", "", "symlink directory (default: <prefix>/bin)")
	shareBinDir := fs.String("share-bin-dir", "", "managed binary directory (default: <prefix>/share/serf/bin)")
	repoURL := fs.String("repo-url", "", "GitHub repository URL for release downloads")
	goos := fs.String("goos", "", "release operating system override")
	goarch := fs.String("goarch", "", "release architecture override")
	fs.Usage = func() {
		_, _ = fmt.Fprintf(stderr, "Usage: serf upgrade [flags] [release|snapshot|vTAG]\n\n")
		_, _ = fmt.Fprintf(stderr, "Upgrade the installed Serf binaries. With no target, release builds track releases and snapshot builds track snapshots.\n\n")
		_, _ = fmt.Fprintf(stderr, "Options:\n")
		printLongFlagDefaults(stderr, fs)
	}
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 1 {
		return fmt.Errorf("expected at most one upgrade target")
	}
	requested := ""
	if fs.NArg() == 1 {
		requested = fs.Arg(0)
	}

	result, err := selfupdate.Upgrade(context.Background(), selfupdate.Options{
		Requested:      requested,
		CurrentChannel: buildinfo.UpgradeChannel(),
		Prefix:         *prefix,
		BinDir:         *binDir,
		ShareBinDir:    *shareBinDir,
		RepoURL:        *repoURL,
		GOOS:           *goos,
		GOARCH:         *goarch,
	})
	if err != nil {
		return err
	}

	_, _ = fmt.Fprintf(stdout, "upgraded serf to %s\n", result.Channel)
	_, _ = fmt.Fprintf(stdout, "  archive: %s\n", result.Archive)
	_, _ = fmt.Fprintf(stdout, "  installed: %s\n", result.ShareBinDir)
	_, _ = fmt.Fprintf(stdout, "  symlinks: %s\n", result.BinDir)
	_, _ = fmt.Fprintf(stdout, "%s\n", result.RestartMessage)
	return nil
}
