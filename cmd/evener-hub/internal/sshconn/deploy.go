package sshconn

import (
	"context"
	"errors"
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

// localBuild is the production cross-compile seam. source is the explicit
// evener checkout to build from (Options.BuildSource); it is verified before
// use, so an unset or wrong source is a clear error rather than a build of
// whatever tree is nearby. It runs the same command shape make build-linux uses
// (make/building.mk:42) and stamps this process's own buildinfo values so the
// deployed binary reports the controller's exact version.
func localBuild(ctx context.Context, source, goos, goarch, out string) error {
	root, err := verifyBuildSource(source)
	if err != nil {
		return fmt.Errorf("go build %s/%s: %w", goos, goarch, err)
	}
	cmd := exec.CommandContext(ctx, "go", "build", "-ldflags", buildLdflags(), "-o", out, "./cmd/evener/")
	cmd.Env = append(os.Environ(),
		"CGO_ENABLED=0",
		"GOOS="+goos,
		"GOARCH="+goarch,
	)
	cmd.Dir = root
	outBytes, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("go build %s/%s: %w: %s", goos, goarch, err, tail(outBytes))
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

// modulePath is this module's path, used to tell the evener checkout apart from
// any other workspace module the hub might be launched inside.
const modulePath = "primeradiant.com/evener"

// verifyBuildSource checks that source is an explicit, real evener checkout and
// returns its absolute path. The source cannot be discovered: an installed hub
// has no reliable way to locate its own source tree, and guessing — walking the
// working directory or runtime.Caller frames — can select a different ancestor
// checkout that declares the same module path. An unset source, or one that is
// not this module with a ./cmd/evener package, is therefore a clear error
// rather than a build of whatever tree happens to be nearby.
func verifyBuildSource(source string) (string, error) {
	if strings.TrimSpace(source) == "" {
		return "", errors.New("no build source configured: set Options.BuildSource to the evener checkout's module root (an installed hub cannot locate its own source)")
	}
	abs, err := filepath.Abs(source)
	if err != nil {
		return "", fmt.Errorf("build source %q: %w", source, err)
	}
	if !declaresEvenerModule(abs) {
		return "", fmt.Errorf("build source %q is not the evener checkout: no go.mod declaring %q", abs, modulePath)
	}
	info, err := os.Stat(filepath.Join(abs, "cmd", "evener"))
	if err != nil {
		return "", fmt.Errorf("build source %q has no cmd/evener package: %w", abs, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("build source %q has a non-directory cmd/evener", abs)
	}
	return abs, nil
}

// declaresEvenerModule reports whether dir holds a go.mod for this module.
func declaresEvenerModule(dir string) bool {
	data, err := os.ReadFile(filepath.Join(dir, "go.mod"))
	if err != nil {
		return false
	}
	for line := range strings.SplitSeq(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 2 && fields[0] == "module" && fields[1] == modulePath {
			return true
		}
	}
	return false
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
		return fmt.Errorf("%w: host %q staging dir: %w", ErrDeploy, host.Name, err)
	}
	defer func() { _ = os.RemoveAll(stageDir) }()
	stage := filepath.Join(stageDir, "evener")

	build := m.opts.BuildBinary
	if build == nil {
		source := m.opts.BuildSource
		build = func(ctx context.Context, goos, goarch, out string) error {
			return localBuild(ctx, source, goos, goarch, out)
		}
	}
	if err := build(ctx, facts.OS, facts.Arch, stage); err != nil {
		return fmt.Errorf("%w: host %q build %s/%s: %w", ErrDeploy, host.Name, facts.OS, facts.Arch, err)
	}

	f, err := os.Open(stage)
	if err != nil {
		return fmt.Errorf("%w: host %q open staged binary: %w", ErrDeploy, host.Name, err)
	}
	defer func() { _ = f.Close() }()

	return m.pushBinary(ctx, host, target, f)
}

// deployTarget resolves the absolute remote path the binary is installed to:
// the registry's evener_path when set (whose directory must already exist),
// otherwise whatever `evener` resolves to on the remote PATH.
func (m *Manager) deployTarget(ctx context.Context, host hostreg.Host) (string, error) {
	if p := strings.TrimSpace(host.EvenerPath); p != "" {
		dir := path.Dir(p)
		out, err := m.runner.Run(ctx, rawCommandArgv(m.opts, host, "test -d "+shellQuote(dir)), nil)
		if err != nil {
			return "", fmt.Errorf("%w: host %q evener_path directory %q does not exist: %w: %s", ErrDeploy, host.Name, dir, err, tail(out))
		}
		return m.resolveDeployTarget(ctx, host, p)
	}

	out, err := m.runner.Run(ctx, rawCommandArgv(m.opts, host, "command -v evener"), nil)
	if err != nil {
		return "", fmt.Errorf("%w: host %q evener not found on PATH: %w: %s", ErrDeploy, host.Name, err, tail(out))
	}
	p := strings.TrimSpace(string(out))
	if p == "" {
		return "", fmt.Errorf("%w: host %q evener not found on PATH", ErrDeploy, host.Name)
	}
	return m.resolveDeployTarget(ctx, host, p)
}

// resolvePathScript is a POSIX-sh path resolver that prints the real path a file
// names, following symlinks. It deliberately avoids `readlink -f`: that flag is a
// GNU/coreutils extension which BSD `readlink` (macOS) rejects with `readlink:
// illegal option -- f`, so every darwin/arm64 deploy failed at path resolution.
// It prints nothing and exits nonzero when the path does not resolve to an
// existing file, which the caller reads as a clear error rather than a silent
// fallback to the symlink.
const resolvePathScript = `evener_resolve() {
  p=$1
  while [ -L "$p" ]; do
    d=$(cd -P "$(dirname "$p")" 2>/dev/null && pwd) || return 1
    t=$(readlink "$p") || return 1
    case $t in
      /*) p=$t ;;
      *) p=$d/$t ;;
    esac
  done
  [ -e "$p" ] || return 1
  d=$(cd -P "$(dirname "$p")" 2>/dev/null && pwd) || return 1
  printf '%s\n' "$d/${p##*/}"
}
evener_resolve `

// resolveDeployCommand builds the remote command that prints the real path p
// names.
func resolveDeployCommand(p string) string {
	return resolvePathScript + shellQuote(p)
}

// resolveDeployTarget resolves p to the real file it names, so the atomic mv
// replaces the installed executable rather than clobbering a symlink. `make
// install` commonly installs evener as a symlink and `command -v evener` returns
// that symlink; mv-ing onto it would replace the link and break the installed
// layout (and later upgrades). The resolver prints nothing for a path that does
// not exist, which is a clear error rather than a silent fallback to the
// symlink.
func (m *Manager) resolveDeployTarget(ctx context.Context, host hostreg.Host, p string) (string, error) {
	out, err := m.runner.Run(ctx, rawCommandArgv(m.opts, host, resolveDeployCommand(p)), nil)
	if err != nil {
		return "", fmt.Errorf("%w: host %q resolve install path %q: %w: %s", ErrDeploy, host.Name, p, err, tail(out))
	}
	resolved := firstLine(string(out))
	if resolved == "" {
		return "", fmt.Errorf("%w: host %q install path %q does not resolve to a real file", ErrDeploy, host.Name, p)
	}
	return resolved, nil
}

// pushBinary streams data to target over an ssh `cat`, then chmod +x and mv it
// into place. The temp-then-mv makes the install atomic: an interrupted push
// never leaves a truncated evener at the final path. Feeding the binary on the
// Runner.Run stdin keeps argv[0] == "ssh".
func (m *Manager) pushBinary(ctx context.Context, host hostreg.Host, target string, data io.Reader) error {
	tmp := target + deployTempSuffix
	remote := fmt.Sprintf("cat > %s && chmod +x %s && mv %s %s", shellQuote(tmp), shellQuote(tmp), shellQuote(tmp), shellQuote(target))
	out, err := m.runner.Run(ctx, rawCommandArgv(m.opts, host, remote), data)
	if err != nil {
		return fmt.Errorf("%w: host %q push %s: %w: %s", ErrDeploy, host.Name, target, err, tail(out))
	}
	return nil
}
