import { describe, expect, it } from "vitest";
import { type BottomStackInput, bottomStack } from "./bottomStack";

const none: BottomStackInput = {
	approvalPending: false,
	questionPending: false,
	folded: false,
	composerBack: false,
};

describe("the bottom of the Session (spec 8.4, ruling 38)", () => {
	it.each([
		["no dock: the tray, then the composer", {}, { dock: null, tray: true, composer: true, modelChip: true }],
		[
			"an approval: the approval dock, and never the composer",
			{ approvalPending: true },
			{ dock: "approval", tray: false, composer: false, modelChip: false },
		],
		[
			"an approval wins over a question, folded or not",
			{ approvalPending: true, questionPending: true, folded: true, composerBack: true },
			{ dock: "approval", tray: false, composer: false, modelChip: false },
		],
		[
			"a question: the dock is the input",
			{ questionPending: true },
			{ dock: "question", tray: false, composer: false, modelChip: false },
		],
		[
			"a question after Other answer…: the composer, without the model chip",
			{ questionPending: true, composerBack: true },
			{ dock: "question", tray: false, composer: true, modelChip: false },
		],
		[
			"a folded question: the bar, and the whole composer",
			{ questionPending: true, folded: true },
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
