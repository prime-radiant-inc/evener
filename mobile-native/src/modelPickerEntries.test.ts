import { expect, test } from "vitest";
import { modelPickerEntries } from "./modelPickerEntries";

// A registry warning (a global-only model under a regional Vertex location)
// has to reach the native session picker's rows: the launch picker, the web
// picker, and the TUI all show it, so a silently warned model here would read
// as a safe choice.
test("a warned model's entry carries the warning for its row", () => {
	const note =
		'regional Vertex location "us-central1" does not serve Gemini 3 or later; use global, us, or eu for gemini-3.8-flash';
	const entries = modelPickerEntries([
		{
			provider: "vertex",
			model: "gemini-3.8-flash",
			displayName: "Gemini 3.8 Flash",
			warnings: [note],
		},
		{ provider: "openai", model: "gpt-5.5", displayName: "GPT-5.5" },
	]);

	expect(entries[0]?.warnings).toEqual([note]);
	expect(entries[0]?.label).toBe("Gemini 3.8 Flash · vertex");
	expect(entries[1]?.warnings).toEqual([]);
});

test("a descriptor without a display name labels with its model id", () => {
	const entries = modelPickerEntries([
		{ provider: "vertex", model: "gemini-3.8-flash" },
	]);

	expect(entries[0]?.label).toBe("gemini-3.8-flash · vertex");
});
