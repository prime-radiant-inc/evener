package main

import "testing"

func FuzzLLMCallCoverage(f *testing.F) {
	for scenario := uint8(0); scenario < 4; scenario++ {
		f.Add(scenario)
	}
	f.Fuzz(func(t *testing.T, scenario uint8) {
		switch scenario % 4 {
		case 0:
			TestLLMCallMainParserBranches(t)
		case 1:
			TestLLMCallProfilesAndOptions(t)
		case 2:
			TestRunLLMCallRemainingErrorsAndOptions(t)
		case 3:
			TestLLMCallMainExit(t)
		}
		TestBuildSystemPrompt(t)
		TestReadJSONSchemaFile(t)
		TestWriteJSON(t)
		TestPrintUsage(t)
		TestRunLLMCall_InvalidFormat(t)
		TestRunLLMCall_BadMetadata(t)
		TestRunLLMCall_SchemaReadError(t)
		TestRunLLMCall_JSONFormatParseError(t)
		TestRunLLMCall_VerboseAndSystemAndMeta(t)
		TestRunLLMCall_AllSamplingOptions(t)
		TestRunLLMCall_SchemaVerbosePretty(t)
		TestLLMCallMain_NoPromptWithBadTimeout(t)
		TestLLMCallMain_MissingProviderAndModel(t)
		TestLLMCallMain_HelpFlagReturnsNil(t)
		TestLLMCallMain_UnknownFlagErrors(t)
		TestRunLLMCall_DefaultsToNoSystemPrompt(t)
		TestRunLLMCall_SetsToolChoiceNoneAndNoTools(t)
		TestRunLLMCall_ErrorsOnToolCalls(t)
		TestRunLLMCall_JSONFormat_ParsesAndPrints(t)
		TestRunLLMCall_Schema_ValidatesAndPrints(t)
	})
}
