// The question dock (spec 8.4): an amber-edged card in the tray's place while
// the agent waits on a question. It shows one question at a time with its
// options, "Other answer…" to answer in your own words through the composer,
// and one primary button that moves to the next question or sends every
// answer as one message. Folded, it is one bar that says what waits.
//
// `questions` stays canonical: only a question's bounded copy (boundQuestion
// over boundQuestionText) is put on screen, and every choice reads the
// canonical ref.
import type { AskQuestionRef, AskResolution } from "@evener/appwire-client";
import { SymbolView } from "expo-symbols";
import { useEffect, useRef } from "react";
import { AccessibilityInfo, Keyboard, Pressable, Text, View } from "react-native";
import { boundQuestion } from "../projectedRows";
import {
	boundQuestionText,
	composeQuestionAnswers,
	nextUnansweredQuestion,
	type QuestionSelections,
	questionAdvanceTarget,
} from "../questionAnswers";
import { allowFontScaling, useColors, useTextScale } from "../ui";
import { useKeyboardShown } from "../useKeyboardShown";
import { useReadingFace } from "../display/displayContext";
import { foldedLabel, orderedOptions, primaryLabel, questionHeader } from "./askDockCopy";
import { DockBody } from "./DockBody";
import { dockCard } from "./dockCard";
import { SymbolButton } from "./SymbolButton";
import type { QuestionDraft } from "./useQuestionDraft";

export interface QuestionDockProps {
	questions: AskQuestionRef[];
	draft: QuestionDraft;
	/** The session can take an answer now. */
	ready: boolean;
	/** The answers are on their way. */
	sending: boolean;
	/** Offline: sending keeps the answers on the phone until the connection
	 * returns, and the send button says so. */
	waitsForConnection?: boolean;
	folded: boolean;
	onFold(folded: boolean): void;
	/** Bring the composer back, focused, to answer in your own words. */
	onOtherAnswer(): void;
	onSend(selections: QuestionSelections): void;
	/** Why the last send didn't go, as one line. */
	error?: string | null;
	/** The composer is back beneath the dock ("Other answer…"). While the
	 * keyboard is up for it, the dock shows only its header and the question:
	 * the room is short, and the composer's Send is the answer. */
	composerUp?: boolean;
}

export function QuestionDock({
	questions,
	draft,
	ready,
	sending,
	waitsForConnection = false,
	folded,
	onFold,
	onOtherAnswer,
	onSend,
	error = null,
	composerUp = false,
}: QuestionDockProps) {
	const typing = useKeyboardShown() && composerUp;
	useOptionsReturnAnnounced(typing, folded);
	const { palette } = useColors();
	const scale = useTextScale();
	const face = useReadingFace();
	const { selections, activeIndex } = draft;
	const card = dockCard(palette);
	const caption = { fontSize: 13 * scale, lineHeight: 18 * scale, color: palette.inkMid };
	if (folded) {
		const unanswered = questions.filter((question) => !selections[question.key]?.resolution).length;
		// A seeded recommendation isn't an answer you sent: while every question
		// has one, the bar still counts them all.
		const label = foldedLabel(unanswered || questions.length);
		return (
			<Pressable
				accessibilityRole="button"
				accessibilityLabel={label}
				// The label names the bar; VoiceOver reads why an answer didn't go after it.
				accessibilityHint={error ?? undefined}
				accessibilityState={{ expanded: false, disabled: false }}
				onPress={() => {
					// Answers that couldn't be read get another try as the dock opens.
					draft.reload();
					onFold(false);
				}}
				style={({ pressed }) => ({
					...card,
					minHeight: 44,
					paddingHorizontal: 16,
					flexDirection: "row",
					alignItems: "center",
					gap: 8,
					opacity: pressed ? 0.6 : 1,
				})}
			>
				<View style={{ flex: 1, paddingVertical: 4 }}>
					<Text
						allowFontScaling={allowFontScaling}
						style={{ fontSize: 15 * scale, lineHeight: 20 * scale, color: palette.inkHi }}
					>
						{label}
					</Text>
					{error ? (
						<Text
							allowFontScaling={allowFontScaling}
							numberOfLines={1}
							style={{ ...caption, color: palette.dangerInk }}
						>
							{error}
						</Text>
					) : null}
				</View>
				<SymbolView name="chevron.up" tintColor={palette.inkMid} size={15 * scale} />
			</Pressable>
		);
	}
	const question = questions[activeIndex] ?? questions[0];
	if (!question) return null;
	const display = boundQuestion(question, boundQuestionText);
	const answer = selections[question.key];
	const editable = ready && draft.loaded && !sending;
	const advanceTarget = questionAdvanceTarget(questions, selections, activeIndex);
	const composes = composeQuestionAnswers(questions, selections) !== null;
	const primaryOff = !editable || (advanceTarget === undefined && !composes);
	const primary = primaryLabel(advanceTarget, questions.length);
	const primaryAccessibility =
		waitsForConnection && advanceTarget === undefined ? `${primary} when you're back online` : primary;
	const filled = advanceTarget === undefined;
	function select(resolution: AskResolution | null) {
		const next = { ...selections, [question.key]: { note: answer?.note ?? "", resolution } };
		draft.setSelections(() => next);
		if (!answer?.resolution && resolution?.kind === "option" && !question.multiSelect) {
			const target = nextUnansweredQuestion(questions, next, activeIndex);
			if (target !== undefined) draft.setActiveIndex(target);
		}
	}
	// The options paired with their bounded copy and where the agent offered
	// them, recommended first.
	const options = orderedOptions(
		question.options.map((option, index) => ({
			position: index,
			label: option.label,
			recommended: option.recommended,
			shown: display.options[index],
		})),
	);
	return (
		// The question and its options scroll when the screen has less room than
		// they need; the header and the answer controls stay on screen, so every
		// question can be answered.
		<View testID="question-dock" style={{ ...card, paddingTop: 4, paddingBottom: 8 }}>
			<View
				style={{ flexDirection: "row", alignItems: "center", paddingLeft: activeIndex > 0 ? 4 : 16, paddingRight: 4 }}
			>
				{activeIndex > 0 ? (
					<SymbolButton label="Previous question" onPress={() => draft.setActiveIndex(activeIndex - 1)}>
						<SymbolView name="chevron.left" tintColor={palette.accentInk} size={15 * scale} />
					</SymbolButton>
				) : null}
				<Text allowFontScaling={allowFontScaling} style={{ ...caption, flex: 1, fontVariant: ["tabular-nums"] }}>
					{questionHeader(activeIndex, questions.length)}
				</Text>
				{typing ? (
					<Pressable
						accessibilityRole="button"
						accessibilityLabel="Show options"
						accessibilityHint="Hides the keyboard"
						onPress={Keyboard.dismiss}
						style={({ pressed }) => ({
							minHeight: 44,
							paddingHorizontal: 8,
							justifyContent: "center",
							opacity: pressed ? 0.6 : 1,
						})}
					>
						<Text
							allowFontScaling={allowFontScaling}
							style={{ fontSize: 15 * scale, lineHeight: 20 * scale, color: palette.accentInk }}
						>
							Show options
						</Text>
					</Pressable>
				) : null}
				<SymbolButton label="Fold" onPress={() => onFold(true)}>
					<SymbolView name="chevron.down" tintColor={palette.inkMid} size={15 * scale} />
				</SymbolButton>
			</View>
			<DockBody
				// Keyed by the question, so each question opens at its top.
				key={question.key}
			>
				<View style={{ paddingHorizontal: 16, gap: 4 }}>
					<Text
						allowFontScaling={allowFontScaling}
						style={{
							...face.semibold,
							fontSize: 17 * scale,
							lineHeight: 24 * scale,
							color: palette.prose,
						}}
					>
						{display.question}
					</Text>
					{display.why ? (
						<Text
							allowFontScaling={allowFontScaling}
							style={{ ...face.regular, fontSize: 15 * scale, lineHeight: 21 * scale, color: palette.inkMid }}
						>
							{display.why}
						</Text>
					) : null}
				</View>
				{typing ? null : (
					<QuestionOptions question={question} options={options} answer={answer} editable={editable} select={select} />
				)}
			</DockBody>
			{error || draft.error ? (
				<Text
					allowFontScaling={allowFontScaling}
					numberOfLines={1}
					style={{ ...caption, color: palette.dangerInk, paddingHorizontal: 16 }}
				>
					{error ?? draft.error}
				</Text>
			) : null}
			{typing ? null : (
				<AnswerBar
					editable={editable}
					onOtherAnswer={onOtherAnswer}
					primary={primary}
					primaryAccessibility={primaryAccessibility}
					primaryOff={primaryOff}
					filled={filled}
					onPrimary={() => {
						if (advanceTarget !== undefined) draft.setActiveIndex(advanceTarget);
						else onSend(selections);
					}}
				/>
			)}
		</View>
	);
}

/** When the options come back on the open dock after you typed with the
 * keyboard up, VoiceOver says so: they return below where you were. A folded
 * dock shows no options, so the keyboard going down there says nothing. */
function useOptionsReturnAnnounced(typing: boolean, folded: boolean) {
	const wasHiddenByTyping = useRef(typing && !folded);
	useEffect(() => {
		if (wasHiddenByTyping.current && !typing && !folded) AccessibilityInfo.announceForAccessibility("Options shown");
		wasHiddenByTyping.current = typing && !folded;
	}, [typing, folded]);
}

/** An option as the dock shows it: where the agent offered it, and its
 * bounded copy. */
interface ShownOption {
	position: number;
	label: string;
	recommended?: boolean;
	shown: ReturnType<typeof boundQuestion>["options"][number] | undefined;
}

/** One question's options, recommended first: radio rows for a single
 * answer, checkboxes for several. */
function QuestionOptions({
	question,
	options,
	answer,
	editable,
	select,
}: {
	question: AskQuestionRef;
	options: ShownOption[];
	answer: QuestionSelections[string] | undefined;
	editable: boolean;
	select(resolution: AskResolution | null): void;
}) {
	const { palette } = useColors();
	const scale = useTextScale();
	return (
		<View style={{ marginTop: 8 }}>
			{options.map((option, index) => {
				const checked = answer?.resolution?.kind === "option" && answer.resolution.labels.includes(option.label);
				const mark = question.multiSelect
					? checked
						? "checkmark.square.fill"
						: "square"
					: checked
						? "largecircle.fill.circle"
						: "circle";
				return (
					<Pressable
						// Keyed by where the agent offered the option: labels are the
						// agent's and can repeat.
						key={`${question.key}:${option.position}`}
						accessibilityRole={question.multiSelect ? "checkbox" : "radio"}
						// The caption and the detail sit inside the row, which VoiceOver
						// reads by its label alone, so both go into what it reads.
						accessibilityLabel={
							option.recommended
								? `${option.shown?.label ?? option.label}, Recommended`
								: (option.shown?.label ?? option.label)
						}
						accessibilityHint={option.shown?.detail || undefined}
						accessibilityState={{ checked, disabled: !editable }}
						disabled={!editable}
						onPress={() => {
							const labels =
								question.multiSelect && answer?.resolution?.kind === "option" ? answer.resolution.labels : [];
							const next =
								question.multiSelect && checked
									? labels.filter((label) => label !== option.label)
									: question.multiSelect
										? [...labels, option.label]
										: [option.label];
							select(next.length ? { kind: "option", labels: next } : null);
						}}
						style={({ pressed }) => ({
							minHeight: 44,
							flexDirection: "row",
							alignItems: "flex-start",
							gap: 12,
							paddingLeft: 16,
							opacity: !editable ? 0.4 : pressed ? 0.6 : 1,
						})}
					>
						<View style={{ paddingTop: 12 }}>
							<SymbolView name={mark} tintColor={checked ? palette.accentInk : palette.inkMid} size={20 * scale} />
						</View>
						<View
							style={{
								flex: 1,
								paddingVertical: 10,
								paddingRight: 16,
								// Hairlines inset to the label divide the rows.
								borderTopWidth: index === 0 ? 0 : 0.5,
								borderTopColor: palette.edge,
							}}
						>
							<Text
								allowFontScaling={allowFontScaling}
								style={{ fontSize: 17 * scale, lineHeight: 22 * scale, color: palette.inkHi }}
							>
								{option.shown?.label ?? option.label}
								{option.recommended ? (
									<Text style={{ fontSize: 13 * scale, color: palette.inkMid }}> · Recommended</Text>
								) : null}
							</Text>
							{option.shown?.detail ? (
								<Text
									allowFontScaling={allowFontScaling}
									style={{ fontSize: 15 * scale, lineHeight: 20 * scale, color: palette.inkMid }}
								>
									{option.shown.detail}
								</Text>
							) : null}
						</View>
					</Pressable>
				);
			})}
		</View>
	);
}

/** The dock's answer controls: "Other answer…", and the primary button that
 * moves to the next question or sends every answer. */
function AnswerBar({
	editable,
	onOtherAnswer,
	primary,
	primaryAccessibility,
	primaryOff,
	filled,
	onPrimary,
}: {
	editable: boolean;
	onOtherAnswer(): void;
	primary: string;
	primaryAccessibility: string;
	primaryOff: boolean;
	filled: boolean;
	onPrimary(): void;
}) {
	const { palette } = useColors();
	const scale = useTextScale();
	return (
		<View
			style={{
				flexDirection: "row",
				alignItems: "center",
				justifyContent: "space-between",
				paddingHorizontal: 8,
				marginTop: 4,
			}}
		>
			<Pressable
				accessibilityRole="button"
				accessibilityLabel="Other answer…"
				accessibilityState={{ disabled: !editable }}
				disabled={!editable}
				onPress={onOtherAnswer}
				style={({ pressed }) => ({
					minHeight: 44,
					paddingHorizontal: 8,
					justifyContent: "center",
					opacity: !editable ? 0.4 : pressed ? 0.6 : 1,
				})}
			>
				<Text
					allowFontScaling={allowFontScaling}
					style={{ fontSize: 17 * scale, lineHeight: 22 * scale, color: palette.accentInk }}
				>
					Other answer…
				</Text>
			</Pressable>
			<Pressable
				accessibilityRole="button"
				accessibilityLabel={primaryAccessibility}
				accessibilityState={{ disabled: primaryOff }}
				disabled={primaryOff}
				onPress={() => {
					if (!primaryOff) onPrimary();
				}}
				style={({ pressed }) => ({
					minHeight: 44,
					paddingHorizontal: 16,
					justifyContent: "center",
					borderRadius: 22,
					backgroundColor: filled ? palette.accentFill : "transparent",
					opacity: primaryOff ? 0.4 : pressed ? 0.6 : 1,
				})}
			>
				<Text
					allowFontScaling={allowFontScaling}
					style={{
						fontSize: 17 * scale,
						lineHeight: 22 * scale,
						fontWeight: "600",
						color: filled ? palette.onFill : palette.accentInk,
					}}
				>
					{primary}
				</Text>
			</Pressable>
		</View>
	);
}
