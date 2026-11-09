//go:build evenerfuzz

package credentials

import "testing"

// FuzzPackageUnion replays every deterministic package scenario under fuzz coverage.

func FuzzPackageUnion(f *testing.F) {

	f.Add(uint8(0))

	f.Fuzz(func(t *testing.T, _ uint8) {

		t.Run("TestStore_LoadMissingFile", TestStore_LoadMissingFile)
		t.Run("TestStore_GetNeverReadsTheEnvironment", TestStore_GetNeverReadsTheEnvironment)
		t.Run("TestStore_SetGetClear", TestStore_SetGetClear)
		t.Run("TestStore_GetIgnoresABlankEntry", TestStore_GetIgnoresABlankEntry)
		t.Run("TestStore_SetWritesMode0600AndLeavesNoTempFile", TestStore_SetWritesMode0600AndLeavesNoTempFile)
		t.Run("TestStore_NamesAreSorted", TestStore_NamesAreSorted)
		t.Run("TestStore_PathIsTheFileItReadsAndWrites", TestStore_PathIsTheFileItReadsAndWrites)
		t.Run("TestStore_PermissionsEnforced", TestStore_PermissionsEnforced)
		t.Run("failure paths", fuzzStoreFailurePaths)
	})
}

func fuzzStoreFailurePaths(t *testing.T) {
	// A path-less store keeps its entries in memory and persists nothing: the
	// name is folded to lower case and the value trimmed on the way in.
	s := &Store{data: fileShape{Schema: 1}}
	if err := s.Set("OPENAI", " value "); err != nil {
		t.Fatal(err)
	}
	t.Setenv("OPENAI_API_KEY", " env ")
	if v, ok := s.Get("openai"); v != "value" || !ok {
		t.Fatalf("Get = %q, %v", v, ok)
	}
	if v, ok := s.Get("custom"); v != "" || ok {
		t.Fatalf("Get of an absent name = %q, %v", v, ok)
	}
}
