package sshconn

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"

	"primeradiant.com/evener/buildinfo"
	"primeradiant.com/evener/cmd/evener-hub/internal/hostreg"
)

// buildinfoPkg is the import path whose ldflags the controller stamps into the
// deployed binary, so its launch-check version equals this process's
// buildinfo.Version().
const buildinfoPkg = "primeradiant.com/evener/buildinfo"

// deployTempSuffix is appended to the install path to form the temp name the
// push writes before the atomic mv. Deploys are serialized per host by the
// manager's per-host lock, so a fixed suffix cannot collide.
const deployTempSuffix = ".tmp"

// localBuild is the production cross-compile seam. It runs the same command
// shape make build-linux uses (make/building.mk:42) and stamps this process's
// own buildinfo values so the deployed binary reports the controller's exact
// version.
func localBuild(ctx context.Context, goos, goarch, out string) error {
	cmd := exec.CommandContext(ctx, "go", "build", "-ldflags", buildLdflags(), "-o", out, "./cmd/evener/")
	cmd.Env = append(os.Environ(),
		"CGO_ENABLED=0",
		"GOOS="+goos,
		"GOARCH="+goarch,
	)
	// Build the controller's own tree even when the hub was not started from
	// its module root.
	if root := moduleRoot(); root != "" {
		cmd.Dir = root
	}
	outBytes, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("go build %s/%s: %v: %s", goos, goarch, err, tail(outBytes))
	}
	return nil
}

// buildLdflags renders the -X flags that carry this process's buildinfo into
// the deployed binary. Reading the values in-process (instead of re-running git
// and date as the Makefile does) guarantees the deployed version is exactly the
// controller's buildinfo.Version().
func buildLdflags() string {
	set := func(name, value string) string {
		return "-X " + buildinfoPkg + "." + name + "=" + value
	}
	return strings.Join([]string{
		set("GitSHA", buildinfo.GitSHA),
		set("GitDirty", buildinfo.GitDirty),
		set("BuildTime", buildinfo.BuildTime),
		set("Channel", buildinfo.Channel),
	}, " ")
}

// moduleRoot walks up from the working directory looking for go.mod so
// `go build ./cmd/evener/` resolves the controller's own tree regardless of the
// launch directory.
func moduleRoot() string {
	dir, err := os.Getwd()
	if err != nil {
		return ""
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}

// deploy cross-compiles this tree for the host target and atomically installs
// it at the host's evener path. The existing binary is untouched unless the
// push fully succeeds.
func (m *Manager) deploy(ctx context.Context, host hostreg.Host, facts Preflight) error {
	target, err := m.deployTarget(ctx, host)
	if err != nil {
		return err
	}

	stageDir, err := os.MkdirTemp("", "evener-deploy-")
	if err != nil {
		return fmt.Errorf("%w: host %q staging dir: %v", ErrDeploy, host.Name, err)
	}
	defer os.RemoveAll(stageDir)
	stage := filepath.Join(stageDir, "evener")

	build := m.opts.BuildBinary
	if build == nil {
		build = localBuild
	}
	if err := build(ctx, facts.OS, facts.Arch, stage); err != nil {
		return fmt.Errorf("%w: host %q build %s/%s: %v", ErrDeploy, host.Name, facts.OS, facts.Arch, err)
	}

	f, err := os.Open(stage)
	if err != nil {
		return fmt.Errorf("%w: host %q open staged binary: %v", ErrDeploy, host.Name, err)
	}
	defer f.Close()

	return m.pushBinary(ctx, host, target, f)
}

// deployTarget resolves the absolute remote path the binary is installed to:
// the registry's evener_path when set (whose directory must already exist),
// otherwise whatever `evener` resolves to on the remote PATH.
func (m *Manager) deployTarget(ctx context.Context, host hostreg.Host) (string, error) {
	if p := strings.TrimSpace(host.EvenerPath); p != "" {
		dir := path.Dir(p)
		out, err := m.runner.Run(ctx, rawCommandArgv(m.opts, host, "test -d "+dir), nil)
		if err != nil {
			return "", fmt.Errorf("%w: host %q evener_path directory %q does not exist: %v: %s", ErrDeploy, host.Name, dir, err, tail(out))
		}
		return p, nil
	}

	out, err := m.runner.Run(ctx, rawCommandArgv(m.opts, host, "command -v evener"), nil)
	if err != nil {
		return "", fmt.Errorf("%w: host %q evener not found on PATH: %v: %s", ErrDeploy, host.Name, err, tail(out))
	}
	p := strings.TrimSpace(string(out))
	if p == "" {
		return "", fmt.Errorf("%w: host %q evener not found on PATH", ErrDeploy, host.Name)
	}
	return p, nil
}

// pushBinary streams data to target over an ssh `cat`, then chmod +x and mv it
// into place. The temp-then-mv makes the install atomic: an interrupted push
// never leaves a truncated evener at the final path. Feeding the binary on the
// Runner.Run stdin keeps argv[0] == "ssh".
func (m *Manager) pushBinary(ctx context.Context, host hostreg.Host, target string, data io.Reader) error {
	tmp := target + deployTempSuffix
	remote := fmt.Sprintf("cat > %s && chmod +x %s && mv %s %s", tmp, tmp, tmp, target)
	out, err := m.runner.Run(ctx, rawCommandArgv(m.opts, host, remote), data)
	if err != nil {
		return fmt.Errorf("%w: host %q push %s: %v: %s", ErrDeploy, host.Name, target, err, tail(out))
	}
	return nil
}
