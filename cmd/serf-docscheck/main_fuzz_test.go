package main

import (
	"os"
	"path/filepath"
	"testing"
)

func FuzzCheckPackage(f *testing.F) {
	f.Add("package sample\n// Exported is documented.\ntype Exported struct{}\n")
	f.Add("package sample\ntype Undocumented struct{}\n")
	f.Add(`package sample

// DocumentedFunc is documented.
func DocumentedFunc() {}
func UndocumentedFunc() {}
type receiver struct{}
func (receiver) ExportedMethod() {}

// DocumentedType is documented.
type DocumentedType struct{}
type UndocumentedType struct{}
type (
	GroupedUndocumented struct{}
	// SpecDocumented is documented.
	SpecDocumented struct{}
)

// DocumentedValues are documented.
const ( DocumentedConst = 1 )
const UndocumentedConst = 2
const (
	// SpecDocumentedConst is documented at the spec.
	SpecDocumentedConst = 3
)
var UndocumentedVar, privateVar = 1, 2
`)
	f.Add("package sample_test\ntype IgnoredExternalTestPackage struct{}\n")
	f.Add("not go")
	f.Fuzz(func(t *testing.T, source string) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "input.go"), []byte(source), 0o600); err != nil {
			t.Fatal(err)
		}
		_, _ = checkPackage(dir)
	})
}
