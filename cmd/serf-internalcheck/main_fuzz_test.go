package main

import (
	"go/token"
	"go/types"
	"testing"
)

func FuzzWalkType(f *testing.F) {
	f.Add("primeradiant.com/serf/agent/internal/example", "Thing", true)
	f.Add("primeradiant.com/serf/agent/example", "Thing", false)
	f.Fuzz(func(t *testing.T, path, name string, pointer bool) {
		if len(path)+len(name) > 4096 || name == "" {
			return
		}
		pkg := types.NewPackage(path, "example")
		obj := types.NewTypeName(token.NoPos, pkg, name, nil)
		var typ types.Type = types.NewNamed(obj, types.NewStruct(nil, nil), nil)
		if pointer {
			typ = types.NewPointer(typ)
		}
		walkType(typ, map[string]bool{}, map[types.Type]bool{})
	})
}
