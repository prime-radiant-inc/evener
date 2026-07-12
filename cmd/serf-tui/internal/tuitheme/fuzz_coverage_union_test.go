//go:build serffuzz

package tuitheme

import "testing"

func init() {
	fuzzCoverageUnion = func(t *testing.T) {
		TestActiveTheme_FallsBackToDark(t)
		TestApplyTheme_DarkAndLightDiffer(t)
		TestBgNotEqualText(t)
		TestColorToHex(t)
		TestDetectSystemThemeKey_FromProbe(t)
		TestDetectSystemThemeKey_ProbeUnavailableFallback(t)
		TestInitThemeFromStateDir_FallsBackToSystem(t)
		TestInitTheme_SetsStyles(t)
		TestLoadThemePreference_Failures(t)
		TestNoTokenIsEmpty(t)
		TestSaveThemePreference_NoOpCases(t)
		TestSetMarkdownInvalidator_InvokedOnThemeChange(t)
		TestSetThemeAndPersist_InvalidNameReturnsFalse(t)
		TestSetThemeCallsMarkdownInvalidator(t)
		TestSetThemeChangesActiveTheme(t)
		TestSetThemeIgnoresUnknown(t)
		TestSetTheme_Dark(t)
		TestSetTheme_Invalid(t)
		TestSetTheme_Light(t)
		TestSetTheme_System(t)
		TestTUIStylesRenderSelectedRow(t)
		TestTerminalBgHelpers_NoTTYNoOp(t)
		TestTextTiersMeetMinContrast(t)
		TestThemePreferencePath_EmptyStateDir(t)
		TestThemePreferencePersistsInStateDir(t)
		TestThemeRegistryHasDarkAndLight(t)
		TestThemeStructFieldsPopulated(t)
		TestValidThemeName(t)

	}
}
