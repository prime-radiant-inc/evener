import { describe, expect, it } from "vitest";
import { transcriptTreeTarget } from "./useTranscriptSubagentTree";

// Which coordinator tree a transcript's finished subagent rows read (#3326).
describe("the tree a transcript holds", () => {
	const OWN = { ref: "local:coord", threadId: "thread-coord" };

	it("is none while no subagent row has finished", () => {
		expect(
			transcriptTreeTarget({ showsFinished: false, coordinator: OWN, onSubagentScreen: false, panelThread: null }),
		).toBeNull();
	});

	it("is the session's own on a coordinator's screen", () => {
		expect(
			transcriptTreeTarget({ showsFinished: true, coordinator: OWN, onSubagentScreen: false, panelThread: null }),
		).toEqual(OWN);
	});

	// A coordinator that restarted runs under a new thread, and a tree asked for
	// under the route's old one is refused: a subagent's screen waits for its
	// panel to read the thread, and then holds the tree under it.
	it("waits on a subagent's screen for the coordinator's thread as it reads now", () => {
		expect(
			transcriptTreeTarget({ showsFinished: true, coordinator: OWN, onSubagentScreen: true, panelThread: null }),
		).toBeNull();
		expect(
			transcriptTreeTarget({
				showsFinished: true,
				coordinator: OWN,
				onSubagentScreen: true,
				panelThread: "thread-restarted",
			}),
		).toEqual({ ref: "local:coord", threadId: "thread-restarted" });
	});
});
