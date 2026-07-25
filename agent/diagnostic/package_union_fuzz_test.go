//go:build serffuzz

package diagnostic

import "testing"

// FuzzPackageUnion replays every deterministic package scenario under fuzz coverage.

func FuzzPackageUnion(f *testing.F) {

	f.Add(uint8(0))

	f.Fuzz(func(t *testing.T, _ uint8) {

		t.Run("TestClassifyUnknownProviderAsSerfConfiguration", TestClassifyUnknownProviderAsSerfConfiguration)
		t.Run("TestDefaultForEverySource", TestDefaultForEverySource)
		t.Run("TestDefaultForSerfConfiguration", TestDefaultForSerfConfiguration)
		t.Run("TestClassifyProviderHTTPFailureAsProvider", TestClassifyProviderHTTPFailureAsProvider)
		t.Run("TestClassifySpawnFailureAsHub", TestClassifySpawnFailureAsHub)
		t.Run("TestFromError_StructuredLLMError_IsProvider", TestFromError_StructuredLLMError_IsProvider)
		t.Run("TestFromError_StructuredLLMError_RenamedInstance_IsProvider", TestFromError_StructuredLLMError_RenamedInstance_IsProvider)
		t.Run("TestFromError_ProviderOnlyNoStatusCode_IsProvider", TestFromError_ProviderOnlyNoStatusCode_IsProvider)
		t.Run("TestFromError_ErrorCodeOnlyNoProviderNoStatus_IsProvider", TestFromError_ErrorCodeOnlyNoProviderNoStatus_IsProvider)
		t.Run("TestClassifyStreamTruncationAsProvider", TestClassifyStreamTruncationAsProvider)
		t.Run("TestFromError_Nil_IsSerfFailure", TestFromError_Nil_IsSerfFailure)
		t.Run("TestFromError_ConfigurationError_IsSerfConfiguration", TestFromError_ConfigurationError_IsSerfConfiguration)
		t.Run("TestFromError_PlainError_FallsThroughToClassify", TestFromError_PlainError_FallsThroughToClassify)
		t.Run("TestFromFields_SourceOverridesClassify", TestFromFields_SourceOverridesClassify)
		t.Run("TestFromFields_UnknownSourceFallsBackToClassify", TestFromFields_UnknownSourceFallsBackToClassify)
		t.Run("TestFromFields_TitleAndHintOverride", TestFromFields_TitleAndHintOverride)
		t.Run("TestFromFields_SourceUI_DefaultTitle", TestFromFields_SourceUI_DefaultTitle)
		t.Run("TestFromFields_HookSourcePreserved", TestFromFields_HookSourcePreserved)
		t.Run("TestFromFields_MCPSource_GetsMCPHints", TestFromFields_MCPSource_GetsMCPHints)
		t.Run("TestFromFields_MCP401_DoesNotMatchProvider", TestFromFields_MCP401_DoesNotMatchProvider)
		t.Run("TestCov_Classify", TestCov_Classify)
		t.Run("TestCov_FromError", TestCov_FromError)
		t.Run("TestCov_NormalizeSource", TestCov_NormalizeSource)
		t.Run("TestCov_DefaultForSource", TestCov_DefaultForSource)
		t.Run("TestCov_FromFields", TestCov_FromFields)
	})
}
