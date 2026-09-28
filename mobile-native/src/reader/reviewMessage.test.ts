import { describe, expect, it } from "vitest";
import { quoteLine, reviewMessage } from "./reviewMessage";

describe("the review message (spec 10.2)", () => {
	it("is the spec's format: the verdict, each comment under its quote, then the overall note", () => {
		expect(
			reviewMessage(
				"docs/superpowers/plans/2026-09-25-settle-race.md",
				"requestChanges",
				[
					{
						quote:
							"The retirement drain and the tree settle pass both take the tree lock. When settle runs first, it can mark the tree idle before the drain has seen pending work, so the root's attention is never delivered.",
						text: "Split this into two steps; the drain should never wait on settle.",
					},
				],
				"close. Fix the ordering and go.",
			),
		).toBe(
			[
				"Review of docs/superpowers/plans/2026-09-25-settle-race.md: request changes.",
				"> The retirement drain and the tree settle pass both take the tree lock. When settle runs first, it can mark the tree idle before the drain has seen pending…\nSplit this into two steps; the drain should never wait on settle.",
				"Overall: close. Fix the ordering and go.",
			].join("\n\n"),
		);
	});

	it("says approved or comments only, and leaves out an empty note", () => {
		expect(reviewMessage("README.md", "approve", [], "  ")).toBe("Review of README.md: approved.");
		expect(reviewMessage("README.md", "commentOnly", [{ quote: "Intro.", text: "Typo in line 2." }], "")).toBe(
			"Review of README.md: comments only.\n\n> Intro.\nTypo in line 2.",
		);
	});

	it("sends a comment with the words it was left on, even after its paragraph changed (Review Focus 3)", () => {
		expect(reviewMessage("plan.md", "requestChanges", [{ quote: "The old wording.", text: "Say why." }], "")).toBe(
			"Review of plan.md: request changes.\n\n> The old wording.\nSay why.",
		);
	});

	it("keeps a comment's own lines and quotes one line of at most 160 characters", () => {
		expect(reviewMessage("a.md", "commentOnly", [{ quote: "  Split\n this   into two.  ", text: "One.\nTwo.\n" }], "")).toBe(
			"Review of a.md: comments only.\n\n> Split this into two.\nOne.\nTwo.",
		);
		expect(quoteLine("x".repeat(200))).toBe(`${"x".repeat(159)}…`);
		expect(quoteLine("x".repeat(160))).toBe("x".repeat(160));
	});
});
