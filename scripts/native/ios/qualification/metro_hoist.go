//go:build linux || darwin

package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"primeradiant.com/evener/envvars"
	"primeradiant.com/evener/internal/devtool/procgroup"
)

// The release build's serial tail opens with the app target, and 42.56 s of it
// is the Metro phase while the compile wave is already done. Metro depends on
// no pod output, so that phase can run beside the build instead of at the end
// of it. Moving it onto another target does not achieve that: xcodebuild
// batches script phases, so the carrier target's compile time does not decide
// when the bundle runs.
//
// This file takes the scheduler out of it. The tool rewrites the app phase into
// a wait-then-copy step, emits the app phase's own script text, and runs that
// text at the start of the build — the bundle overlaps the whole compile wave
// instead of the last part of it.

// metroHoistStaging is the run-owned directory the hoisted bundle is written to
// and copied from. It lives inside the run output so a failed run keeps its
// bundle for inspection.
func metroHoistStaging(output string) string {
	return filepath.Join(output, "metro-hoist")
}

// metroHoistArgs is the patch command vector. The project path is relative to
// mobile-native because that is the working directory every runner command
// uses; the script and emitted paths are absolute.
func metroHoistArgs(root, output string) []string {
	staging := metroHoistStaging(output)
	return []string{
		"bundle", "exec", "ruby",
		filepath.Join(root, "scripts", "native", "ios", "qualification", "metro_hoist.rb"),
		"--app-project", "ios/Evener.xcodeproj",
		"--host-target", "Evener",
		"--staging", staging,
		"--emit", metroHoistScript(output),
	}
}

// metroHoistScript is the emitted copy of the app phase's script. It sits
// outside the staging directory so the copy phase moves only bundle outputs
// into the app.
func metroHoistScript(output string) string {
	return filepath.Join(output, "metro-hoist.sh")
}

// metroHoistDerived is where Metro keeps its intermediate files. It sits beside
// the staging directory so the bundle outputs and the derived files stay apart.
func metroHoistDerived(staging string) string {
	return filepath.Join(filepath.Dir(staging), "metro-hoist-derived")
}

// metroHoistEnvKeys are the app target's own resolved build settings the Metro
// script reads. They come from the host target's -showBuildSettings output, so
// the hoisted script sees the values Xcode would have given the app phase;
// nothing here is a guessed constant.
var metroHoistEnvKeys = []string{
	"SRCROOT", "PROJECT_DIR", "PODS_ROOT", "CONFIGURATION", "PLATFORM_NAME",
	"TARGETED_DEVICE_FAMILY", "IPHONEOS_DEPLOYMENT_TARGET", "PRODUCT_SETTINGS_PATH",
	"PRODUCT_NAME", "TARGET_NAME", "SDKROOT", "DEVELOPER_DIR",
}

// metroHoistEnv builds the environment overrides: the host's settings plus the
// three values the hoist redirects.
func metroHoistEnv(settings map[string]string, staging string) map[string]string {
	env := make(map[string]string, len(metroHoistEnvKeys)+3)
	for _, key := range metroHoistEnvKeys {
		if value := settings[key]; value != "" {
			env[key] = value
		}
	}
	env["CONFIGURATION_BUILD_DIR"] = staging
	env["UNLOCALIZED_RESOURCES_FOLDER_PATH"] = ""
	env["DERIVED_FILE_DIR"] = metroHoistDerived(staging)
	return env
}

// metroHoistRun is the background bundle process.
type metroHoistRun struct {
	logPath string
	log     *os.File
	done    chan error
	reaped  chan struct{}
	pid     int
}

// hoistMetroBundle rewrites the app phase into the copy step, emits the phase's
// own script text, and starts it. The patch script fails loudly when the bundle
// phase is missing, so a run can never silently measure a no-op.
func (r *runner) hoistMetroBundle(ctx context.Context) error {
	staging := metroHoistStaging(r.output)
	// The hoisted script runs outside xcodebuild, so without the app target's
	// resolved settings it would bundle with the wrong project environment and
	// the build would still look green. Fail before anything runs.
	if len(r.hostSettings) == 0 {
		return errors.New("metro hoist: no resolved build settings for the app target")
	}
	if _, err := r.command(ctx, "metro-hoist-patch", metroHoistArgs(r.root, r.output)...); err != nil {
		return err
	}
	if err := os.MkdirAll(metroHoistDerived(staging), 0o700); err != nil {
		return err
	}
	logPath := filepath.Join(r.output, "metro-hoist.log")
	log, err := os.OpenFile(logPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "iOS qualification: metro-hoist (%s)\n", logPath)
	cmd := exec.Command("/bin/bash", metroHoistScript(r.output)) //nolint:noctx // Cancellation stops and reaps the whole owned process group, not just the direct child.
	cmd.Dir = r.native
	cmd.Env = envvars.ApplyOverlay(commandEnv(), metroHoistEnv(r.hostSettings, staging))
	cmd.Stdout, cmd.Stderr = log, log
	if err := procgroup.Start(cmd); err != nil {
		return errors.Join(err, log.Close())
	}
	run := &metroHoistRun{logPath: logPath, log: log, done: make(chan error, 1), reaped: make(chan struct{}), pid: cmd.Process.Pid}
	r.hoist = run
	go func() {
		err := cmd.Wait()
		close(run.reaped)
		run.done <- err
	}()
	// A bundle that dies in its first moment (a missing module, a bad flag) must
	// fail the run now, not after the app phase has waited out its marker
	// deadline.
	select {
	case err := <-run.done:
		r.hoist = nil
		closeErr := run.log.Close()
		if err != nil {
			return errors.Join(fmt.Errorf("metro-hoist exited before the build, inspect %s: %w", logPath, err), closeErr)
		}
		return closeErr
	case <-time.After(2 * time.Second):
	}
	return nil
}

// waitMetroHoist joins the hoisted bundle. A failure here fails the run even
// when the build looked green: the copy phase moves whatever the script left
// behind, and a half-written bundle would otherwise surface only in the
// journeys job.
func (r *runner) waitMetroHoist(ctx context.Context) error {
	run := r.hoist
	if run == nil {
		return nil
	}
	r.hoist = nil
	var err error
	select {
	case err = <-run.done:
	case <-ctx.Done():
		procgroup.Stop(run.pid, run.reaped, 5*time.Second)
		err = errors.Join(ctx.Err(), <-run.done)
	}
	closeErr := run.log.Close()
	if err != nil {
		err = fmt.Errorf("metro-hoist failed, inspect %s: %w", run.logPath, err)
	}
	return errors.Join(err, closeErr)
}
