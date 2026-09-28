package hub

import (
	"bytes"
	"context"
	"debug/buildinfo"
	"errors"
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

// errArtifactNotEvener is the identity half of validateDeployBinary's refusal,
// separable so the default-adoption path can say which cause it hit: a build
// that read fine and was rejected on its main package is "not an evener build",
// while a path that no longer resolves or a file whose buildinfo cannot be read
// is a different operator problem and must not be described as the first. The
// message text is unchanged for callers that print the refusal; only the
// wrapping lets a caller classify it.
var errArtifactNotEvener = errors.New("artifact is not the evener runtime")

// verifyDeployArtifactIdentity refuses a Go executable whose main package is not
// the evener runtime. subject names the artifact the way the refusal must — the
// flag the operator passed, or the defaulted own-executable state — so the
// message never sends an operator looking for a flag they did not type. It is
// judged from the buildinfo the caller has already read, on both reads of the
// artifact — where the flag is validated at startup and again on the seam before
// the push — so a non-evener program can never reach a host and replace its
// evener; the post-push version comparison then only has to judge the one thing
// buildinfo cannot: whether a genuine evener artifact was built from this
// controller's tree.
func verifyDeployArtifactIdentity(subject string, info *buildinfo.BuildInfo) error {
	if info.Path == evenerMainPackage {
		return nil
	}
	return fmt.Errorf("%w: %s is not evener: its main package is %q, want %q; supply a pre-built evener for the host's target", errArtifactNotEvener, subject, info.Path, evenerMainPackage)
}

// hubDeployHelp is the remedy the sshconn refusals append when the hub has no
// deploy source or turned deploying off. It names the hub's own state and flags;
// sshconn's default ("set Options.BuildSource") names an internal field the
// operator cannot act on. The default sentence leads because that is how a hub
// normally has a deploy source — its own executable — so the refusals a
// -no-deploy hub (or a hub that refused the deploy it had) produces tell the
// operator the shortest way back to a deploy.
const hubDeployHelp = "run this hub without -no-deploy so it deploys its own executable, or set -deploy-binary <path> (a pre-built evener for the host's target) or -build-source <path> (an evener checkout to cross-compile from)"

// hubDeployHelpUnwired is hubDeployHelp for the one state whose first clause
// names no action it can take: a hub whose own executable is not an evener build
// (an embedder, a test binary, or any future hub executable that is not the
// evener runtime) is already running without -no-deploy, so telling it to run
// without -no-deploy describes where it is. The flags it can act on lead
// instead. deployHelp returns this text only when validateDeployFlags read the
// executable and rejected it on its main package (errArtifactNotEvener), so the
// cause it names is the cause that happened; every other adoption failure gets
// hubDeployHelpNoSource.
const hubDeployHelpUnwired = "this hub's own executable is not an evener build, so there is nothing it can deploy by default; set -deploy-binary <path> (a pre-built evener for the host's target) or -build-source <path> (an evener checkout to cross-compile from)"

// hubDeployHelpNoSource is hubDeployHelpUnwired's sibling for the other
// unwired states: the hub could not locate its own executable, or could not read
// it as a Go build at all (a file replaced or removed under a running hub, a
// permission change), or the options never went through validation. Claiming
// "not an evener build" for these would be the opposite of the truth for an
// evener hub whose executable merely moved, so this text states only what is
// known: no own-executable source was adopted, and these are the flags that
// supply one.
const hubDeployHelpNoSource = "this hub has no own-executable deploy source: its own executable could not be located or read as a Go build, so there is nothing to deploy by default; set -deploy-binary <path> (a pre-built evener for the host's target) or -build-source <path> (an evener checkout to cross-compile from)"

// deployDefaultFailure classifies why the own-executable default was not
// adopted, so the unwired state's remedy names the actual cause: an evener hub
// whose executable could not be located or read is a different operator problem
// from one whose executable is not evener at all.
type deployDefaultFailure int

const (
	// deployDefaultNone: the default was adopted, was not attempted (-no-deploy,
	// or a named source won), or the options never went through validation.
	deployDefaultNone deployDefaultFailure = iota
	// deployDefaultNotEvener: the executable read fine and was rejected on its
	// main package (errArtifactNotEvener).
	deployDefaultNotEvener
	// deployDefaultNoSource: the executable could not be located, or could not be
	// read as a Go build, so there is no default source to describe.
	deployDefaultNoSource
)

// deployWiring is the deploy half of sshconn.Options the hub derives from its
// flags. Constructing it is a pure function of hubOptions, so the precedence
// between the two flags is assertable without starting a hub.
type deployWiring struct {
	buildBinary func(ctx context.Context, goos, goarch, out string) error
	buildSource string
	// ownExecutable records that buildBinary serves this hub's own executable,
	// adopted by the no-flag default, rather than an operator-named artifact.
	// sshconn's Options.OwnExecutable carries it, and it is the one source the
	// dirty-controller refusal exempts: these bytes are this hub's build by
	// construction, where a named artifact's "<sha>-dirty" label cannot prove
	// anything against a dirty controller.
	ownExecutable bool
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
	dw := deployWiring{help: o.deployHelp(), disabled: o.noDeploy}
	if o.noDeploy {
		return dw
	}
	switch {
	case o.deployBinary != "":
		dw.buildBinary = o.deployBinarySeam()
		dw.ownExecutable = o.deployDefault
	case o.buildSource != "":
		dw.buildSource = o.buildSource
	}
	return dw
}

// deployBinarySeam builds the BuildBinary seam for the artifact this hub
// deploys: the operator's -deploy-binary, or the own executable the no-flag
// default adopted. Only the defaulted seam is marked as this hub's own build,
// because only it is one.
func (o hubOptions) deployBinarySeam() func(ctx context.Context, goos, goarch, out string) error {
	if o.deployDefault {
		return ownExecutableBuild(o.deployBinary)
	}
	return deployBinaryBuild(o.deployBinary)
}

// deployHelp picks the remedy the refusals name for the state this hub is in.
// After validateDeployFlags, a hub that did not set -no-deploy and validated no
// source at all is the unwired state — the default resolves for every evener
// build — and it is the one state the shared text cannot serve: it is already
// running without -no-deploy, so that clause names no action it can take. Of the
// two unwired texts, the one that asserts a cause ("not an evener build") is
// returned only when that cause is what validateDeployFlags recorded
// (defaultFailure); every other adoption failure reads the text that asserts
// nothing beyond the missing source. Every other state reads the shared text,
// whose default sentence is exactly the action -no-deploy (and a refusal of a
// source that IS configured) has a way back to.
func (o hubOptions) deployHelp() string {
	if o.noDeploy || o.deployBinary != "" || o.buildSource != "" {
		return hubDeployHelp
	}
	switch o.defaultFailure {
	case deployDefaultNotEvener:
		return hubDeployHelpUnwired
	case deployDefaultNoSource, deployDefaultNone:
		return hubDeployHelpNoSource
	}
	return hubDeployHelpNoSource
}

// deployPathLogLine is the one startup line that records the deploy source the
// hub actually chose, so an operator can see whether the default fired, an
// explicit source won, or -no-deploy disabled deploys — and which named sources
// an opt-out overrode. It returns "" when there is no deploy source and nothing
// to say: a non-evener executable (an embedder or test binary) keeps exactly the
// old silence, and its refusals still name the remedy through the unwired
// state's text (hubDeployHelpUnwired or hubDeployHelpNoSource).
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
// refusal such a hub produces still names the remedy for the failure that
// happened (hubDeployHelpUnwired when the executable is not evener,
// hubDeployHelpNoSource otherwise), so the state is explicit rather than a
// silent no-op.
//
// "This hub's own executable" is the evener runtime by packaging, not by
// assumption: cmd/evener-hub is a library package (package hub — no main func,
// no executable), and ./cmd/evener links it in behind `evener hub`
// (cmd/evener/main.go imports it as hubcmd and its command table runs it for
// both `hub` and `serve`), so every launch path a hub has runs the evener
// binary, and the binary `make build-hub` builds is ./cmd/evener/. os.Executable()
// in a running hub is therefore exactly the artifact validateDeployBinary
// accepts (buildinfo.Path == evenerMainPackage) — the test that boots a real hub
// and reads the line this resolution logs asserts it (deploy_default_e2e_test.go).
// It is also why the default cannot fire for an embedder or a test binary: their
// own executable is not that build, and the unwired state is explicit rather
// than silent — the refusals name the remedy for the failure that happened
// (hubDeployHelpUnwired or hubDeployHelpNoSource).
//
// The residual the file read leaves open — the executable replaced under a
// running hub — is documented and accepted where the default's seam reads it
// (ownExecutableBuild).
func (o *hubOptions) defaultDeployBinary() {
	exe, err := hubExecutable()
	if err != nil || strings.TrimSpace(exe) == "" {
		o.defaultFailure = deployDefaultNoSource
		return
	}
	abs, err := validateDeployBinary(exe)
	if err != nil {
		if errors.Is(err, errArtifactNotEvener) {
			o.defaultFailure = deployDefaultNotEvener
		} else {
			o.defaultFailure = deployDefaultNoSource
		}
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
	if err := verifyDeployArtifactIdentity(fmt.Sprintf("%s %q", deployBinaryFlag, path), buildInfo); err != nil {
		return "", err
	}
	return abs, nil
}

// deployArtifact is the binary a deploy stages: the operator's -deploy-binary, or
// the hub's own executable adopted by the no-flag default.
type deployArtifact struct {
	path string
	// ownExecutable records that path is this hub's own executable, adopted
	// without a flag. It changes what a refusal says — the defaulted state's own
	// exits, never a flag the operator did not type — and it is the marker
	// sshconn's dirty-controller exemption reads (Options.OwnExecutable).
	ownExecutable bool
}

// subject names the artifact the way a refusal must: the flag the operator
// passed with its path, or the defaulted own-executable state. A refusal that
// named -deploy-binary for a source nobody named would send the operator looking
// for a flag they never typed.
func (a deployArtifact) subject() string {
	if a.ownExecutable {
		return fmt.Sprintf("this hub's own executable %q (the default deploy source)", a.path)
	}
	return fmt.Sprintf("%s %q", deployBinaryFlag, a.path)
}

// remedy is the clause a refusal of the defaulted artifact appends: the operator
// passed no flag, so the exits a defaulted source has — turn the default off, or
// name another artifact — are the ones the message must offer. An explicit
// artifact's refusals already name their own fix and append nothing.
func (a deployArtifact) remedy() string {
	if a.ownExecutable {
		return fmt.Sprintf("; %s stops deploying this hub's own executable, and %s names a different artifact", noDeployFlag, deployBinaryFlag)
	}
	return ""
}

// ownExecutableBuild is the Options.BuildBinary seam for the defaulted source:
// the hub's own executable, adopted by validateDeployFlags. It is
// deployBinaryBuild plus the marker that says these bytes are this hub's own
// build, which sshconn's dirty-controller rule reads. The file is read at deploy
// time, so a hub whose executable is replaced under it deploys the replacement:
// a cross-commit replacement is caught after the push (errDeployUnstamped — the
// host reports a version this controller did not stamp), and a replacement that
// is itself a dirty build at this controller's own commit reports the same
// "<sha>-dirty" and is not detectable. That residual is accepted rather than
// closed with hash plumbing: such a hub is a locally built dirty controller, and
// the operator who wants the running build's bytes rather than whatever is on
// disk already has the exact artifact by passing no flag at all.
func ownExecutableBuild(path string) func(ctx context.Context, goos, goarch, out string) error {
	art := deployArtifact{path: path, ownExecutable: true}
	return func(ctx context.Context, goos, goarch, out string) error {
		return copyDeployBinary(ctx, art, goos, goarch, out)
	}
}

// deployBinaryBuild is the Options.BuildBinary seam for an operator-named
// artifact. There is nothing to compile and no claim to verify beyond the
// artifact's own: copyDeployBinary checks it targets goos/goarch and is evener,
// then copies it to out.
func deployBinaryBuild(path string) func(ctx context.Context, goos, goarch, out string) error {
	art := deployArtifact{path: path}
	return func(ctx context.Context, goos, goarch, out string) error {
		return copyDeployBinary(ctx, art, goos, goarch, out)
	}
}

// copyDeployBinary verifies and stages one artifact. Nothing is written to out
// until the artifact has been read and its identity and target confirmed, and
// every fact is judged from the bytes of the one read — buildinfo included — so
// a file swapped between the identity check and the copy cannot be staged under
// another file's verdict.
func copyDeployBinary(ctx context.Context, art deployArtifact, goos, goarch, out string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	data, err := os.ReadFile(art.path)
	if err != nil {
		return fmt.Errorf("%s: %w", art.subject(), err)
	}
	info, err := buildinfo.Read(bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("%s is not a readable Go executable: %w", art.subject(), err)
	}
	if err := verifyDeployArtifactIdentity(art.subject(), info); err != nil {
		// Terminal, not the retryable ErrDeploy: the artifact is the operator's, so
		// a non-evener file at that path is a permanent mistake that retrying only
		// re-reads — the same classification the wrong-target refusal beside it uses.
		return fmt.Errorf("%w: %w", sshconn.ErrDeployArtifactUnusable, err)
	}
	gotOS, gotArch := buildSetting(info, "GOOS"), buildSetting(info, "GOARCH")
	if !sshconn.TargetMatches(gotOS, gotArch, goos, goarch) {
		// Terminal, not the retryable ErrDeploy the other push failures use: the
		// artifact is the operator's, so the mismatch is a permanent mistake that
		// retrying only re-reads. The predicate is sshconn's own (TargetMatches),
		// which the manager's fallback dispatch reads too, so the two cannot
		// disagree about what the host's target is. The message names the flag to
		// fix and both targets, so the remedy (rebuild for the host, or use
		// -build-source) is visible without tracing the seam.
		return fmt.Errorf("%w: %s targets %s/%s, but the host needs %s/%s; supply an evener built for %s/%s, or set %s instead%s",
			sshconn.ErrDeployArtifactUnusable, art.subject(), gotOS, gotArch, goos, goarch, goos, goarch, buildSourceFlag, art.remedy())
	}
	src, err := os.Stat(art.path)
	if err != nil {
		return fmt.Errorf("%s: %w", art.subject(), err)
	}
	// The host must be able to exec what it receives, so the staged copy always
	// carries the exec bits — the artifact's own read/write bits are preserved,
	// but a source without them cannot make the staged file unrunnable.
	if err := os.WriteFile(out, data, src.Mode().Perm()|0o111); err != nil {
		return fmt.Errorf("%s: write staged binary %q: %w", art.subject(), out, err)
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
