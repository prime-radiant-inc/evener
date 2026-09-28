import { describe, expect, it } from "vitest";
import { type BottomStackInput, bottomStack } from "./bottomStack";

const none: BottomStackInput = {
	approvalPending: false,
	questionPending: false,
	folded: false,
	composerBack: false,
	trayShowing: false,
};

describe("the bottom of the Session (spec 8.4, ruling 38)", () => {
	it.each([
		["a resting session: the composer alone", {}, { dock: null, tray: false, composer: true, modelChip: true }],
		["a working session: the tray, then the composer", { trayShowing: true }, { dock: null, tray: true, composer: true, modelChip: true }],
		[
			"an approval: the approval dock, and never the composer",
			{ approvalPending: true, trayShowing: true },
			{ dock: "approval", tray: false, composer: false, modelChip: false },
		],
		[
			"an approval wins over a question, folded or not",
			{ approvalPending: true, questionPending: true, folded: true, composerBack: true },
			{ dock: "approval", tray: false, composer: false, modelChip: false },
		],
		[
			"a question: the dock is the input",
			{ questionPending: true, trayShowing: true },
			{ dock: "question", tray: false, composer: false, modelChip: false },
		],
		[
			"a question after Other answer…: the composer, without the model chip",
			{ questionPending: true, composerBack: true },
			{ dock: "question", tray: false, composer: true, modelChip: false },
		],
		[
			"a folded question: the bar, and the whole composer",
			{ questionPending: true, folded: true, trayShowing: true },
			{ dock: "foldedQuestion", tray: false, composer: true, modelChip: true },
		],
		[
			"a folded question after Other answer…: still the whole composer",
			{ questionPending: true, folded: true, composerBack: true },
			{ dock: "foldedQuestion", tray: false, composer: true, modelChip: true },
		],
	] as const)("%s", (_name, input, expected) => {
		expect(bottomStack({ ...none, ...input })).toEqual(expected);
	});
});
