package main

import "testing"

func FuzzNamingCheckCoverage(f *testing.F) {
	for scenario := range uint8(14) {
		f.Add(scenario)
	}
	f.Fuzz(func(t *testing.T, scenario uint8) {
		switch scenario % 14 {
		case 0:
			t.Run("run", TestRunNamingResultsAndMain)
		case 1:
			t.Run("walker-errors", TestRunInjectedWalkerFailures)
		case 2:
			t.Run("filesystem", TestRunFilesystemFailuresAndSorting)
		case 3:
			t.Run("parser", TestRemainingParserBranches)
		case 4:
			t.Run("exclude-sort", TestRunExcludedFileAndSameFileSort)
		case 5:
			t.Run("string", TestViolationString)
		case 6:
			t.Run("upstream", TestIsUpstreamCamelKey)
		case 7:
			t.Run("toml-tag", TestCheckTOMLTag)
		case 8:
			t.Run("camel-empty", TestToCamelCaseEmpty)
		case 9:
			t.Run("parse-error", TestCheckGoFileParseError)
		case 10:
			t.Run("toml-multiline", TestCheckTOMLFileMultilineAndQuotedKeys)
		case 11:
			t.Run("toml-missing", TestCheckTOMLFileMissingFile)
		case 12:
			t.Run("verbose", TestRunVerboseCoversGoAndTOML)
		case 13:
			t.Run("hidden", TestIsExcludedHiddenSegment)
		}
		t.Run("camel", TestIsCamelCase)
		t.Run("kebab", TestIsKebabCase)
		t.Run("snake", TestIsSnakeCase)
		t.Run("tag", TestTagKey)
		t.Run("suggestions", TestSuggestions)
		t.Run("go-violations", TestCheckGoFile_Violations)
		t.Run("appwire-tag", TestCheckJSONTag_AppwireCarveOut)
		t.Run("provider-tag", TestCheckJSONTag_ProvidersCarveOut)
		t.Run("appwire-file", TestCheckGoFile_AppwirePath)
		t.Run("provider-file", TestCheckGoFile_ProvidersPath)
		t.Run("toml-file", TestCheckTOMLFile)
		t.Run("excluded", TestRun_SkipsExcludedPaths)
	})
}
