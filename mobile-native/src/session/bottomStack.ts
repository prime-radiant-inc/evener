// What the bottom of the Session shows (spec 8.4, rulings 14 and 38), in one
// rule:
// - a pending approval: its dock, and never the composer, since an approval
//   has no redirect;
// - a pending question: its dock, or the dock folded to a bar, and the
//   composer once the dock folds or "Other answer…" brings it back;
// - otherwise the composer.
// The model chip steps aside while the composer answers an open dock.

export interface BottomStackInput {
	approvalPending: boolean;
	questionPending: boolean;
	folded: boolean;
	/** "Other answer…" brought the composer back under an open dock. */
	composerBack: boolean;
}

export interface BottomStack {
	dock: "approval" | "question" | "foldedQuestion" | null;
	composer: boolean;
	modelChip: boolean;
}

export function bottomStack(input: BottomStackInput): BottomStack {
	if (input.approvalPending) return { dock: "approval", composer: false, modelChip: false };
	if (input.questionPending) {
		if (input.folded) return { dock: "foldedQuestion", composer: true, modelChip: true };
		return { dock: "question", composer: input.composerBack, modelChip: false };
	}
	return { dock: null, composer: true, modelChip: true };
}
