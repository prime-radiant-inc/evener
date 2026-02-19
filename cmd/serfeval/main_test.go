package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestSerfEvalBuild(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "serfeval")
	cmd := exec.Command("go", "build", "-o", binary, ".")
	cmd.Dir = "."

	// Need to set the working directory to the module root for "go build ."
	// to resolve correctly, so use the package path instead.
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Dir = wd

	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("build failed: %v\n%s", err, out)
	}
}

func TestSerfEvalHelp(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "serfeval")
	build := exec.Command("go", "build", "-o", binary, ".")
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	build.Dir = wd
	out, err := build.CombinedOutput()
	if err != nil {
		t.Fatalf("build failed: %v\n%s", err, out)
	}

	// Running with -help should exit 0 and print usage.
	cmd := exec.Command(binary, "-help")
	output, _ := cmd.CombinedOutput()
	// flag.Usage writes to stderr; -help causes exit 2 in Go's flag package.
	// Just verify it produces usage text.
	if len(output) == 0 {
		t.Error("expected usage output from -help, got nothing")
	}
}

func TestSerfEvalMissingFlags(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "serfeval")
	build := exec.Command("go", "build", "-o", binary, ".")
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	build.Dir = wd
	out, err := build.CombinedOutput()
	if err != nil {
		t.Fatalf("build failed: %v\n%s", err, out)
	}

	// Running without required flags should exit non-zero.
	cmd := exec.Command(binary)
	err = cmd.Run()
	if err == nil {
		t.Error("expected non-zero exit when required flags are missing")
	}
}
