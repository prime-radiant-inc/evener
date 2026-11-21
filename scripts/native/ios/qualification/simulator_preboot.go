//go:build linux || darwin

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
)

// This file carries the pre-boot path: the workflow creates the run's
// simulator before the journeys step and the journeys mode adopts it. It is a
// file of its own because checkout-ref.sh requires it by path: a pull request
// head that predates simulator pre-boot lacks it, so the selector falls back
// to the merge ref and the new workflow step never builds an older runner that
// cannot answer --mode simulator.

// simulatorDevice is one row of a `simctl list --json devices` listing.
type simulatorDevice struct {
	UDID  string
	State string
}

// simulatorDevices is the listing's shape: runtime identifiers mapped to the
// devices that use them.
type simulatorDevices map[string][]simulatorDevice

func parseSimulatorDevices(data []byte) (simulatorDevices, error) {
	var listing struct{ Devices simulatorDevices }
	if err := json.Unmarshal(data, &listing); err != nil {
		return nil, err
	}
	return listing.Devices, nil
}

// holds reports whether the listing still carries the device.
func (d simulatorDevices) holds(udid string) bool {
	for _, runtimeDevices := range d {
		if slices.ContainsFunc(runtimeDevices, func(device simulatorDevice) bool { return device.UDID == udid }) {
			return true
		}
	}
	return false
}

// prepareSimulator creates this run's simulator and starts its boot, then
// exits without waiting: the boot proceeds while the workflow downloads and
// unpacks its artifacts, and the journeys mode adopts the device through
// --simulator-dir. Waiting stays where it always was, in the journey script's
// bootstatus, because only that step knows when a booted device is needed.
func prepareSimulator(ctx context.Context, output string) error {
	if !filepath.IsAbs(output) || filepath.Clean(output) != output {
		return errors.New("--output must be a new, clean absolute path")
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
	return r.bootSimulator(ctx)
}

// bootSimulator creates the run's device and starts its boot. simctl returns
// as soon as the device is Booting, so the wait stays in the journey script's
// bootstatus — the only step that knows when it needs a booted device.
func (r *runner) bootSimulator(ctx context.Context) error {
	udid, err := r.createSimulator(ctx, simulatorName(r.output))
	if err != nil {
		return err
	}
	_, err = r.command(ctx, "simulator-boot", "xcrun", "simctl", "boot", udid)
	return err
}

// adoptSimulator takes over the device prepareSimulator created. The identity
// file is the two steps' only contract, so a device that is no longer listed
// fails here, before the journey child spends a minute discovering it, and
// the device's own log travels into the run's output for the artifact.
func (r *runner) adoptSimulator(ctx context.Context, dir string) error {
	data, err := os.ReadFile(filepath.Join(dir, "simulator-identity.json"))
	if err != nil {
		return fmt.Errorf("read the pre-booted simulator identity: %w", err)
	}
	var identity struct {
		UDID string `json:"udid"`
	}
	if err := json.Unmarshal(data, &identity); err != nil {
		return fmt.Errorf("parse the pre-booted simulator identity: %w", err)
	}
	if !deviceUUIDPattern.MatchString(identity.UDID) {
		return errors.New("the pre-booted simulator identity carries no device UUID")
	}
	listing, err := r.command(ctx, "simulator-adopt", "xcrun", "simctl", "list", "--json", "devices")
	if err != nil {
		return err
	}
	devices, err := parseSimulatorDevices(listing)
	if err != nil {
		return err
	}
	if !devices.holds(identity.UDID) {
		return fmt.Errorf("%w: %s", errSimulatorGone, identity.UDID)
	}
	r.identity.SimulatorUDID = identity.UDID
	for _, name := range []string{"simulator-identity.json", "simulator-runtimes.log", "simulator-create.log", "simulator-boot.log"} {
		if err := copyEvidence(filepath.Join(dir, name), filepath.Join(r.output, name)); err != nil {
			return err
		}
	}
	return nil
}

// copyEvidence carries the pre-boot step's diagnostics into the run output
// the workflow uploads. A log the step never wrote is not an error: the
// identity file it wrote is the contract, and it was already read.
func copyEvidence(source, destination string) error {
	data, err := os.ReadFile(source)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	return os.WriteFile(destination, data, 0o600)
}
