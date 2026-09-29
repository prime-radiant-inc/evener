import { parseSteeringNotifications } from "@evener/appwire-client";
import { notificationWireItem } from "@evener/appwire-client/testing/notificationWireFixtures";
import { describe, expect, it } from "vitest";
import type { TimelineRow } from "../timeline";
import { findMatches, matchLabel, rowText, stepMatch } from "./findInSession";

const rows: TimelineRow[] = [
	{ kind: "time", id: "time:turn_1", turnId: "turn_1", at: 0 },
	{ kind: "user", id: "u", text: "Fix the flaky Settle test" },
	{ kind: "assistant", id: "a", markdown: "The race is in **settle**.", streaming: false },
	{
		kind: "run",
		id: "run:s",
		steps: [
			{
				kind: "activity",
				id: "s",
				label: "shell",
				family: "tool",
				state: "failed",
				detail: { description: "Run the tests", arguments: JSON.stringify({ command: "go test ./agent/..." }) },
			},
		],
	},
	{ kind: "failure", id: "f", title: "Failed", detail: "go test exited 1" },
];

describe("finding words in the loaded transcript (ruling 29)", () => {
	it("matches any row's text, ignoring case", () => {
		expect(findMatches(rows, "settle")).toEqual([1, 2]);
		expect(findMatches(rows, "GO TEST")).toEqual([3, 4]);
		expect(findMatches(rows, "   ")).toEqual([]);
	});

	it("reads a run's steps, targets included, and nothing from a time marker", () => {
		expect(rowText(rows[3] as TimelineRow)).toContain("go test ./agent/...");
		expect(rowText(rows[0] as TimelineRow)).toBe("");
	});

	it("starts at the newest match and steps both ways", () => {
		const matches = [1, 2, 4];
		expect(stepMatch(matches, null, -1)).toBe(4);
		expect(stepMatch(matches, 4, -1)).toBe(2);
		expect(stepMatch(matches, 1, -1)).toBeNull();
		expect(stepMatch(matches, 2, 1)).toBe(4);
		expect(stepMatch(matches, 4, 1)).toBeNull();
		expect(stepMatch([], null, -1)).toBeNull();
	});

	it("says where you are", () => {
		expect(matchLabel([1, 2, 4], 2)).toBe("2 of 3");
		expect(matchLabel([], null)).toBe("No matches");
	});
});

describe("finding words in a delegate or job notification", () => {
	const text = notificationWireItem("delegate-reported").text ?? "";
	const row: TimelineRow = {
		kind: "notice",
		id: "n",
		origin: "steering",
		family: "informational",
		tone: "info",
		text,
		notifications: parseSteeringNotifications(text),
	};

	it("matches the words the card shows, never its markup", () => {
		expect(findMatches([row], "settle finished")).toEqual([0]);
		expect(findMatches([row], "waits for the drain")).toEqual([0]);
		expect(findMatches([row], "delegate_id")).toEqual([]);
	});
});
