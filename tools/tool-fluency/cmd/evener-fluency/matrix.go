package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"primeradiant.com/evener/cmdutil"
)

// matrixVersion is one prompt version under test: its label and the evener
// binary built from it.
type matrixVersion struct {
	Label string
	Bin   string
}

// matrixConfigs expands versions and models into one run configuration per
// pair, each writing to <base.outDir>/<label>/<model>.
func matrixConfigs(base runConfig, versions []matrixVersion, models []string) []runConfig {
	var cfgs []runConfig
	for _, v := range versions {
		for _, model := range models {
			cfg := base
			cfg.evenerBin = v.Bin
			cfg.model = model
			cfg.outDir = filepath.Join(base.outDir, safeName(v.Label), safeName(model))
			cfg.systemPromptAppend = slices.Clone(base.systemPromptAppend)
			cfgs = append(cfgs, cfg)
		}
	}
	return cfgs
}

// runMatrixSuite runs one configuration. Tests replace it.
var runMatrixSuite = runSuiteWithConfig

// runMatrix runs every configuration, at most maxConcurrent at once, and
// joins their errors so one failing pair does not stop the others.
func runMatrix(cfgs []runConfig, maxConcurrent int) error {
	sem := make(chan struct{}, maxConcurrent)
	errs := make([]error, len(cfgs))
	var wg sync.WaitGroup
	for i, cfg := range cfgs {
		wg.Go(func() {
			sem <- struct{}{}
			defer func() { <-sem }()
			if err := runMatrixSuite(cfg); err != nil {
				errs[i] = fmt.Errorf("%s: %w", cfg.outDir, err)
			}
		})
	}
	wg.Wait()
	return errors.Join(errs...)
}

func runMatrixCommand(args []string) error {
	fs := flag.NewFlagSet("matrix", flag.ContinueOnError)
	base := runConfig{}
	var systemPromptAppend cmdutil.StringSliceFlag
	defineRunFlags(fs, &base, &systemPromptAppend)
	var versionFlags cmdutil.StringSliceFlag
	fs.Var(&versionFlags, "version", "LABEL=BIN: a prompt version and its evener binary (repeatable)")
	versionManifestPath := fs.String("version-manifest", "", "YAML file mapping labels to git refs; matrix resolves and builds each one itself")
	versionCache := fs.String("version-cache", "", "directory to cache binaries built from --version-manifest, keyed by commit (required with --version-manifest)")
	repo := fs.String("repo", ".", "git repository --version-manifest resolves refs and builds against")
	buildPackage := fs.String("build-package", "./cmd/evener", "package --version-manifest builds for each resolved commit")
	models := fs.String("models", "", "comma-separated models, such as lunarouter/deepseek-4.1-flash")
	maxConcurrent := fs.Int("max-concurrent", 2, "most runs at once; the gateway caps concurrent requests")
	if err := fs.Parse(args); err != nil {
		return err
	}
	var misused []string
	fs.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "model", "evener-bin", "build", "harness":
			misused = append(misused, "--"+f.Name)
		}
	})
	if len(misused) > 0 {
		return fmt.Errorf("matrix sets %s itself; name versions with --version and models with --models", strings.Join(misused, ", "))
	}
	if base.outDir == "" {
		return errors.New("--out is required")
	}
	if *maxConcurrent < 1 {
		return errors.New("--max-concurrent must be at least 1")
	}
	if *versionManifestPath != "" && *versionCache == "" {
		return errors.New("--version-manifest needs --version-cache, a directory to cache the binaries it builds")
	}
	var versions []matrixVersion
	seenLabel := map[string]bool{}
	for _, v := range versionFlags {
		label, bin, err := parseLabeled(v)
		if err != nil {
			return err
		}
		versions = append(versions, matrixVersion{Label: label, Bin: bin})
		seenLabel[label] = true
	}
	if *versionManifestPath != "" {
		manifest, err := loadVersionManifest(*versionManifestPath)
		if err != nil {
			return err
		}
		built, err := versionsFromManifest(context.Background(), *repo, *versionCache, *buildPackage, manifest)
		if err != nil {
			return err
		}
		for _, v := range built {
			if seenLabel[v.Label] {
				return fmt.Errorf("label %q is in both --version and --version-manifest", v.Label)
			}
			seenLabel[v.Label] = true
			versions = append(versions, v)
		}
	}
	var modelList []string
	for m := range strings.SplitSeq(*models, ",") {
		if m = strings.TrimSpace(m); m != "" {
			modelList = append(modelList, m)
		}
	}
	if len(versions) == 0 || len(modelList) == 0 {
		return errors.New("need at least one --version or --version-manifest entry, and one model in --models")
	}
	base.harness = "cli"
	base.systemPromptAppend = []string(systemPromptAppend)
	cfgs := matrixConfigs(base, versions, modelList)
	if err := checkMatrixCells(cfgs); err != nil {
		return err
	}
	// Decide the process-wide hermetic setting once, right after parsing and
	// before any cell (goroutine) runs, so concurrent cells share one setting
	// instead of racing on a per-cell set/restore (#3227). See
	// configureHermeticRunEnv.
	configureHermeticRunEnv(base.inheritOperatorEnv)
	return runMatrix(cfgs, *maxConcurrent)
}

// checkMatrixCells refuses before any run starts when two cells would share a
// directory, as a repeated model or two labels with the same safe name would,
// or when a cell already holds results from an earlier run.
func checkMatrixCells(cfgs []runConfig) error {
	seen := map[string]bool{}
	for _, cfg := range cfgs {
		if seen[cfg.outDir] {
			return fmt.Errorf("two runs would share %s; name each version and each model once", cfg.outDir)
		}
		seen[cfg.outDir] = true
		if holdsResults(cfg.outDir) {
			return fmt.Errorf("%s already holds results; name a new --out or a new version label", cfg.outDir)
		}
	}
	return nil
}
