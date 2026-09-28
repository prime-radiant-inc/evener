import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";

// Inline code in agent messages must follow Dynamic Type (spec 16.2), but it
// renders from third-party native source: mobile-native ships the fix as the
// react-native-enriched-markdown patch, and the native gate has no iOS
// toolchain to compile or run that Objective-C. This test guards the patch
// itself — it reads the committed diff and checks the code-font hunks apply the
// render scale instead of a fixed 1.0, which is what lets a `code` span grow
// with the prose around it (issue #2440). It fails before that patch change and
// passes after.
const patchPath = fileURLToPath(
	new URL("../patches/react-native-enriched-markdown+1.0.2.patch", import.meta.url),
);

/** The added (`+`) lines of the diff for one package-relative path. */
function addedLines(path: string): string {
	const lines = readFileSync(patchPath, "utf8").split("\n");
	const start = lines.findIndex((line) =>
		line.startsWith(`diff --git a/node_modules/react-native-enriched-markdown/${path} `),
	);
	if (start === -1) throw new Error(`the patch has no diff for ${path}`);
	const rest = lines.slice(start + 1);
	const end = rest.findIndex((line) => line.startsWith("diff --git "));
	const body = end === -1 ? rest : rest.slice(0, end);
	return body
		.filter((line) => line.startsWith("+") && !line.startsWith("+++"))
		.map((line) => line.slice(1))
		.join("\n");
}

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
