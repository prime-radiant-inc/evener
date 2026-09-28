import { describe, expect, it } from "vitest";
import { patchAddedLines } from "./packagePatch.testkit";

// Inline code in agent messages must follow Dynamic Type (spec 16.2), but it
// renders from third-party native source: mobile-native ships the fix as the
// react-native-enriched-markdown patch, and the native gate has no iOS
// toolchain to compile or run that Objective-C. This test guards the patch
// itself — it reads the committed diff and checks the code-font hunks apply the
// render scale instead of a fixed 1.0, which is what lets a `code` span grow
// with the prose around it (issue #2440). It fails before that patch change and
// passes after.
const addedLines = (path: string) => patchAddedLines("react-native-enriched-markdown", "1.0.2", path);

describe("react-native-enriched-markdown inline-code patch", () => {
	it("scales inline code with the render context's font multiplier", () => {
		const codeRenderer = addedLines("ios/renderer/CodeRenderer.m");
		// Both code-font paths read the same multiplier the block text uses.
		expect(codeRenderer).toContain("[context fontSizeMultiplier]");
		expect(codeRenderer).toContain("scaleMultiplier:codeScale");
		expect(codeRenderer).toContain("monospacedSystemFontOfSize:codeFontSize * codeScale");
		// The fixed scale this issue is about must be gone.
		expect(codeRenderer).not.toContain("scaleMultiplier:1.0");
	});

	it("exposes the block-text scale on RenderContext", () => {
		expect(addedLines("ios/renderer/RenderContext.h")).toContain("- (CGFloat)fontSizeMultiplier;");
		const impl = addedLines("ios/renderer/RenderContext.m");
		expect(impl).toContain("RCTFontSizeMultiplierWithMax(_maxFontSizeMultiplier)");
		expect(impl).toContain("_allowFontScaling ?");
		// The block-text cache now shares that one definition.
		expect(impl).toContain("effectiveMultiplier = [self fontSizeMultiplier]");
	});
});
