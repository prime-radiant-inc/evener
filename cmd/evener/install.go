package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"unicode"

	"primeradiant.com/evener/buildinfo"
	"primeradiant.com/evener/internal/remoteinstall"
)

var runRemoteInstallCommand = runRemoteInstall

func runInstall(args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("install", flag.ContinueOnError)
	fs.SetOutput(stderr)
	defaultVersion := "latest"
	if buildinfo.UpgradeChannel() == "snapshot" {
		defaultVersion = "snapshot"
	}
	version := fs.String("version", defaultVersion, "release to install: latest, snapshot, or a version tag")
	prefix := fs.String("prefix", "", "remote install prefix (default: $HOME/.local)")
	binDir := fs.String("bin-dir", "", "remote symlink directory (default: <prefix>/bin)")
	shareBinDir := fs.String("share-bin-dir", "", "remote managed binary directory (default: <prefix>/share/evener/bin)")
	fs.Usage = func() {
		_, _ = fmt.Fprintln(stderr, "Usage: evener install [flags] user@host")
		_, _ = fmt.Fprintln(stderr, "Install Evener on a remote macOS or Linux host over SSH.")
		_, _ = fmt.Fprintln(stderr, "")
		_, _ = fmt.Fprintln(stderr, "Options:")
		printLongFlagDefaults(stderr, fs)
	}
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return errors.New("expected exactly one SSH target (user@host)")
	}
	target := fs.Arg(0)
	if err := validateInstallTarget(target); err != nil {
		return err
	}
	if strings.TrimSpace(*version) == "" {
		return errors.New("--version must not be empty")
	}

	remoteScript := remoteinstall.Command(*version, *prefix, *binDir, *shareBinDir)
	sshArgs := []string{
		"-T",
		"-o", "BatchMode=yes",
		"-o", "ConnectTimeout=10",
		target,
		remoteScript,
	}
	_, _ = fmt.Fprintf(stdout, "Installing Evener on %s\n", target)
	if err := runRemoteInstallCommand(context.Background(), sshArgs, bytes.NewReader(remoteinstall.Script), stdout, stderr); err != nil {
		return fmt.Errorf("remote install on %s: %w", target, err)
	}
	return nil
}

func runRemoteInstall(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	cmd := exec.CommandContext(ctx, "ssh", args...)
	cmd.Stdin = stdin
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	return cmd.Run()
}

func validateInstallTarget(target string) error {
	if target == "" {
		return errors.New("SSH target must not be empty")
	}
	if strings.HasPrefix(target, "-") {
		return errors.New("SSH target must not begin with '-'")
	}
	if strings.IndexFunc(target, func(r rune) bool {
		return unicode.IsSpace(r) || unicode.IsControl(r)
	}) >= 0 {
		return errors.New("SSH target must not contain whitespace or control characters")
	}
	return nil
}
