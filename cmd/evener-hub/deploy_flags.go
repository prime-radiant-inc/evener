package hub

import (
	"context"
	"debug/buildinfo"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"primeradiant.com/evener/cmd/evener-hub/internal/sshconn"
)

// The deploy flags are validated where they are read (parseHubOptions), so a bad
// value fails hub startup naming the flag rather than surfacing at the first
// attach as a deploy failure the operator has to trace back to a flag.
const (
	deployBinaryFlag = "-deploy-binary"
	buildSourceFlag  = "-build-source"
	noDeployFlag     = "-no-deploy"
)

// evenerMainPackage is the main package of the artifact -deploy-binary accepts:
// the evener runtime the host runs. buildinfo.Path is the import path of a Go
// executable's main package — for this repository's ./cmd/evener exactly this
// value — and it is the field that separates the runtime from every other Go
// program: another module's command, and also this repository's other commands
// (cmd/evener-hub, cmd/evener-dev), whose main packages differ. Main.Path cannot
// do that job: it names the module, which every command built from this
// repository shares.
const evenerMainPackage = "primeradiant.com/evener/cmd/evener"

// verifyDeployArtifactIdentity refuses a Go executable whose main package is not
// the evener runtime, naming the flag. It is judged from the buildinfo the caller
// has already read, on both reads of the artifact — where the flag is validated
// at startup and again on the seam before the push — so a non-evener program can
// never reach a host and replace its evener; the post-push version comparison
// then only has to judge the one thing buildinfo cannot: whether a genuine evener
// artifact was built from this controller's tree.
func verifyDeployArtifactIdentity(path string, info *buildinfo.BuildInfo) error {
	if info.Path == evenerMainPackage {
		return nil
	}
	return fmt.Errorf("%s %q is not evener: its main package is %q, want %q; supply a pre-built evener for the host's target", deployBinaryFlag, path, info.Path, evenerMainPackage)
}

// hubDeployHelp is the remedy the sshconn refusals append when no deploy path is
// configured. It names the hub's own state and flags; sshconn's default ("set
// Options.BuildSource") names an internal field the operator cannot act on. The
// default sentence leads because that is how a hub normally has a deploy source —
// its own executable — so the refusals a -no-deploy or non-evener hub produces
// tell the operator the shortest way back to a deploy.
const hubDeployHelp = "run an evener hub without -no-deploy so it deploys its own executable, or set -deploy-binary <path> (a pre-built evener for the host's target) or -build-source <path> (an evener checkout to cross-compile from)"

// deployWiring is the deploy half of sshconn.Options the hub derives from its
// flags. Constructing it is a pure function of hubOptions, so the precedence
// between the two flags is assertable without starting a hub.
type deployWiring struct {
	buildBinary func(ctx context.Context, goos, goarch, out string) error
	buildSource string
	// binaryArtifact records that buildBinary is a pre-built binary artifact
	// (the hub's own executable, or -deploy-binary) rather than a compile of
	// buildSource. sshconn's Options.BinaryArtifact carries it.
	binaryArtifact bool
	// disabled records -no-deploy: the hub wires no source, and sshconn must
	// refuse every deploy path rather than only the push, so the installer
	// fallback cannot quietly install a published artifact on a host either.
	disabled bool
	help     string
}

// deployWiring resolves the deploy flags into sshconn.Options' build seams, with
// -deploy-binary preferred when both are set — the manager's own dispatch
// prefers BuildBinary too, so the two cannot disagree. The help text is always
// supplied: the hub always has a state to name, including the -no-deploy and
// non-evener-executable cases where the refusal actually fires. -no-deploy wins
// over both flags: it is the explicit opt-in to the pre-default behavior, so a
// named deploy source alongside it is ignored (and validateDeployFlags still
// validates it, so a typo cannot hide behind the opt-out). The defaulted
// own-executable source arrives here as an ordinary deployBinary: by the time a
// hubOptions reaches this method, validateDeployFlags has resolved the default
// the same way it stores an explicit flag.
//
// The fields are read directly, with no second TrimSpace: validateDeployFlags has
// already stored the one trimmed, canonical value the hub validates, logs, and
// wires, so re-trimming here would be a second, divergent reading of the same
// flag. Production always constructs hubOptions through parseHubOptions.
func (o hubOptions) deployWiring() deployWiring {
	dw := deployWiring{help: hubDeployHelp, disabled: o.noDeploy}
	if o.noDeploy {
		return dw
	}
	switch {
	case o.deployBinary != "":
		dw.buildBinary = deployBinaryBuild(o.deployBinary)
		dw.binaryArtifact = true
	case o.buildSource != "":
		dw.buildSource = o.buildSource
	}
	return dw
}

// deployPathLogLine is the one startup line that records the deploy source the
// hub actually chose, so an operator can see whether the default fired, an
// explicit source won, or -no-deploy disabled deploys — and which named sources
// an opt-out overrode. It returns "" when there is no deploy source and nothing
// to say: a non-evener executable (an embedder or test binary) keeps exactly the
// old silence, and its refusals still name the remedy through hubDeployHelp.
func (o hubOptions) deployPathLogLine() string {
	if o.noDeploy {
		line := "[hub] deploy path: " + noDeployFlag + " (deploys disabled)"
		var ignored []string
		if o.deployBinary != "" {
			ignored = append(ignored, deployBinaryFlag+" "+o.deployBinary)
		}
		if o.buildSource != "" {
			ignored = append(ignored, buildSourceFlag+" "+o.buildSource)
		}
		if len(ignored) > 0 {
			line += "; ignoring " + strings.Join(ignored, " and ")
		}
		return line
	}
	switch {
	case o.deployBinary != "" && o.buildSource != "":
		return fmt.Sprintf("[hub] deploy path: %s %s takes precedence over %s %s", deployBinaryFlag, o.deployBinary, buildSourceFlag, o.buildSource)
	case o.deployDefault:
		return fmt.Sprintf("[hub] deploy path: %s %s (default: this hub's own executable)", deployBinaryFlag, o.deployBinary)
	case o.deployBinary != "":
		return fmt.Sprintf("[hub] deploy path: %s %s", deployBinaryFlag, o.deployBinary)
	case o.buildSource != "":
		return fmt.Sprintf("[hub] deploy path: %s %s", buildSourceFlag, o.buildSource)
	}
	return ""
}

// validateDeployFlags validates both deploy flags at startup and stores the one
// value the hub validates, logs, and wires: each flag is trimmed, and each path is
// stored canonically. That is what makes a whitespace-only value not a deploy path
// at all, instead of skipping validation while the raw string is still logged and
// wired as one; and it keeps the artifact the operator named at startup identical
// to the one a later deploy reads. -build-source reuses sshconn's checkout
// validation through its exported entry point so the rules cannot drift;
// -deploy-binary is checked here because only the hub reads the file. Every error
// names the flag the operator must fix. When neither flag is given and -no-deploy
// is not set, the hub's own executable is adopted as the deploy artifact through
// the same validation — so the default cannot wire anything an explicit flag
// could not, and a binary that is not an evener build leaves the hub unwired (a
// named refusal state) rather than being pushed to a host.
func (o *hubOptions) validateDeployFlags() error {
	o.deployBinary = strings.TrimSpace(o.deployBinary)
	if o.deployBinary != "" {
		abs, err := validateDeployBinary(o.deployBinary)
		if err != nil {
			return err
		}
		o.deployBinary = abs
	}
	o.buildSource = strings.TrimSpace(o.buildSource)
	if o.buildSource != "" {
		abs, err := sshconn.ValidateBuildSource(o.buildSource)
		if err != nil {
			return fmt.Errorf("%s %q: %w", buildSourceFlag, o.buildSource, err)
		}
		o.buildSource = abs
	}
	if o.deployBinary == "" && o.buildSource == "" && !o.noDeploy {
		o.defaultDeployBinary()
	}
	return nil
}

// defaultDeployBinary adopts this hub's own running executable as the deploy
// artifact when the operator named no source, so connecting to a bare host
// provisions it with the controller's exact build by default. Resolution and
// validation are the explicit flag's: the executable is run through
// validateDeployBinary (stat, exec bits, readable buildinfo, evener main
// package), and anything that fails leaves the deploy unwired exactly as the
// no-flag case was before this default existed. That is deliberate: an embedder
// or test binary has no evener build to offer, and wiring it would push a
// non-evener program — or nothing — while claiming a deploy source exists. The
// refusal such a hub produces still names the remedy (hubDeployHelp), so the
// state is explicit rather than a silent no-op.
func (o *hubOptions) defaultDeployBinary() {
	exe, err := hubExecutable()
	if err != nil || strings.TrimSpace(exe) == "" {
		return
	}
	abs, err := validateDeployBinary(exe)
	if err != nil {
		return
	}
	o.deployBinary = abs
	o.deployDefault = true
}

// validateDeployBinary checks an operator-supplied artifact where the flag is
// read and returns the canonical absolute path to store. It must be a stat-able
// file the hub can read, carrying the Go buildinfo the seam later reads for its
// target, and that buildinfo's main package must be the evener runtime — so a
// missing path, a directory, an unreadable file, a stripped/non-Go file, or a Go
// program that is not evener is refused now, naming the flag, instead of at the
// first attach. Canonicalizing the path the way the build source is
// (verifyBuildSource) keeps the artifact validation approved the same file a
// later deploy reads.
func validateDeployBinary(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("%s %q: %w", deployBinaryFlag, path, err)
	}
	abs, err = filepath.EvalSymlinks(abs)
	if err != nil {
		return "", fmt.Errorf("%s %q: %w", deployBinaryFlag, path, err)
	}
	info, err := os.Stat(abs)
	if err != nil {
		return "", fmt.Errorf("%s %q: %w", deployBinaryFlag, path, err)
	}
	if info.IsDir() {
		return "", fmt.Errorf("%s %q is a directory, want a pre-built evener executable", deployBinaryFlag, path)
	}
	// The host must be able to exec the artifact. A 0644 Go binary passes every
	// other check here and in the seam, so without this it would validate at
	// startup and then be installed unable to run.
	if info.Mode().Perm()&0o111 == 0 {
		return "", fmt.Errorf("%s %q is not executable (mode %v), want a pre-built evener executable", deployBinaryFlag, path, info.Mode().Perm())
	}
	buildInfo, err := buildinfo.ReadFile(abs)
	if err != nil {
		return "", fmt.Errorf("%s %q is not a readable Go executable: %w", deployBinaryFlag, path, err)
	}
	if err := verifyDeployArtifactIdentity(path, buildInfo); err != nil {
		return "", err
	}
	return abs, nil
}

// deployBinaryBuild is the Options.BuildBinary seam for an operator-supplied
// artifact. There is nothing to compile, so the artifact is verified to target
// goos/goarch and to be evener, then copied to out. Both facts are read from the
// artifact's own buildinfo, never trusted from the operator, so a wrong-platform
// artifact — or a Go program that is not evener at all — is refused before the
// push rather than installed and caught only by the post-push identity check. A
// stripped or non-Go file is a refusal, not a panic.
func deployBinaryBuild(path string) func(ctx context.Context, goos, goarch, out string) error {
	return func(ctx context.Context, goos, goarch, out string) error {
		return copyDeployBinary(ctx, path, goos, goarch, out)
	}
}

// copyDeployBinary verifies and stages one operator-supplied artifact. Nothing
// is written to out until the artifact has been read and its identity and target
// confirmed, so a refused artifact leaves no partial file for the push to
// install.
func copyDeployBinary(ctx context.Context, path, goos, goarch, out string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	info, err := buildinfo.ReadFile(path)
	if err != nil {
		return fmt.Errorf("%s %q is not a readable Go executable: %w", deployBinaryFlag, path, err)
	}
	if err := verifyDeployArtifactIdentity(path, info); err != nil {
		// Terminal, not the retryable ErrDeploy: the artifact is the operator's, so
		// a non-evener file at that path is a permanent mistake that retrying only
		// re-reads — the same classification the wrong-target refusal beside it uses.
		return fmt.Errorf("%w: %w", sshconn.ErrDeployArtifactUnusable, err)
	}
	gotOS, gotArch := buildSetting(info, "GOOS"), buildSetting(info, "GOARCH")
	if gotOS != goos || gotArch != goarch {
		// Terminal, not the retryable ErrDeploy the other push failures use: the
		// artifact is the operator's, so the mismatch is a permanent mistake that
		// retrying only re-reads. The message names the flag to fix and both
		// targets, so the remedy (rebuild for the host, or use -build-source) is
		// visible without tracing the seam.
		return fmt.Errorf("%w: %s %q targets %s/%s, but the host needs %s/%s; supply an evener built for %s/%s, or set %s instead",
			sshconn.ErrDeployArtifactUnusable, deployBinaryFlag, path, gotOS, gotArch, goos, goarch, goos, goarch, buildSourceFlag)
	}
	src, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("%s %q: %w", deployBinaryFlag, path, err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("%s %q: %w", deployBinaryFlag, path, err)
	}
	// The host must be able to exec what it receives, so the staged copy always
	// carries the exec bits — the artifact's own read/write bits are preserved,
	// but a source without them cannot make the staged file unrunnable.
	if err := os.WriteFile(out, data, src.Mode().Perm()|0o111); err != nil {
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
