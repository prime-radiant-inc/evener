// The question dock's saved answers and its place, kept in the device's
// drafts so a half-answered ask survives leaving the session or the app. A
// new set of questions, or another session, loads its own.
import type { AskQuestionRef } from "@evener/appwire-client";
import { useState } from "react";
import type { DraftDestination } from "../draftRepository";
import { nativeDrafts } from "../nativeDrafts";
import { type QuestionSelections, questionsIdentity, seedQuestionAnswers } from "../questionAnswers";

interface Saved {
	selections: QuestionSelections;
	activeIndex: number;
	loaded: boolean;
	error: string | null;
}

export interface QuestionDraft {
	selections: QuestionSelections;
	loaded: boolean;
	error: string | null;
	activeIndex: number;
	setSelections(update: (values: QuestionSelections) => QuestionSelections): void;
	setActiveIndex(index: number): void;
}

function load(destination: DraftDestination, questions: AskQuestionRef[], signature: string): Saved {
	// No question, nothing to read.
	if (!questions.length) return { selections: {}, activeIndex: 0, loaded: true, error: null };
	try {
		const activeKey = nativeDrafts().readQuestionPosition(
			destination,
			questions.map((item) => item.key),
		);
		return {
			selections: seedQuestionAnswers(questions, nativeDrafts().readQuestions(destination, signature)),
			activeIndex: Math.max(
				0,
				questions.findIndex((question) => question.key === activeKey),
			),
			loaded: true,
			error: null,
		};
	} catch {
		return {
			activeIndex: 0,
			selections: {},
			loaded: false,
			error: "Saved answers could not be loaded.",
		};
	}
}

export function useQuestionDraft(destination: DraftDestination, questions: AskQuestionRef[]): QuestionDraft {
	// Bounded: the signature never carries a question's full, unbounded prose
	// (questionsIdentity's own comment).
	const signature = questionsIdentity(questions);
	const identity = `${destination.hubId}\u0000${destination.sessionRef}\u0000${signature}`;
	const [loadedFor, setLoadedFor] = useState(identity);
	const [saved, setSaved] = useState(() => load(destination, questions, signature));
	const [positionError, setPositionError] = useState<string | null>(null);
	// Other questions, or another session: its own saved answers, read while
	// rendering so the dock never shows one ask's answers on another.
	let current = saved;
	if (loadedFor !== identity) {
		current = load(destination, questions, signature);
		setLoadedFor(identity);
		setSaved(current);
		setPositionError(null);
	}
	function setActiveIndex(index: number) {
		const question = questions[index];
		if (!question || !current.loaded) return;
		setSaved((values) => ({ ...values, activeIndex: index }));
		try {
			nativeDrafts().writeQuestionPosition(destination, question.key);
			setPositionError(null);
		} catch {
			setPositionError("Your place could not be saved. Your answers are kept separately.");
		}
	}
	function setSelections(update: (values: QuestionSelections) => QuestionSelections) {
		if (!current.loaded) return;
		const selections = update(current.selections);
		try {
			nativeDrafts().writeQuestions(destination, signature, selections);
			setSaved((values) => ({ ...values, selections, loaded: true, error: null }));
		} catch {
			setSaved((values) => ({
				...values,
				selections,
				loaded: true,
				error: "These answers could not be saved. They're kept while this session is open.",
			}));
		}
	}
	return {
		selections: current.selections,
		loaded: current.loaded,
		error: current.error ?? positionError,
		activeIndex: current.activeIndex,
		setSelections,
		setActiveIndex,
	};
}
