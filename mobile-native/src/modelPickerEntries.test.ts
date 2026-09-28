import type { ModelListResponse } from "@evener/appwire-client";
import { describe, expect, test } from "vitest";
import { modelPickerEntry, modelSections } from "./modelPickerEntries";

// A registry warning (a global-only model under a regional Vertex location)
// has to reach the native session picker's rows: the launch picker, the web
// picker, and the TUI all show it, so a silently warned model here would read
// as a safe choice.
test("a warned model's entry carries the warning for its row", () => {
	const note =
		'regional Vertex location "us-central1" does not serve Gemini 3 or later; use global, us, or eu for gemini-3.8-flash';
	const warned = modelPickerEntry({
		provider: "vertex",
		model: "gemini-3.8-flash",
		displayName: "Gemini 3.8 Flash",
		warnings: [note],
	});

	expect(warned.warnings).toEqual([note]);
	expect(warned.title).toBe("Gemini 3.8 Flash");
	expect(modelPickerEntry({ provider: "openai", model: "gpt-5.5", displayName: "GPT-5.5" }).warnings).toEqual([]);
});

test("a descriptor without a display name titles with its model id", () => {
	expect(modelPickerEntry({ provider: "vertex", model: "gemini-3.8-flash" }).title).toBe("gemini-3.8-flash");
});

describe("a row's context and price (spec 8.5)", () => {
	test.each([
		[{ contextWindow: 200_000, inputCostPerMillion: 3, outputCostPerMillion: 15 }, "200K context · $3 in · $15 out per M"],
		[{ contextWindow: 1_000_000, inputCostPerMillion: 0.25, outputCostPerMillion: 1.5 }, "1M context · $0.25 in · $1.50 out per M"],
		[{ contextWindow: 128_000 }, "128K context"],
		[{ inputCostPerMillion: 2 }, "$2 in per M"],
		[{}, ""],
	])("%o reads %s", (facts, detail) => {
		expect(modelPickerEntry({ provider: "p", model: "m", ...facts }).detail).toBe(detail);
	});
});

describe("the model sheet's sections", () => {
	const catalog: ModelListResponse = {
		data: [
			{ provider: "openai", model: "gpt-5.5", displayName: "GPT-5.5", supportsVision: true },
			{ provider: "anthropic", model: "claude-sonnet-5", displayName: "Claude Sonnet 5", supportsVision: true },
			{ provider: "anthropic", model: "claude-haiku-5", displayName: "Claude Haiku 5", supportsVision: false },
		],
		recent: [
			{ provider: "anthropic", model: "claude-haiku-5" },
			// Recent is global: a model this session can't run is left out.
			{ provider: "meta", model: "muse-spark-1.3" },
		],
	};
	const titles = (sections: ReturnType<typeof modelSections>) =>
		sections.map((section) => [section.title, section.data.map((entry) => entry.title)]);

	test("puts recent models first, then one section per provider as the catalog lists them", () => {
		expect(titles(modelSections(catalog, "", false))).toEqual([
			["Recent", ["Claude Haiku 5"]],
			["openai", ["GPT-5.5"]],
			["anthropic", ["Claude Sonnet 5", "Claude Haiku 5"]],
		]);
	});

	test("searches names, providers and ids", () => {
		expect(titles(modelSections(catalog, "SONNET", false))).toEqual([["anthropic", ["Claude Sonnet 5"]]]);
		expect(titles(modelSections(catalog, "openai", false))).toEqual([["openai", ["GPT-5.5"]]]);
		expect(modelSections(catalog, "nothing like it", false)).toEqual([]);
	});

	test("lists only models that can see images for the vision model", () => {
		expect(titles(modelSections(catalog, "", true))).toEqual([
			["openai", ["GPT-5.5"]],
			["anthropic", ["Claude Sonnet 5"]],
		]);
	});

	test("has no Recent section without history", () => {
		expect(modelSections({ data: catalog.data }, "", false).map((section) => section.title)).toEqual([
			"openai",
			"anthropic",
		]);
	});
});
