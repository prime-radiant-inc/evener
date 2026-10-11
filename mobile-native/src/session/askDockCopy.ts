// What the ask dock says, and what it does with your text (spec 8.4). For a
// question: the header, the order options appear in, and what "Other answer…"
// or a Send while the dock is folded does with your text (ruling 14). For an
// approval: its sentence, its target, its scope, and its first button, on
// S12's fallback (ruling 12).
import type { AskQuestionRef, SandboxEscalationRequested } from "@evener/appwire-client";
import { composeQuestionAnswers, nextUnansweredQuestion, type QuestionSelections } from "../questionAnswers";

export function questionHeader(index: number, total: number): string {
	return total > 1 ? `Question ${index + 1} of ${total}` : "Question";
}

export function primaryLabel(advanceTarget: number | undefined, total: number): string {
	if (advanceTarget !== undefined) return "Next question";
	return total > 1 ? "Send answers" : "Send answer";
}

export function foldedLabel(unanswered: number): string {
	return unanswered === 1 ? "Answer the question" : `Answer ${unanswered} questions`;
}

/** The recommended option first; the rest keep the agent's order (a stable sort). */
export function orderedOptions<T extends { recommended?: boolean }>(options: readonly T[]): T[] {
	return [...options].sort((a, b) => Number(!!b.recommended) - Number(!!a.recommended));
}

export interface TextAnswer {
	selections: QuestionSelections;
	/** The ask's whole reply, once every question has an answer. */
	message: string | null;
	/** Where the dock returns while questions are still unanswered. */
	nextIndex: number | undefined;
}

/** Your text as the free answer to the question the dock is on. When that
 * completes the ask, the reply is ready to send as one message; otherwise the
 * dock returns at the next unanswered question. */
export function answerWithText(
	questions: AskQuestionRef[],
	selections: QuestionSelections,
	activeIndex: number,
	text: string,
): TextAnswer {
	const question = questions[activeIndex];
	const answer = text.trim();
	if (!question || !answer) return { selections, message: null, nextIndex: activeIndex };
	const next: QuestionSelections = {
		...selections,
		[question.key]: { note: selections[question.key]?.note ?? "", resolution: { kind: "free", text: answer } },
	};
	const nextIndex = nextUnansweredQuestion(questions, next, activeIndex);
	return {
		selections: next,
		message: nextIndex === undefined ? composeQuestionAnswers(questions, next) : null,
		nextIndex,
	};
}

export interface ApprovalCard {
	/** What it wants, in the hub's words, not the agent's. */
	wants: string;
	tool: string;
	/** The literal path, or "" when the hub couldn't name one. */
	target: string;
	scope: string;
	partiallyRan: boolean;
	primary: { label: string; detail: string };
}

// The hub's floor when it can't name the denied path
// (agent/session_escalation.go).
const UNNAMED_PATH = "<denied>";

export function approvalCard(request: SandboxEscalationRequested): ApprovalCard {
	// Only read_file, write_file and edit_file escalate
	// (escalatableTools, agent/session_escalation.go).
	const reads = request.tool === "read_file";
	const named = request.deniedPath !== "" && request.deniedPath !== UNNAMED_PATH;
	return {
		wants: reads
			? "Wants to read outside the workspace"
			: request.mode === "read-only"
				? "Wants to write a file"
				: "Wants to write outside the workspace",
		tool: request.tool,
		target: named ? request.deniedPath : "",
		scope: scopeSentence(request.mode, reads),
		partiallyRan: request.partiallyRan === true,
		primary: named
			? { label: "Allow this file only", detail: "It will ask again for the next one" }
			: { label: "Allow once", detail: "Just this action" },
	};
}

function scopeSentence(mode: string, reads: boolean): string {
	if (mode === "read-only" && !reads) return "This session is read-only.";
	if (mode === "workspace-write" || mode === "restricted")
		return reads
			? "This session can only read inside its project folder."
			: "This session can only write inside its project folder.";
	return "The sandbox blocked this action.";
}
