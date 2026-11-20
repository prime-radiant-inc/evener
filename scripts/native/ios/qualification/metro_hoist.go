//go:build linux || darwin

package main

import (
	"context"
	"path/filepath"
)

// E16 lab scaffolding (never leaves the lab branch).
//
// The release build's 88.8 s serial tail begins with the app target at 689.0 s;
// the Metro phase inside it runs 716.2 s -> 758.8 s (42.56 s) while the whole
// box is idle. Metro does not depend on any pod output, so the only reason it
// costs tail seconds is that it lives on the app target, which cannot start
// until every pod target has finished.
//
// This file moves that phase, unchanged, onto a pod target that finishes early
// and rewrites the app phase into a wait-then-copy step: the bundle is produced
// during the compile wave, and the app target pays milliseconds to copy it.

// metroHoistCarrierTarget is the pod target that carries the hoisted phase.
// ZXingObjC finishes at 343.2 s in the E15 timeline, 345.8 s before the app
// target starts, so a 42 s phase on it cannot reach the app target's gate.
const metroHoistCarrierTarget = "ZXingObjC"

// metroHoistStaging is the run-owned directory the hoisted bundle is written to
// and copied from. It lives inside the run output so a failed experiment keeps
// its bundle for inspection.
func metroHoistStaging(output string) string {
	return filepath.Join(output, "metro-hoist")
}

// metroHoistArgs is the patch command vector. The project paths are relative to
// mobile-native because that is the working directory every runner command
// uses; the script path is absolute so it survives a checkout move.
func metroHoistArgs(root, output string) []string {
	return []string{
		"bundle", "exec", "ruby",
		filepath.Join(root, "scripts", "native", "ios", "qualification", "metro_hoist.rb"),
		"--app-project", "ios/Evener.xcodeproj",
		"--pods-project", "ios/Pods/Pods.xcodeproj",
		"--host-target", "Evener",
		"--carrier-target", metroHoistCarrierTarget,
		"--staging", metroHoistStaging(output),
	}
}

// hoistMetroBundle rewrites the generated projects so the Metro phase runs on
// an early pod target instead of in the app target's serial tail. The patch
// script fails loudly when the bundle phase or the carrier target is missing, so
// a run can never measure a no-op as if it were the experiment.
func (r *runner) hoistMetroBundle(ctx context.Context) error {
	_, err := r.command(ctx, "metro-hoist-patch", metroHoistArgs(r.root, r.output)...)
	return err
}
