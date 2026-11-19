//go:build linux || darwin

// Command qualification builds the real iOS host and extension without
// publishing, and runs the installed-app journeys against those products.
package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"primeradiant.com/evener/internal/devtool/procgroup"
)

//nolint:tagliatelle // This camelCase artifact is the shared build/UI runner contract.
type buildIdentity struct {
	SourceRevision string            `json:"sourceRevision"`
	NpmLockSHA256  string            `json:"npmLockSha256"`
	PodLockSHA256  string            `json:"podLockSha256"`
	SimulatorUDID  string            `json:"simulatorUDID"`
	AppPath        string            `json:"appPath"`
	Tools          map[string]string `json:"tools"`
}

type runner struct {
	root, native, output string
	// derived is the DerivedData root an explicit --derived-data names. The
	// workflow points it at a path outside the run-owned output so a restored
	// build cache lands where xcodebuild reads it; empty means
	// output/DerivedData.
	derived     string
	uiRunner    string
	identity    buildIdentity
	statusReads int
	unlockLogs  int
}

func main() {
	os.Exit(runCLI())
}

func runCLI() int {
	mode := flag.String("mode", "build", "What to run: build (build, verify and prepare the UI test products) or journeys (run the journeys against downloaded products)")
	output := flag.String("output", "", "New absolute directory for diagnostics (both modes) and DerivedData (build mode), must not exist")
	derivedData := flag.String("derived-data", derivedDataFromEnv(), "New or cache-restored absolute directory for DerivedData in build mode; defaults to $IOS_DERIVED_DATA, then DERIVED_DATA inside --output")
	products := flag.String("products", "", "DerivedData-shaped directory carrying Build/Products from the build job (journeys mode)")
	flag.Usage = func() {
		_, _ = fmt.Fprintln(flag.CommandLine.Output(), "Usage: build-simulator.sh --output ABSOLUTE_DIR [--derived-data ABSOLUTE_DIR]\n       run-ui-journeys.sh --products ABSOLUTE_DIR --output ABSOLUTE_DIR\nBuild mode builds a Release iPhone simulator app and embedded share extension, proves the intended journeys cannot run, registers the generated UI test target and builds the test products. Journeys mode runs the installed-app journeys against those downloaded products. Each mode creates and deletes its own simulator, requires the pinned macOS toolchain, and never publishes. Build mode also needs the installed npm/gem dependencies. Linux refuses before generation.")
		flag.PrintDefaults()
	}
	flag.Parse()
	if flag.NArg() != 0 || *output == "" {
		flag.Usage()
		return 2
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	var err error
	switch *mode {
	case "build":
		err = qualify(ctx, *output, *derivedData)
	case "journeys":
		if *products == "" {
			flag.Usage()
			return 2
		}
		err = journeys(ctx, *output, *products)
	default:
		flag.Usage()
		return 2
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		if exit, ok := errors.AsType[*exec.ExitError](err); ok {
			return procgroup.ExitCode(exit.ProcessState)
		}
		return 1
	}
	return 0
}

// qualify builds and verifies the real app, then prepares the UI test products
// against the same DerivedData: the red absent-target pass, the generated test
// target, build-for-testing and the product-completeness check.
func qualify(ctx context.Context, output, derivedData string) (result error) {
	if runtime.GOOS != "darwin" {
		return errMacRequired
	}
	if !filepath.IsAbs(output) || filepath.Clean(output) != output {
		return errors.New("--output must be a new, clean absolute path")
	}
	if derivedData != "" && (!filepath.IsAbs(derivedData) || filepath.Clean(derivedData) != derivedData) {
		return errors.New("--derived-data must be a clean absolute path")
	}
	root, err := os.Getwd()
	if err != nil {
		return err
	}
	r := newRunner(root, output)
	r.derived = derivedData
	if _, err := os.Lstat(filepath.Join(r.native, "ios")); !errors.Is(err, os.ErrNotExist) {
		return errors.New("mobile-native/ios already exists; use a fresh qualification checkout to preserve generated work")
	}
	if err := os.Mkdir(output, 0o700); err != nil {
		return err
	}
	r.identity.Tools = make(map[string]string)
	defer func() { result = errors.Join(result, r.writeJSON("build-identity.json", r.identity)) }()
	if err := r.provenance(ctx); err != nil {
		return err
	}
	if err := r.toolchain(ctx, buildToolchain); err != nil {
		return err
	}
	if err := r.prebuild(ctx); err != nil {
		return err
	}
	committed := filepath.Join(r.native, "Podfile.lock")
	resolved := filepath.Join(r.native, "ios", "Podfile.lock")
	lock, err := os.ReadFile(committed)
	if err != nil {
		return fmt.Errorf("%w: %w", errLockMismatch, err)
	}
	if err := os.WriteFile(resolved, lock, 0o600); err != nil {
		return err
	}
	resolveErr := r.resolvePods(ctx, lock)
	// Retain even an unchanged old input when CocoaPods refuses resolution. A
	// failed resolver is never called a candidate success; the only automatic
	// unlock is the changed-local-podspec refusal, and its result is still
	// compared against the committed lock.
	lockErr := checkLock(ctx, committed, resolved, output)
	if resolveErr != nil || lockErr != nil {
		return errors.Join(resolveErr, lockErr)
	}
	if _, err := r.commandOutput(ctx, "pod-deployment", false, "bundle", "exec", "pod", "install", "--deployment", "--project-directory=ios"); err != nil {
		return err
	}
	if err := checkLock(ctx, committed, resolved, output); err != nil {
		return err
	}
	if err := r.generatedMetadata(ctx); err != nil {
		return err
	}
	return r.withOwnedSimulator(ctx, simulatorName(output), func(udid string) error {
		derived := r.derivedDataDir()
		if _, err := r.commandOutput(ctx, "xcodebuild-release", false, releaseBuildCommand(udid, derived)...); err != nil {
			return err
		}
		r.identity.AppPath = filepath.Join(derived, "Build", "Products", "Release-iphonesimulator", "Evener.app")
		if err := r.verifyApp(ctx, r.identity.AppPath, "built"); err != nil {
			return err
		}
		if err := r.provenanceUnchanged(ctx); err != nil {
			return err
		}
		if err := r.buildForJourneys(ctx); err != nil {
			return err
		}
		return r.requireJourneyProducts(ctx)
	})
}

// journeys runs the installed-app journeys against the products the build job
// uploaded. It validates the downloaded layout before any side effect, then
// creates its own simulator and leaves the deferred cleanup as the run's only
// simulator owner.
func journeys(ctx context.Context, output, products string) (result error) {
	if !filepath.IsAbs(output) || filepath.Clean(output) != output {
		return errors.New("--output must be a new, clean absolute path")
	}
	if !filepath.IsAbs(products) || filepath.Clean(products) != products {
		return errors.New("--products must be a clean absolute path")
	}
	layout, err := journeyProducts(products)
	if err != nil {
		return err
	}
	if runtime.GOOS != "darwin" {
		return errMacRequired
	}
	root, err := os.Getwd()
	if err != nil {
		return err
	}
	r := newRunner(root, output)
	if err := os.Mkdir(output, 0o700); err != nil {
		return err
	}
	r.identity.Tools = make(map[string]string)
	defer func() { result = errors.Join(result, r.writeJSON("build-identity.json", r.identity)) }()
	if err := r.provenance(ctx); err != nil {
		return err
	}
	if err := r.toolchain(ctx, journeyToolchain); err != nil {
		return err
	}
	r.identity.AppPath = layout.App
	return r.withOwnedSimulator(ctx, simulatorName(output), func(udid string) error {
		// The journey child reads the identity as it stands for this run, and
		// the run's own products and simulator are valid only in its lifetime.
		if err := r.writeJSON("build-identity.json", r.identity); err != nil {
			return err
		}
		if err := r.provenanceUnchanged(ctx); err != nil {
			return err
		}
		return r.uiJourney(ctx)
	})
}

func newRunner(root, output string) *runner {
	return &runner{
		root:     root,
		native:   filepath.Join(root, "mobile-native"),
		output:   output,
		uiRunner: filepath.Join(root, "scripts", "native", "ios", "run-ui-tests.sh"),
	}
}

// derivedDataFromEnv is the workflow's handoff. It exports the stable path as
// IOS_DERIVED_DATA, and build mode reads it here rather than taking a flag: a
// pull-request head from before the cache change carries this script without
// that flag, and a flag it does not know would fail every such run.
func derivedDataFromEnv() string {
	return os.Getenv("IOS_DERIVED_DATA")
}

// derivedDataDir is the build mode's DerivedData root. An explicit
// --derived-data names a stable path the workflow restores a cache into; the
// default keeps local runs inside their run-owned output.
func (r *runner) derivedDataDir() string {
	if r.derived != "" {
		return r.derived
	}
	return filepath.Join(r.output, "DerivedData")
}

func simulatorName(output string) string {
	return "Evener-qualification-" + filepath.Base(output)
}

// The runner is Apple silicon and the app is installed into an arm64
// simulator, so ONLY_ACTIVE_ARCH builds the one slice the journeys exercise
// instead of compiling the whole native dependency graph for both.
func releaseBuildCommand(udid, derivedData string) []string {
	return []string{"xcodebuild", "-workspace", "ios/Evener.xcworkspace", "-scheme", "Evener", "-configuration", "Release", "-sdk", "iphonesimulator", "-destination", "platform=iOS Simulator,id=" + udid, "-derivedDataPath", derivedData, "build", "ONLY_ACTIVE_ARCH=YES"}
}

// uiJourney runs the installed-app qualification journeys as a child inside
// the calling run's simulator lifetime: the owner holds the simulator until
// the child has stopped — the deferred cleanup runs only after this returns —
// and the child's exit is the run's own result. The child owns its private
// hub, provider and connection state; the parent never reads them.
func (r *runner) uiJourney(ctx context.Context) error {
	uiOutput := filepath.Join(r.output, "ui")
	if err := os.Mkdir(uiOutput, 0o700); err != nil {
		return err
	}
	if _, err := r.commandOutput(ctx, "ui-journey", false, "bash", r.uiRunner,
		"--build-identity", filepath.Join(r.output, "build-identity.json"),
		"--output", uiOutput); err != nil {
		return err
	}
	return nil
}

// buildForJourneys proves the red side before the target exists, registers the
// generated-only UI test target, and compiles the test products against the
// app products already in this run's DerivedData. A red pass that succeeds
// means the harness cannot be trusted about what it runs.
func (r *runner) buildForJourneys(ctx context.Context) error {
	udid := r.identity.SimulatorUDID
	derived := r.derivedDataDir()
	_, redErr := r.commandOutput(ctx, "absent-target-red", false, redPassCommand(udid, derived)...)
	if err := ctx.Err(); err != nil {
		return err
	}
	redExit := 0
	if redErr != nil {
		var exit *exec.ExitError
		if !errors.As(redErr, &exit) {
			return redErr
		}
		redExit = procgroup.ExitCode(exit.ProcessState)
	}
	if err := os.WriteFile(filepath.Join(r.output, "absent-target-red.exit"), []byte(strconv.Itoa(redExit)+"\n"), 0o600); err != nil {
		return err
	}
	if redExit == 0 {
		return errors.New("the intended journey ran before the qualification target existed; the harness cannot be trusted")
	}
	if _, err := r.commandOutput(ctx, "add-ui-target", false, "bundle", "exec", "ruby", "../scripts/native/ios/add-ui-test-target.rb", "ios/Evener.xcodeproj"); err != nil {
		return err
	}
	_, err := r.commandOutput(ctx, "build-for-testing", false, buildForTestingCommand(udid, derived)...)
	return err
}

func redPassCommand(udid, derivedData string) []string {
	return []string{"xcodebuild", "test", "-workspace", "ios/Evener.xcworkspace", "-scheme", "Evener", "-configuration", "Release", "-only-testing:EvenerQualificationUITests/testSavedHubMarkdownAndDraftRecovery", "-destination", "platform=iOS Simulator,id=" + udid, "-derivedDataPath", derivedData, "ONLY_ACTIVE_ARCH=YES"}
}

func buildForTestingCommand(udid, derivedData string) []string {
	return []string{"xcodebuild", "build-for-testing", "-workspace", "ios/Evener.xcworkspace", "-scheme", "Evener", "-configuration", "Release", "-destination", "platform=iOS Simulator,id=" + udid, "-derivedDataPath", derivedData, "ONLY_ACTIVE_ARCH=YES", "-quiet"}
}

// productsLayout is the products tree both jobs depend on. The journeys restore
// it verbatim: the xctestrun resolves its product paths relative to its own
// location, and the app carries its extension, the runner and the test bundle.
type productsLayout struct {
	ProductsRoot      string
	SimulatorProducts string
	App               string
	Extension         string
	TestRunner        string
	TestBundle        string
	XCTestRuns        []string
}

const (
	appProduct        = "Evener.app"
	extensionProduct  = "Evener.app/PlugIns/EvenerShareExtension.appex"
	testRunnerProduct = "EvenerQualificationUITests-Runner.app"
	testBundleProduct = "EvenerQualificationUITests.xctest"
)

// journeyProducts validates a DerivedData-shaped products tree. It returns an
// error naming every missing product, so the build job never uploads an empty
// or partial artifact and the journeys job never starts from one.
func journeyProducts(derivedData string) (productsLayout, error) {
	layout := productsLayout{
		ProductsRoot:      filepath.Join(derivedData, "Build", "Products"),
		SimulatorProducts: filepath.Join(derivedData, "Build", "Products", "Release-iphonesimulator"),
	}
	layout.App = filepath.Join(layout.SimulatorProducts, appProduct)
	layout.Extension = filepath.Join(layout.SimulatorProducts, filepath.FromSlash(extensionProduct))
	layout.TestRunner = filepath.Join(layout.SimulatorProducts, testRunnerProduct)
	layout.TestBundle = filepath.Join(layout.SimulatorProducts, testBundleProduct)
	var missing []string
	for _, path := range []string{layout.App, layout.Extension, layout.TestRunner} {
		if _, err := os.Stat(path); err != nil {
			missing = append(missing, path)
		}
	}
	// A UI test bundle is embedded in its runner app's PlugIns in some Xcode
	// layouts and placed beside the app in others; both carry the same bundle.
	if _, err := os.Stat(layout.TestBundle); err != nil {
		embedded := filepath.Join(layout.TestRunner, "PlugIns", testBundleProduct)
		if _, embeddedErr := os.Stat(embedded); embeddedErr == nil {
			layout.TestBundle = embedded
		} else {
			missing = append(missing, layout.TestBundle)
		}
	}
	runs, err := xctestRuns(layout.ProductsRoot)
	if err != nil {
		return productsLayout{}, err
	}
	if len(runs) == 0 {
		missing = append(missing, filepath.Join(layout.ProductsRoot, "*.xctestrun"))
	}
	if len(missing) > 0 {
		return productsLayout{}, fmt.Errorf("%w: missing %s", errIncompleteProducts, strings.Join(missing, ", "))
	}
	layout.XCTestRuns = runs
	return layout, nil
}

// xctestRuns finds every generated test-run configuration below the products
// root; xcodebuild may place more than one (one per architecture).
func xctestRuns(productsRoot string) ([]string, error) {
	var runs []string
	err := filepath.WalkDir(productsRoot, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			if errors.Is(walkErr, fs.ErrNotExist) {
				return nil
			}
			return walkErr
		}
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".xctestrun") {
			runs = append(runs, path)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	slices.Sort(runs)
	return runs, nil
}

// requireJourneyProducts records the DerivedData sizes the build job reports,
// then fails the build if any product the journeys need is absent.
func (r *runner) requireJourneyProducts(ctx context.Context) error {
	if err := r.logDerivedDataSizes(ctx); err != nil {
		return err
	}
	_, err := journeyProducts(r.derivedDataDir())
	return err
}

// logDerivedDataSizes retains the products tree and every top-level DerivedData
// directory size in the build job's output, for the caching change that
// follows this split.
func (r *runner) logDerivedDataSizes(ctx context.Context) error {
	derived := r.derivedDataDir()
	paths := []string{filepath.Join(derived, "Build", "Products")}
	entries, err := os.ReadDir(derived)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.IsDir() {
			paths = append(paths, filepath.Join(derived, entry.Name()))
		}
	}
	output, err := r.command(ctx, "derived-data-sizes", append([]string{"du", "-sh"}, paths...)...)
	if err != nil {
		return err
	}
	for line := range strings.Lines(string(output)) {
		fmt.Fprintf(os.Stderr, "iOS qualification: %s\n", strings.TrimSpace(line))
	}
	return nil
}

// createSimulator discovers a supported iPhone runtime, creates this run's
// disposable simulator and records its identity. Creating a device has a side
// effect before stdout delivers its identity, so the bounded operation
// finishes even on cancellation; the caller owns the returned ID.
func (r *runner) createSimulator(ctx context.Context, name string) (string, error) {
	listing, err := r.command(ctx, "simulator-runtimes", "xcrun", "simctl", "list", "--json", "runtimes")
	if err != nil {
		return "", err
	}
	runtimeID, deviceID, err := selectSimulator(listing)
	if err != nil {
		return "", err
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	createCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Minute)
	created, err := r.command(createCtx, "simulator-create", "xcrun", "simctl", "create", name, deviceID, runtimeID)
	cancel()
	if err != nil {
		return "", err
	}
	udid := strings.TrimSpace(string(created))
	if !regexp.MustCompile(`^[0-9A-Fa-f]{8}(-[0-9A-Fa-f]{4}){3}-[0-9A-Fa-f]{12}$`).MatchString(udid) {
		return "", errors.New("simctl create did not return a device UUID; inspect simulator-create.log")
	}
	r.identity.SimulatorUDID = udid
	if err := r.writeJSON("simulator-identity.json", map[string]string{"udid": udid, "runtime": runtimeID, "deviceType": deviceID}); err != nil {
		return "", err
	}
	return udid, nil
}

// withOwnedSimulator makes one run the simulator's sole owner: the device is
// created here and deleted by the deferred cleanup on every exit, including a
// failed run. A create failure owns nothing, so no delete is issued.
func (r *runner) withOwnedSimulator(ctx context.Context, name string, run func(udid string) error) (result error) {
	udid, err := r.createSimulator(ctx, name)
	if err != nil {
		return err
	}
	defer func() { result = errors.Join(result, r.cleanupSimulator()) }()
	return run(udid)
}

func (r *runner) provenance(ctx context.Context) error {
	revision, err := r.command(ctx, "source-revision", "git", "-C", r.root, "rev-parse", "HEAD")
	if err != nil {
		return err
	}
	r.identity.SourceRevision = strings.TrimSpace(string(revision))
	if err := r.provenanceUnchanged(ctx); err != nil {
		return err
	}
	for path, digest := range map[string]*string{"package-lock.json": &r.identity.NpmLockSHA256, "Podfile.lock": &r.identity.PodLockSHA256} {
		data, err := os.ReadFile(filepath.Join(r.native, path))
		if err != nil {
			return err
		}
		hash := sha256.Sum256(data)
		*digest = hex.EncodeToString(hash[:])
	}
	return r.writeJSON("build-identity.json", r.identity)
}

func (r *runner) provenanceUnchanged(ctx context.Context) error {
	r.statusReads++
	data, err := r.command(ctx, "source-status-"+strconv.Itoa(r.statusReads), "git", "-C", r.root, "status", "--porcelain", "--untracked-files=normal")
	if err != nil {
		return err
	}
	if len(data) != 0 {
		return errors.New("qualification requires unchanged committed source; inspect source-status log")
	}
	return nil
}

type toolCheck struct {
	name string
	args []string
	want string
}

// buildToolchain pins every tool the build path invokes directly.
var buildToolchain = []toolCheck{
	{"node", []string{"node", "--version"}, "v22.13.1"},
	{"ruby", []string{"ruby", "-e", "print RUBY_VERSION"}, "3.3.6"},
	{"bundler", []string{"bundle", "exec", "ruby", "-rbundler", "-e", "print Bundler::VERSION"}, "2.7.2"},
	{"cocoapods", []string{"bundle", "exec", "pod", "--version"}, "1.16.2"},
	{"xcode", []string{"xcodebuild", "-version"}, ""},
	{"go", []string{"go", "version"}, ""},
}

// journeyToolchain pins the tools the journeys entry itself invokes. The
// runtime pair is built by the shared recipes, whose frontend build carries its
// own toolchain policy; node/ruby/cocoapods checks here would not be honest
// about what this entry uses.
var journeyToolchain = []toolCheck{
	{"xcode", []string{"xcodebuild", "-version"}, ""},
	{"go", []string{"go", "version"}, ""},
}

func (r *runner) toolchain(ctx context.Context, tools []toolCheck) error {
	for _, tool := range tools {
		data, err := r.command(ctx, "tool-"+tool.name, tool.args...)
		r.identity.Tools[tool.name] = strings.TrimSpace(string(data))
		if err != nil {
			return err
		}
		if tool.want != "" && r.identity.Tools[tool.name] != tool.want {
			return fmt.Errorf("%s must be %s, observed %s", tool.name, tool.want, r.identity.Tools[tool.name])
		}
	}
	fields := strings.Fields(r.identity.Tools["xcode"])
	if len(fields) < 2 || fields[0] != "Xcode" || fields[1] != "26.6" {
		return errors.New("qualification requires Xcode 26.6")
	}
	for _, help := range []struct{ name, tool string }{{"simctl", "simctl"}, {"xcresulttool", "xcresulttool"}} {
		if _, err := r.commandOutput(ctx, "help-"+help.name, false, "xcrun", help.tool, "help"); err != nil {
			return err
		}
	}
	return nil
}

func (r *runner) prebuild(ctx context.Context) error {
	script := filepath.Join(r.root, "scripts", "native", "ios", "qualification", "project-canonicalize.mjs")
	var first map[string]string
	for _, phase := range []string{"first", "repeat"} {
		if _, err := r.commandOutput(ctx, "prebuild-"+phase, false, prebuildCommand(phase)...); err != nil {
			return err
		}
		files := make(map[string]string)
		base := filepath.Join(r.native, "ios")
		if err := filepath.WalkDir(base, func(path string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil || entry.IsDir() {
				return walkErr
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			rel, err := filepath.Rel(base, path)
			if err != nil {
				return err
			}
			hash := sha256.Sum256(data)
			files[rel] = hex.EncodeToString(hash[:])
			return nil
		}); err != nil {
			return err
		}
		identity, err := r.projectIdentity(ctx, phase, script, base)
		if err != nil {
			return err
		}
		files[generatedProjectFile] = identity
		if err := r.writeJSON("prebuild-"+phase+".json", files); err != nil {
			return err
		}
		if phase == "first" {
			first = files
		} else if !reflect.DeepEqual(first, files) {
			return errors.New("repeated prebuild changed generated output; compare prebuild-first.json, prebuild-repeat.json and prebuild-project-*.json")
		}
	}
	return r.provenanceUnchanged(ctx)
}

// projectIdentity hashes the canonical parsed form of the generated Xcode
// project. The canonical JSON is retained as a mismatch diagnostic.
func (r *runner) projectIdentity(ctx context.Context, phase, script, base string) (string, error) {
	canonical := filepath.Join(r.output, "prebuild-project-"+phase+".json")
	args := canonicalProjectCommand(script, r.native, filepath.Join(base, generatedProjectFile), canonical)
	if _, err := r.commandOutput(ctx, "prebuild-project-"+phase, false, args...); err != nil {
		return "", err
	}
	data, err := os.ReadFile(canonical)
	if err != nil {
		return "", err
	}
	hash := sha256.Sum256(data)
	return hex.EncodeToString(hash[:]), nil
}

// resolvePods installs against the copied committed lock. When CocoaPods
// refuses because a local podspec version changed, only the pods that refusal
// names are unlocked with `pod update --no-repo-update`; the install repeats
// while CocoaPods names another changed pod, so unrelated local parents stay
// locked. The update itself can refuse on another still-locked local pod, so
// its log is read the same way and the accumulated validated names retry the
// update. A name the committed lock does not declare as local, or a refusal
// that names nothing new, is surfaced instead of widening the update. Registry
// pods reachable only through a named local pod may still move with their
// parent (the voice-pod candidate moved onnxruntime alongside the new
// EvenerVoice pod), so the committed-lock comparison plus review remains the
// guard on what reaches the branch.
func (r *runner) resolvePods(ctx context.Context, committedLock []byte) error {
	var local, unlocked []string
	for attempt := 1; ; attempt++ {
		logName := "pod-resolve"
		if attempt > 1 {
			logName = "pod-resolve-" + strconv.Itoa(attempt)
		}
		if _, err := r.commandOutput(ctx, logName, false, "bundle", "exec", "pod", "install", "--project-directory=ios"); err == nil {
			return nil
		} else if ctx.Err() != nil {
			return err
		}
		installLog, readErr := os.ReadFile(filepath.Join(r.output, logName+".log"))
		if readErr != nil {
			return readErr
		}
		if !podUnlockNeeded(installLog) {
			return fmt.Errorf("pod install failed without the changed-local-podspec refusal; inspect %s.log", logName)
		}
		if local == nil {
			local = localPodNames(committedLock)
			if len(local) == 0 {
				return fmt.Errorf("the committed lock declares no local pods to unlock; inspect %s.log", logName)
			}
		}
		named := podRefusalNames(installLog)
		if len(named) == 0 {
			return fmt.Errorf("the changed-local-podspec refusal named no pod; inspect %s.log", logName)
		}
		added := false
		for _, name := range named {
			if !slices.Contains(local, name) {
				return fmt.Errorf("%w: %s; inspect %s.log", errPodRefusalName, name, logName)
			}
			if !slices.Contains(unlocked, name) {
				unlocked = append(unlocked, name)
				added = true
			}
		}
		if !added {
			return fmt.Errorf("%w; inspect %s.log", errPodRefusalStuck, logName)
		}
		next, err := r.unlockPods(ctx, local, unlocked)
		if err != nil {
			return err
		}
		unlocked = next
	}
}

// unlockPods re-resolves every local pod validated so far and reads a
// changed-local-podspec refusal out of the update's own log, because updating
// one named pod can refuse on another still-locked local pod before the
// candidate lock is complete. Each retry carries the accumulated names so the
// update resolves them together. Log names continue across attempts: the
// first is pod-unlock, later ones pod-unlock-N.
func (r *runner) unlockPods(ctx context.Context, local, unlocked []string) ([]string, error) {
	for {
		r.unlockLogs++
		logName := "pod-unlock"
		if r.unlockLogs > 1 {
			logName = "pod-unlock-" + strconv.Itoa(r.unlockLogs)
		}
		if _, err := r.commandOutput(ctx, logName, false, podUnlockCommand(unlocked)...); err == nil {
			return unlocked, nil
		} else if ctx.Err() != nil {
			return nil, err
		}
		updateLog, readErr := os.ReadFile(filepath.Join(r.output, logName+".log"))
		if readErr != nil {
			return nil, readErr
		}
		if !podUnlockNeeded(updateLog) {
			return nil, fmt.Errorf("pod update failed without the changed-local-podspec refusal; inspect %s.log", logName)
		}
		named := podRefusalNames(updateLog)
		if len(named) == 0 {
			return nil, fmt.Errorf("the changed-local-podspec refusal named no pod; inspect %s.log", logName)
		}
		added := false
		for _, name := range named {
			if !slices.Contains(local, name) {
				return nil, fmt.Errorf("%w: %s; inspect %s.log", errPodRefusalName, name, logName)
			}
			if !slices.Contains(unlocked, name) {
				unlocked = append(unlocked, name)
				added = true
			}
		}
		if !added {
			return nil, fmt.Errorf("%w; inspect %s.log", errPodRefusalStuck, logName)
		}
	}
}

func (r *runner) generatedMetadata(ctx context.Context) error {
	for _, target := range []string{"Evener", "EvenerShareExtension"} {
		data, err := r.command(ctx, "settings-"+target, "xcodebuild", "-project", "ios/Evener.xcodeproj", "-target", target, "-configuration", "Release", "-sdk", "iphonesimulator", "-showBuildSettings", "-json")
		if err != nil {
			return err
		}
		var targets []struct {
			Target        string
			BuildSettings map[string]string
		}
		if err := json.Unmarshal(data, &targets); err != nil {
			return err
		}
		if len(targets) != 1 || targets[0].Target != target {
			return fmt.Errorf("%w: missing generated target %s", errIdentity, target)
		}
		settings := targets[0].BuildSettings
		info, err := r.plist(ctx, "generated-info-"+target, filepath.Join(r.native, "ios", settings["INFOPLIST_FILE"]))
		if err != nil {
			return err
		}
		entitlements, err := r.plist(ctx, "generated-entitlements-"+target, filepath.Join(r.native, "ios", settings["CODE_SIGN_ENTITLEMENTS"]))
		if err != nil {
			return err
		}
		if err := validateGeneratedSettings(info, entitlements, settings, target != "Evener"); err != nil {
			return err
		}
	}
	return nil
}

func (r *runner) verifyApp(ctx context.Context, app, phase string) error {
	if _, err := os.Stat(filepath.Join(app, "main.jsbundle")); err != nil {
		return fmt.Errorf("embedded Metro bundle: %w", err)
	}
	for _, bundle := range []struct{ name, path string }{{"host", app}, {"share", filepath.Join(app, "PlugIns", "EvenerShareExtension.appex")}} {
		name := phase + "-" + bundle.name
		info, err := r.plist(ctx, name+"-info", filepath.Join(bundle.path, "Info.plist"))
		if err != nil {
			return err
		}
		if _, err := r.commandOutput(ctx, name+"-signature", false, "codesign", "--verify", "--deep", "--strict", bundle.path); err != nil {
			return err
		}
		xml, err := r.command(ctx, name+"-entitlements", "codesign", "--display", "--entitlements", "-", "--xml", bundle.path)
		if err != nil {
			return err
		}
		path := filepath.Join(r.output, name+"-entitlements.plist")
		if err := os.WriteFile(path, xml, 0o600); err != nil {
			return err
		}
		entitlements, err := r.plist(ctx, name+"-entitlements-json", path)
		if err != nil {
			return err
		}
		var embedded map[string]any
		if len(entitlements) == 0 {
			// Simulator builds embed the real entitlements in the binary and
			// leave the signature's entitlement blob empty; read the embedded
			// section so the capability check still sees the product's own
			// entitlements.
			embedded, err = r.embeddedEntitlements(ctx, name, bundle.path, info)
			if err != nil {
				return err
			}
		}
		entitlements = productEntitlements(entitlements, embedded)
		if err := validateBundleMetadata(info, entitlements, bundle.name == "share"); err != nil {
			return err
		}
	}
	return nil
}

// embeddedEntitlements extracts the bundle executable's __TEXT,__entitlements
// section and parses it, retaining the decoded plist as evidence.
func (r *runner) embeddedEntitlements(ctx context.Context, name, bundle string, info map[string]any) (map[string]any, error) {
	executable, _ := info["CFBundleExecutable"].(string)
	if executable == "" {
		return nil, fmt.Errorf("%w: %s has no CFBundleExecutable", errIdentity, bundle)
	}
	dump, err := r.command(ctx, name+"-embedded-entitlements-dump", "xcrun", "otool", "-X", "-s", "__TEXT", "__entitlements", filepath.Join(bundle, executable))
	if err != nil {
		return nil, err
	}
	plistBytes, err := decodeSectionDump(dump)
	if err != nil {
		return nil, err
	}
	path := filepath.Join(r.output, name+"-embedded-entitlements.plist")
	if err := os.WriteFile(path, plistBytes, 0o600); err != nil {
		return nil, err
	}
	return r.plist(ctx, name+"-embedded-entitlements-json", path)
}

func (r *runner) plist(ctx context.Context, name, path string) (map[string]any, error) {
	data, err := r.command(ctx, name, "plutil", "-convert", "json", "-o", "-", path)
	if err != nil {
		return nil, err
	}
	var result map[string]any
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, err
	}
	return result, nil
}

func (r *runner) cleanupSimulator() error {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	udid := r.identity.SimulatorUDID
	data, listErr := r.command(ctx, "simulator-before-cleanup", "xcrun", "simctl", "list", "--json", "devices")
	var listing struct {
		Devices map[string][]struct{ UDID, State string }
	}
	parseErr := json.Unmarshal(data, &listing)
	var shutdownErr error
	for _, devices := range listing.Devices {
		for _, device := range devices {
			if device.UDID == udid && device.State == "Booted" {
				_, shutdownErr = r.commandOutput(ctx, "simulator-shutdown", false, "xcrun", "simctl", "shutdown", udid)
			}
		}
	}
	_, deleteErr := r.commandOutput(ctx, "simulator-delete", false, "xcrun", "simctl", "delete", udid)
	return errors.Join(listErr, parseErr, shutdownErr, deleteErr)
}

func (r *runner) writeJSON(name string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(r.output, name), append(data, '\n'), 0o600)
}

func (r *runner) command(ctx context.Context, name string, args ...string) ([]byte, error) {
	return r.commandOutput(ctx, name, true, args...)
}

func (r *runner) commandOutput(ctx context.Context, name string, capture bool, args ...string) (output []byte, result error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	logPath := filepath.Join(r.output, name+".log")
	log, err := os.OpenFile(logPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, err
	}
	defer func() { result = errors.Join(result, log.Close()) }()
	fmt.Fprintf(os.Stderr, "iOS qualification: %s (%s)\n", name, logPath)
	cmd := exec.Command(args[0], args[1:]...) //nolint:noctx // Cancellation below stops and reaps the whole owned process group, not just the direct child.
	cmd.Dir = r.native
	cmd.Env = append(os.Environ(), "CI=1", "APPLE_TEAM_ID=EVENERCI01")
	var stdout bytes.Buffer
	cmd.Stdout, cmd.Stderr = log, log
	if capture {
		cmd.Stdout = io.MultiWriter(log, &stdout)
	}
	if err := procgroup.Start(cmd); err != nil {
		return nil, err
	}
	done, reaped := make(chan error, 1), make(chan struct{})
	go func() {
		err := cmd.Wait()
		close(reaped)
		done <- err
	}()
	select {
	case err = <-done:
	case <-ctx.Done():
		procgroup.Stop(cmd.Process.Pid, reaped, 5*time.Second)
		err = errors.Join(ctx.Err(), <-done)
	}
	if err != nil {
		return stdout.Bytes(), fmt.Errorf("%s failed, inspect %s: %w", name, logPath, err)
	}
	return stdout.Bytes(), nil
}
