package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fixtureModule writes a self-contained module whose only dependency is a
// local replace, so `go list -m all` needs no network and no real workspace.
func fixtureModule(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	write := func(rel, body string) {
		t.Helper()
		path := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("go.mod", "module example.com/fixture\n\ngo 1.27\n\nrequire example.com/local v0.0.0\n\nreplace example.com/local => ./local\n")
	write("local/go.mod", "module example.com/local\n\ngo 1.27\n")
	t.Setenv("GOWORK", "off")
	t.Setenv("GOPROXY", "off")
	t.Setenv("GOSUMDB", "off")
	return dir
}

func TestDiffModulesUnchangedGraph(t *testing.T) {
	modules := []module{
		{Path: "example.com/main", Main: true},
		{Path: "github.com/a/b", Version: "v1.2.3"},
	}
	if changes := diffModules(modules, modules); len(changes) != 0 {
		t.Fatalf("unchanged graph reported changes: %+v", changes)
	}
}

func TestDiffModulesReportsAddedRemovedAndChanged(t *testing.T) {
	base := []module{
		{Path: "github.com/old/removed", Version: "v1.0.0"},
		{Path: "github.com/kept/upgraded", Version: "v1.0.0"},
		{Path: "github.com/kept/replaced", Version: "v1.0.0"},
	}
	head := []module{
		{Path: "github.com/kept/upgraded", Version: "v1.2.0"},
		{Path: "github.com/kept/replaced", Version: "v1.0.0", Replace: &module{Path: "example.com/fork", Version: "v1.0.1"}},
		{Path: "github.com/new/added", Version: "v0.9.0"},
	}
	changes := diffModules(base, head)
	if len(changes) != 4 {
		t.Fatalf("changes = %+v, want four", changes)
	}
	byPath := make(map[string]change, len(changes))
	for _, change := range changes {
		byPath[change.Path] = change
	}
	if got := byPath["github.com/new/added"]; got.Base != "" || got.Head != "v0.9.0" {
		t.Errorf("added = %+v", got)
	}
	if got := byPath["github.com/old/removed"]; got.Base != "v1.0.0" || got.Head != "" {
		t.Errorf("removed = %+v", got)
	}
	if got := byPath["github.com/kept/upgraded"]; got.Base != "v1.0.0" || got.Head != "v1.2.0" {
		t.Errorf("upgraded = %+v", got)
	}
	if got := byPath["github.com/kept/replaced"]; got.Head != "v1.0.0 => example.com/fork v1.0.1" {
		t.Errorf("replaced = %+v", got)
	}
}

func TestModuleIdentityNamesAMainModule(t *testing.T) {
	if got := moduleIdentity(module{Path: "example.com/main", Main: true}); got != "(main module)" {
		t.Fatalf("moduleIdentity(main) = %q", got)
	}
}

func TestLicenseName(t *testing.T) {
	for _, test := range []struct {
		name string
		text string
		want string
	}{
		{"mit", "MIT License\n\nPermission is hereby granted, free of charge, to any person", "MIT"},
		{"apache", "Apache License\nVersion 2.0, January 2004", "Apache-2.0"},
		{"bsd", "Redistribution and use in source and binary forms, with or without modification", "BSD"},
		{"mpl", "Mozilla Public License Version 2.0", "MPL-2.0"},
		{"isc", "Permission to use, copy, modify, and/or distribute this software", "ISC"},
		{"gpl", "GNU GENERAL PUBLIC LICENSE Version 3", "GPL"},
		{"unknown", "All rights reserved.", ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := licenseName(test.text); got != test.want {
				t.Fatalf("licenseName(%s) = %q, want %q", test.name, got, test.want)
			}
		})
	}
}

func TestLicenseForReadsTheModuleLicenseFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "LICENSE"), []byte("MIT License\n\nPermission is hereby granted, free of charge"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := licenseFor(module{Dir: dir}); got.Name != "MIT" || got.File != "LICENSE" || !got.Downloaded {
		t.Fatalf("licenseFor = %+v, want the MIT LICENSE", got)
	}

	unknown := t.TempDir()
	if err := os.WriteFile(filepath.Join(unknown, "COPYING"), []byte("All rights reserved."), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := licenseFor(module{Dir: unknown}); got.Name != "" || got.File != "COPYING" || !got.Downloaded {
		t.Fatalf("licenseFor(unknown) = %+v", got)
	}
	if got := licenseFor(module{Dir: t.TempDir()}); got.File != "" || !got.Downloaded {
		t.Fatalf("licenseFor(no file) = %+v", got)
	}
	if got := licenseFor(module{}); got.Downloaded {
		t.Fatalf("licenseFor(no dir) = %+v, want not downloaded", got)
	}
}

func TestRenderChangesListsModulesAndLicenses(t *testing.T) {
	summary := renderChanges("base-sha", []change{
		{Path: "github.com/new/added", Head: "v1.0.0", License: licenseInfo{Name: "MIT", Downloaded: true}},
		{Path: "github.com/old/removed", Base: "v1.0.0", License: licenseInfo{Name: "Apache-2.0", Downloaded: true}},
	})
	for _, want := range []string{"base-sha", "github.com/new/added", "github.com/old/removed", "v1.0.0", "MIT", "Apache-2.0", "—"} {
		if !strings.Contains(summary, want) {
			t.Fatalf("summary does not contain %q:\n%s", want, summary)
		}
	}
}

// The unchanged graph is the live module graph compared with itself, so the
// check passes without needing a reviewer acknowledgement.
func TestListModulesUnchangedGraph(t *testing.T) {
	modules, err := listModules(fixtureModule(t))
	if err != nil {
		t.Fatalf("listModules: %v", err)
	}
	if len(modules) == 0 {
		t.Fatal("listModules returned no modules")
	}
	if changes := diffModules(modules, modules); len(changes) != 0 {
		t.Fatalf("unchanged live graph reported changes: %+v", changes)
	}
}

// The default test path must not touch the network or the real workspace's
// module cache: downloading is opt-in for the CI license listing.
func TestListModulesDoesNotDownload(t *testing.T) {
	saved := downloadModules
	t.Cleanup(func() { downloadModules = saved })
	downloaded := false
	downloadModules = func(string) { downloaded = true }
	if _, err := listModules(fixtureModule(t)); err != nil {
		t.Fatalf("listModules: %v", err)
	}
	if downloaded {
		t.Fatal("listModules downloaded the module graph on the default path")
	}
}

func TestComparisonBase(t *testing.T) {
	if _, compare, err := comparisonBase("pull_request", ""); err == nil || compare {
		t.Fatalf("pull request without a base = compare %v err %v, want a refusal", compare, err)
	}
	if _, compare, err := comparisonBase("workflow_dispatch", ""); err == nil || compare {
		t.Fatalf("dispatch without a base = compare %v err %v, want a refusal", compare, err)
	}
	if base, compare, err := comparisonBase("workflow_dispatch", " base-sha "); err != nil || !compare || base != "base-sha" {
		t.Fatalf("dispatch with a base = %q compare %v err %v", base, compare, err)
	}
	if _, compare, err := comparisonBase("push", ""); err != nil || compare {
		t.Fatalf("push = compare %v err %v, want no comparison", compare, err)
	}
}
