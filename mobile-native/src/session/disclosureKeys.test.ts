import { scopedDisclosureId } from "@evener/appwire-client";
import { describe, expect, it } from "vitest";
import type { RunStep, TimelineRow } from "../timeline";
import { rowDisclosureIds } from "./disclosureKeys";

// A step's disclosure id must not move as history records the live step: a row
// the reader expanded would collapse on the recording change. The row id and
// the turn id both change there (the live execution id vs the file's logical
// turn; internal/apptranscript/logical_turn.go's invariant), so the id keys on
// the call id (timeline.ts's stepKey), shared with the list's row key.
const step = (id: string, turnId: string, over: Partial<RunStep> = {}): RunStep => ({
	kind: "activity",
	id,
	label: "shell",
	family: "tool",
	state: "completed",
	detail: { callId: "call-7" },
	turnId,
	...over,
});

describe("a step's disclosure id through the recording transition", () => {
	it("is identical for the live row and its recorded row, whose turns differ", () => {
		const live = step("tool:call:call-7", "t_abc");
		const recorded = step("item_tool_3_1", "turn_1", {
			state: "completed",
			transcriptKey: "apptranscript-item-v2:turn_1:3:1",
		});
		expect(rowDisclosureIds("hub", "ref", recorded)).toEqual(rowDisclosureIds("hub", "ref", live));
	});

	it("is identical for a run through the same transition", () => {
		const live: TimelineRow = { kind: "run", id: "run:tool:call:call-7", steps: [step("tool:call:call-7", "t_abc")] };
		const recorded: TimelineRow = {
			kind: "run",
			id: "run:item_tool_3_1",
			steps: [step("item_tool_3_1", "turn_1", { transcriptKey: "apptranscript-item-v2:turn_1:3:1" })],
			transcriptKey: "apptranscript-item-v2:turn_1:3:1",
		};
		expect(rowDisclosureIds("hub", "ref", recorded)).toEqual(rowDisclosureIds("hub", "ref", live));
	});

	it("still keeps a call-less step keyed by its row id", () => {
		const [id] = rowDisclosureIds("hub", "ref", step("wire-row", "turn_1", { detail: {} }));
		expect(id).toBe(scopedDisclosureId(JSON.stringify(["hub", "ref"]), JSON.stringify(["activity", "", "wire-row"])));
	});
});
