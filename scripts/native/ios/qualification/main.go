//go:build linux || darwin

// Command qualification builds the real iOS host and extension without publishing.
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
	uiRunner             string
	identity             buildIdentity
	statusReads          int
}

func main() {
	os.Exit(runCLI())
}

func runCLI() int {
	output := flag.String("output", "", "New absolute directory for build diagnostics and DerivedData, must not exist")
	ui := flag.Bool("with-ui", false, "Also run the installed-app qualification journeys inside this run's simulator lifetime")
	flag.Usage = func() {
		_, _ = fmt.Fprintln(flag.CommandLine.Output(), "Usage: build-simulator.sh --output ABSOLUTE_DIR [--with-ui]\nBuilds a Release iPhone simulator app and embedded share extension. Requires the pinned macOS toolchain and installed npm/gem dependencies. Never publishes. Linux refuses before generation. The owned simulator is deleted before exit; artifact paths cannot be reused for a later UI run. With --with-ui the installed-app qualification journeys run as a child inside the same simulator lifetime, and a failed journey fails the run.")
		flag.PrintDefaults()
	}
	flag.Parse()
	if flag.NArg() != 0 || *output == "" {
		flag.Usage()
		return 2
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := qualify(ctx, *output, *ui); err != nil {
		fmt.Fprintln(os.Stderr, err)
		if exit, ok := errors.AsType[*exec.ExitError](err); ok {
			return procgroup.ExitCode(exit.ProcessState)
		}
		return 1
	}
	return 0
}

func qualify(ctx context.Context, output string, withUI bool) (result error) {
	if runtime.GOOS != "darwin" {
		return errMacRequired
	}
	if !filepath.IsAbs(output) || filepath.Clean(output) != output {
		return errors.New("--output must be a new, clean absolute path")
	}
	root, err := os.Getwd()
	if err != nil {
		return err
	}
	r := &runner{root: root, native: filepath.Join(root, "mobile-native"), output: output}
	r.uiRunner = filepath.Join(r.root, "scripts", "native", "ios", "run-ui-tests.sh")
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
	if err := r.toolchain(ctx); err != nil {
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
	listing, err := r.command(ctx, "simulator-runtimes", "xcrun", "simctl", "list", "--json", "runtimes")
	if err != nil {
		return err
	}
	runtimeID, deviceID, err := selectSimulator(listing)
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	// Creating a device has a side effect before stdout delivers its identity.
	// Finish that bounded operation even on cancellation, then delete this ID.
	createCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Minute)
	created, err := r.command(createCtx, "simulator-create", "xcrun", "simctl", "create", "Evener-qualification-"+filepath.Base(output), deviceID, runtimeID)
	cancel()
	if err != nil {
		return err
	}
	udid := strings.TrimSpace(string(created))
	if !regexp.MustCompile(`^[0-9A-Fa-f]{8}(-[0-9A-Fa-f]{4}){3}-[0-9A-Fa-f]{12}$`).MatchString(udid) {
		return errors.New("simctl create did not return a device UUID; inspect simulator-create.log")
	}
	r.identity.SimulatorUDID = udid
	defer func() { result = errors.Join(result, r.cleanupSimulator()) }()
	if err := r.writeJSON("simulator-identity.json", map[string]string{"udid": udid, "runtime": runtimeID, "deviceType": deviceID}); err != nil {
		return err
	}
	if _, err := r.commandOutput(ctx, "xcodebuild-release", false, "xcodebuild", "-workspace", "ios/Evener.xcworkspace", "-scheme", "Evener", "-configuration", "Release", "-sdk", "iphonesimulator", "-destination", "platform=iOS Simulator,id="+udid, "-derivedDataPath", filepath.Join(output, "DerivedData"), "build"); err != nil {
		return err
	}
	r.identity.AppPath = filepath.Join(output, "DerivedData", "Build", "Products", "Release-iphonesimulator", "Evener.app")
	if err := r.verifyApp(ctx, r.identity.AppPath, "built"); err != nil {
		return err
	}
	if err := r.provenanceUnchanged(ctx); err != nil {
		return err
	}
	if !withUI {
		return nil
	}
	// The journeys read the identity as it stands mid-run: the paths and the
	// simulator ID are valid only inside this run's own lifetime.
	if err := r.writeJSON("build-identity.json", r.identity); err != nil {
		return err
	}
	return r.uiJourney(ctx)
}

// uiJourney runs the installed-app qualification journeys as a child inside
// this run's simulator lifetime: the parent holds the simulator until the
// child has stopped — the deferred cleanup runs only after qualify returns —
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

func (r *runner) toolchain(ctx context.Context) error {
	tools := []struct {
		name string
		args []string
		want string
	}{
		{"node", []string{"node", "--version"}, "v22.13.1"},
		{"ruby", []string{"ruby", "-e", "print RUBY_VERSION"}, "3.3.6"},
		{"bundler", []string{"bundle", "exec", "ruby", "-rbundler", "-e", "print Bundler::VERSION"}, "2.7.2"},
		{"cocoapods", []string{"bundle", "exec", "pod", "--version"}, "1.16.2"},
		{"xcode", []string{"xcodebuild", "-version"}, ""},
		{"go", []string{"go", "version"}, ""},
	}
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
// refuses because a local podspec version changed, the lock's own local
// development pods are unlocked with one `pod update --no-repo-update`;
// registry pins are never named and keep their locked versions. A second
// install then completes the candidate lock for review.
func (r *runner) resolvePods(ctx context.Context, committedLock []byte) error {
	if _, err := r.commandOutput(ctx, "pod-resolve", false, "bundle", "exec", "pod", "install", "--project-directory=ios"); err == nil {
		return nil
	} else if ctx.Err() != nil {
		return err
	}
	installLog, readErr := os.ReadFile(filepath.Join(r.output, "pod-resolve.log"))
	if readErr != nil {
		return readErr
	}
	if !podUnlockNeeded(installLog) {
		return errors.New("pod install failed without the changed-local-podspec refusal; inspect pod-resolve.log")
	}
	names := localPodNames(committedLock)
	if len(names) == 0 {
		return errors.New("the committed lock declares no local pods to unlock; inspect pod-resolve.log")
	}
	if _, err := r.commandOutput(ctx, "pod-unlock", false, podUnlockCommand(names)...); err != nil {
		return err
	}
	_, err := r.commandOutput(ctx, "pod-resolve-2", false, "bundle", "exec", "pod", "install", "--project-directory=ios")
	return err
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
