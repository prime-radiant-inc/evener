package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

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
	if got := licenseFor(module{Dir: dir}); got != "MIT" {
		t.Fatalf("licenseFor = %q, want MIT", got)
	}

	unknown := t.TempDir()
	if err := os.WriteFile(filepath.Join(unknown, "COPYING"), []byte("All rights reserved."), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := licenseFor(module{Dir: unknown}); got != "unrecognized (COPYING)" {
		t.Fatalf("licenseFor(unknown) = %q", got)
	}
	if got := licenseFor(module{Dir: t.TempDir()}); got != "no license file" {
		t.Fatalf("licenseFor(no file) = %q", got)
	}
	if got := licenseFor(module{}); got != "unknown (module not downloaded)" {
		t.Fatalf("licenseFor(no dir) = %q", got)
	}
}

func TestRenderChangesListsModulesAndLicenses(t *testing.T) {
	summary := renderChanges("base-sha", []change{
		{Path: "github.com/new/added", Head: "v1.0.0", License: "MIT"},
		{Path: "github.com/old/removed", Base: "v1.0.0", License: "Apache-2.0"},
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
	modules, err := listModules(".")
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
