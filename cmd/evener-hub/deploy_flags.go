package hub

import (
	"context"
	"debug/buildinfo"
	"fmt"
	"os"
	"strings"

	"primeradiant.com/evener/cmd/evener-hub/internal/sshconn"
)

// The deploy flags are validated where they are read (parseHubOptions), so a bad
// value fails hub startup naming the flag rather than surfacing at the first
// attach as a deploy failure the operator has to trace back to a flag.
const (
	deployBinaryFlag = "-deploy-binary"
	buildSourceFlag  = "-build-source"
)

// hubDeployHelp is the remedy the sshconn terminal version refusal appends when
// no deploy path is configured. It names the hub's own flags; sshconn's default
// ("set Options.BuildSource") names an internal field the operator cannot act on.
const hubDeployHelp = "set -deploy-binary <path> (a pre-built evener for the host's target) or -build-source <path> (an evener checkout to cross-compile from)"

// deployWiring is the deploy half of sshconn.Options the hub derives from its
// flags. Constructing it is a pure function of hubOptions, so the precedence
// between the two flags is assertable without starting a hub.
type deployWiring struct {
	buildBinary func(ctx context.Context, goos, goarch, out string) error
	buildSource string
	help        string
}

// deployWiring resolves the deploy flags into sshconn.Options' build seams, with
// -deploy-binary preferred when both are set — the manager's own dispatch
// prefers BuildBinary too, so the two cannot disagree. The help text is always
// supplied: the hub always has flags to name, including the no-flag case where
// the refusal actually fires.
func (o hubOptions) deployWiring() deployWiring {
	dw := deployWiring{help: hubDeployHelp}
	switch {
	case strings.TrimSpace(o.deployBinary) != "":
		dw.buildBinary = deployBinaryBuild(o.deployBinary)
	case strings.TrimSpace(o.buildSource) != "":
		dw.buildSource = o.buildSource
	}
	return dw
}

// validateDeployFlags validates both deploy flags at startup. -build-source
// reuses sshconn's checkout validation through its exported entry point so the
// rules cannot drift; -deploy-binary is checked here because only the hub reads
// the file. Every error names the flag the operator must fix. It normalizes
// -build-source to its canonical absolute path in place.
func (o *hubOptions) validateDeployFlags() error {
	if strings.TrimSpace(o.deployBinary) != "" {
		if err := validateDeployBinary(o.deployBinary); err != nil {
			return err
		}
	}
	if strings.TrimSpace(o.buildSource) != "" {
		abs, err := sshconn.ValidateBuildSource(o.buildSource)
		if err != nil {
			return fmt.Errorf("%s %q: %w", buildSourceFlag, o.buildSource, err)
		}
		o.buildSource = abs
	}
	return nil
}

// validateDeployBinary checks an operator-supplied artifact where the flag is
// read: it must be a stat-able file the hub can read, carrying the Go buildinfo
// the seam later reads for its target — so a missing path, a directory, an
// unreadable file, or a stripped/non-Go file is refused now, naming the flag,
// instead of at the first attach.
func validateDeployBinary(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("%s %q: %w", deployBinaryFlag, path, err)
	}
	if info.IsDir() {
		return fmt.Errorf("%s %q is a directory, want a pre-built evener executable", deployBinaryFlag, path)
	}
	if _, err := buildinfo.ReadFile(path); err != nil {
		return fmt.Errorf("%s %q is not a readable Go executable: %w", deployBinaryFlag, path, err)
	}
	return nil
}

// deployBinaryBuild is the Options.BuildBinary seam for an operator-supplied
// artifact. There is nothing to compile, so the artifact is verified to target
// goos/goarch and copied to out. The target is read from the artifact's own
// buildinfo, never trusted from the operator, so a wrong-platform artifact is
// refused before the push rather than installed and caught only by the
// post-push identity check. A stripped or non-Go file is a refusal, not a panic.
func deployBinaryBuild(path string) func(ctx context.Context, goos, goarch, out string) error {
	return func(ctx context.Context, goos, goarch, out string) error {
		return copyDeployBinary(ctx, path, goos, goarch, out)
	}
}

// copyDeployBinary verifies and stages one operator-supplied artifact. Nothing
// is written to out until the artifact has been read and its target confirmed,
// so a refused artifact leaves no partial file for the push to install.
func copyDeployBinary(ctx context.Context, path, goos, goarch, out string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	info, err := buildinfo.ReadFile(path)
	if err != nil {
		return fmt.Errorf("%s %q is not a readable Go executable: %w", deployBinaryFlag, path, err)
	}
	gotOS, gotArch := buildSetting(info, "GOOS"), buildSetting(info, "GOARCH")
	if gotOS != goos || gotArch != goarch {
		return fmt.Errorf("%s %q targets %s/%s, but the host needs %s/%s", deployBinaryFlag, path, gotOS, gotArch, goos, goarch)
	}
	src, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("%s %q: %w", deployBinaryFlag, path, err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("%s %q: %w", deployBinaryFlag, path, err)
	}
	// Preserve the artifact's own permission bits: a `go build` artifact is
	// 0755, and the host must be able to exec what it receives.
	if err := os.WriteFile(out, data, src.Mode().Perm()); err != nil {
		return fmt.Errorf("%s %q: write staged binary %q: %w", deployBinaryFlag, path, out, err)
	}
	return nil
}

// buildSetting reads one buildinfo setting (GOOS/GOARCH) from a Go artifact.
func buildSetting(info *buildinfo.BuildInfo, key string) string {
	for _, s := range info.Settings {
		if s.Key == key {
			return s.Value
		}
	}
	return ""
}
