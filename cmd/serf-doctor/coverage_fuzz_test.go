package main

import "testing"

func FuzzDoctorCoverage(f *testing.F) {
	for scenario := range uint8(4) {
		f.Add(scenario)
	}
	f.Fuzz(func(t *testing.T, scenario uint8) {
		switch scenario % 4 {
		case 0:
			t.Run("writers", TestMainAndWriterFailures)
		case 1:
			t.Run("dispatch", TestRunDispatchAndFlagFailures)
		case 2:
			t.Run("outputs", TestDoctorRemainingOutputs)
		case 3:
			t.Run("override", TestLocateOverrideRootLabel)
		}
		t.Run("locate-human", TestRun_LocateHuman)
		t.Run("locate-json", TestRun_LocateJSON)
		t.Run("watches", TestRun_WatchesRunawayFuse)
		t.Run("flags", TestRun_FlagsAfterSelector)
		t.Run("unknown", TestRun_UnknownSubcommand)
		t.Run("help", TestRun_Help)
		t.Run("selector", TestRun_NoSelectorErrors)
		t.Run("apilog-human", TestRun_APILogHuman)
		t.Run("apilog-json", TestRun_APILogJSON)
		t.Run("apilog-flags", TestRun_APILogFlags)
		t.Run("apilog-selector", TestRun_APILogNoSelector)
		t.Run("tree-human", TestRun_TreeHuman)
		t.Run("tree-json", TestRun_TreeJSON)
		t.Run("tree-options", TestRun_TreeDepthAndObservers)
		t.Run("tree-selector", TestRun_TreeNoSelector)
		t.Run("plugins-human", TestRun_PluginsHuman)
		t.Run("plugins-json", TestRun_PluginsJSON)
		t.Run("plugins-root", TestRun_PluginsUnwritableStoreRoot)
	})
}
