import {
	type ActivityTree,
	type EvenerDelegateInfo,
	isExitOnly,
	itemIdentityMatches,
	MEMORY_CONTEXT_EVENT_KIND,
	type TurnModel,
} from "@evener/appwire-client";
import { type ReactNode, useEffect, useMemo, useState } from "react";
import { Pressable, ScrollView, Text, View } from "react-native";
import { Gesture, GestureDetector } from "react-native-gesture-handler";
import { SafeAreaView } from "react-native-safe-area-context";
import { HoldingModal } from "./alerts/HoldingModal";
import { copyText } from "./clipboard";
import { type MenuItem, menuAccessibility, menuPreview, showMenu } from "./longPressMenu";
import { MarkdownResponse } from "./MarkdownResponse";
import { setDisclosureOpenAll, useDisclosureOpenAmong } from "./nativeDisclosure";
import type { MobileTimelineItem } from "./projectedRows";
import type { NativeFileOpenContext } from "./reader/markdownFileReferences";
import { useMinuteClock } from "./session/minuteClock";
import { useNowPastQuiet } from "./session/quietClock";
import { memoryContextSourceDisclosureId, rowDisclosureIds } from "./session/disclosureKeys";
import type { ErrorAction } from "./session/errorAction";
import { ErrorRow } from "./session/ErrorRow";
import { ExitMark } from "./session/ExitMark";
import { MemoryContextBody } from "./session/MemoryContextBody";
import { NotificationCards } from "./session/NotificationCards";
import { ApprovalHistory } from "./session/ApprovalHistory";
import { QuestionHistory } from "./session/QuestionHistory";
import { RunRow } from "./session/RunRow";
import { StepEvidence } from "./session/StepEvidence";
import { SubagentRow } from "./session/SubagentRow";
import { SystemEvent } from "./session/SystemEvent";
import { subagentLine } from "./session/subagentLine";
import { ThoughtRow } from "./session/ThoughtRow";
import { askRowQuestions, stepWords, timeMarkerText } from "./session/transcriptRows";
import { useStepEvidence } from "./session/useStepEvidence";
import { subagentOutcome } from "./subagents/subagentModel";
import { TranscriptImages } from "./TranscriptImages";
import { isCriticalNotice, noticeLabel, type TimelineRow } from "./timeline";
import type { ActivityPresentation } from "./transcriptPresentation";
import { allowFontScaling, Copy, styles, useColors, useTextScale } from "./ui";
import { Button } from "./sheet/Grouped";
import { FoldChevron } from "./board/BoardRow";

// A row that shows a call's small exit mark beside its line: the mark and the
// line share one row, centered. Module-level so it is not re-allocated on every
// render (the same reason RunRow keeps its own `lineStyle` in one place).
const exitRowStyle = { flexDirection: "row", alignItems: "center", gap: 6 } as const;
// The line beside the mark wraps in the space left for it. React Native
// defaults a row child's flexShrink to 0, so without this a long command (or a
// large text scale) overflows the viewport and pushes the mark offscreen. Same
// constraint RunRow puts on its own step text.
const exitTextStyle = { flex: 1, minWidth: 0 } as const;

// The evidence source for a row that is not a tool step: hooks are called for
// every row, so a non-step needs a stable source with nothing to show.
const NO_STEP = { label: "", summaryOnly: true, detail: {} } as const;

export function TimelineItem({
	item,
	hubId,
	sessionRef,
	sourceTurns,
	activityPresentation,
	expandByDefault = false,
	showDuration = true,
	fork,
	forkDisabled = false,
	quote,
	live = false,
	liveRunsOpen = false,
	delegates,
	subagentTree,
	openSubagent,
	answerFor,
	errorActionFor,
	onErrorAction,
	documentChips,
	fileContext,
}: {
	item: TimelineRow;
	hubId: string;
	sessionRef: string;
	/** Retained canonical turns, read only when a shared-notes snapshot opens. */
	sourceTurns?: readonly TurnModel[];
	activityPresentation?: ActivityPresentation;
	expandByDefault?: boolean;
	showDuration?: boolean;
	fork?: (entryIndex: number, preview: string) => void;
	forkDisabled?: boolean;
	/** Quotes text into the composer's draft. */
	quote?: (text: string) => void;
	/** This row is the live run: the last run of the turn in progress. */
	live?: boolean;
	/** The live run shows its steps (NativeTranscriptPresentation.liveRunsOpen). */
	liveRunsOpen?: boolean;
	/** The session's subagents, for a subagent row's state and activity. */
	delegates?: readonly EvenerDelegateInfo[];
	/** The coordinator's subagent tree, which a finished subagent's row
	 * reads its outcome from; the screen holds it (useTranscriptSubagentTree). */
	subagentTree?: ActivityTree | null;
	/** Opens a subagent's own transcript. */
	openSubagent?: (ref: string, title: string, runStartedAt?: string) => void;
	/** Your answer to the question an ask_user row asked, when you gave one. */
	answerFor?: (itemId: string) => string | undefined;
	/** The one action an error row offers (errorAction), and running it. */
	errorActionFor?: (row: { id: string; title: string; detail: string; turnId?: string }) => ErrorAction | null;
	onErrorAction?: (action: ErrorAction) => void;
	/** The document chips under an agent's message (spec 8.2): the screen
	 * decides which documents a message names and where a chip opens. */
	documentChips?: (message: { id: string; markdown: string; streaming: boolean }) => ReactNode;
	fileContext?: NativeFileOpenContext;
}) {
	const disclosureIds = rowDisclosureIds(hubId, sessionRef, item);
	const defaultOpen = (item.kind === "activity" || item.kind === "run") && expandByDefault;
	const expanded = useDisclosureOpenAmong(disclosureIds, defaultOpen);
	const toggle = () => setDisclosureOpenAll(disclosureIds, !expanded);
	// Nothing collapses on its own: a run held open while it is live stays
	// open once it finishes, until the reader folds it.
	const heldOpen = item.kind === "run" && live && liveRunsOpen;
	// Pinned even where the level already opens every run, or a switch to a
	// level that doesn't would fold it.
	const disclosureKey = disclosureIds.join("\n");
	// biome-ignore lint/correctness/useExhaustiveDependencies: disclosureKey stands for disclosureIds, a new array each render.
	useEffect(() => {
		if (heldOpen) setDisclosureOpenAll(disclosureIds, true);
	}, [heldOpen, disclosureKey]);
	const colors = useColors();
	// A tool step's evidence, for the rows that show one: the same renderer a
	// run uses, so an expanded failed call keeps its 40-line preview, its ANSI
	// handling and its full-log viewer here too.
	// Every activity but a thought shows this evidence: a tool step, and an
	// unknown forward-compatible item, whose body the projection put in its
	// detail. A thought has its own row.
	const step = item.kind === "activity" && item.family !== "reasoning" ? item : NO_STEP;
	const evidence = useStepEvidence(step);
	// A thought the projector didn't show reads as one quiet line, with no
	// error rule, unless the thought itself failed.
	const quietThought = item.kind === "failure" && item.thought === true && item.title !== "Thought failed";
	const textScale = useTextScale();
	const label = item.kind === "notice" ? noticeLabel(item) : undefined;
	const noticeText = useMemo(() => {
		if (item.kind !== "notice") return undefined;
		// List rows stay bounded. An opened disclosure whose recorded text can
		// exceed that bound - a shared-notes snapshot or an automatic memory
		// refresh - reads the full original from the canonical model the store
		// already holds: the row's own text is a truncated display copy, so the
		// literal Source (and a malformed refresh's fallback) stays complete.
		const readsRetainedOriginal =
			item.origin === "system" && (item.eventKind === "notes-context" || item.eventKind === MEMORY_CONTEXT_EVENT_KIND);
		if (!expanded || !readsRetainedOriginal || !sourceTurns) return item.text;
		return (
			sourceTurns.find((turn) => turn.id === item.turnId)?.items.find((source) => itemIdentityMatches(source, item))
				?.text ?? item.text
		);
	}, [expanded, item, sourceTurns]);
	let content: ReactNode;
	switch (item.kind) {
		case "details":
			content = (
				<SystemEvent label={`Session details · ${item.entries.length}`} expanded={expanded} onToggle={toggle}>
					{item.entries.map((entry) => (
						<TimelineItem
							key={entry.id}
							item={entry}
							hubId={hubId}
							sessionRef={sessionRef}
							sourceTurns={sourceTurns}
							errorActionFor={errorActionFor}
							onErrorAction={onErrorAction}
						/>
					))}
				</SystemEvent>
			);
			break;
		case "user":
			content = (
				<YourMessage
					item={item}
					fork={
						fork &&
						!forkDisabled &&
						item.transcriptEntryIndex !== undefined &&
						Number.isSafeInteger(item.transcriptEntryIndex) &&
						item.transcriptEntryIndex > 0
							? fork
							: undefined
					}
					quote={quote}
				/>
			);
			break;
		case "note":
			content = <NoteRow text={item.text} />;
			break;
		case "assistant":
			content = (
				<>
					<AgentMessage markdown={item.markdown} quote={quote} fileContext={fileContext} />
					{documentChips?.({ id: item.id, markdown: item.markdown, streaming: item.streaming })}
				</>
			);
			break;
		case "notice": {
			if (item.family === "approval") {
				content = <ApprovalHistory text={item.text} decidedAtMs={item.decidedAtMs} />;
				break;
			}
			if (item.notifications) {
				content = (
					<NotificationCards
						fragments={item.notifications}
						delegates={delegates}
						openSubagent={openSubagent}
						disclosureId={disclosureIds[0] ?? ""}
					/>
				);
				break;
			}
			content = isCriticalNotice(item) ? (
				<ErrorRow
					title={item.text}
					detail={item.hint ?? ""}
					action={errorActionFor?.({ id: item.id, title: item.text, detail: "", turnId: item.turnId }) ?? null}
					onAction={onErrorAction}
					attention={item.tone === "attention"}
				/>
			) : (
				<SystemEvent label={label} text={noticeText} hint={item.hint} expanded={expanded} onToggle={toggle}>
					{item.eventKind === MEMORY_CONTEXT_EVENT_KIND ? (
						<MemoryContextBody
							observation={item.memoryContext}
							source={noticeText ?? item.text}
							sourceDisclosureId={memoryContextSourceDisclosureId(hubId, sessionRef, item)}
						/>
					) : item.rendersMarkdown ? (
						<MarkdownResponse markdown={item.text} />
					) : undefined}
				</SystemEvent>
			);
			break;
		}
		case "failure":
			if (quietThought) {
				content = (
					<Text
						allowFontScaling={allowFontScaling}
						style={{ fontSize: 14 * textScale, lineHeight: 19 * textScale, color: colors.palette.inkLow }}
					>
						{item.title}
					</Text>
				);
				break;
			}
			content = (
				<ErrorRow
					title={item.title}
					detail={item.detail}
					action={errorActionFor?.(item) ?? null}
					onAction={onErrorAction}
					attention={item.attention}
				/>
			);
			break;
		case "attachments":
			content = <TranscriptImages images={item.items} hubId={hubId} />;
			break;
		case "activity":
			// The shared exit rule's inputs, read once for whichever activity
			// branch needs them (the compact intent line, or the full step line).
			const exitSignals = { status: item.state, error: item.detail.error, exitCode: item.detail.exitCode };
			if (item.family === "reasoning" && item.state !== "running") {
				content = (
					<ThoughtRow
						durationMs={item.detail.durationMs}
						text={item.detail.output}
						expanded={expanded}
						onToggle={toggle}
					/>
				);
				break;
			}
			if (item.label === "delegate") {
				content = (
					<Subagent
						ownerRef={sessionRef}
						row={item}
						delegates={delegates}
						tree={subagentTree}
						openSubagent={openSubagent}
					/>
				);
				break;
			}
			if (item.label === "ask_user") {
				const questions = askRowQuestions(item);
				if (questions) {
					content = <QuestionHistory questions={questions} answer={answerFor?.(item.id)} />;
					break;
				}
			}
			if (activityPresentation?.mode === "intent") {
				// A summary-only row is one quiet line, but an exit-only call keeps its
				// small x beside it so the phone shows the same mark the web does at
				// this level (Jesse, 2026-10-16).
				const intentExitOnly = isExitOnly(exitSignals);
				content = intentExitOnly ? (
					<View style={exitRowStyle}>
						<View style={exitTextStyle}>
							{/* The mark is decorative, so the code it stands for has to be
							named here or VoiceOver never hears it (the full step line
							names it in its own label). */}
							<Copy muted label={`${activityPresentation.summary}, exit ${item.detail.exitCode}`}>
								{activityPresentation.summary}
							</Copy>
						</View>
						<ExitMark />
					</View>
				) : (
					<Copy muted>{activityPresentation.summary}</Copy>
				);
				break;
			}
			// A tool step reads as its words (the package's toolStepSummary),
			// never its raw tool name.
			const stepName = stepWords(item);
			// An otherwise-clean call whose process exited nonzero: not a failure
			// (the shared classifier no longer reads the exit), but the exit stays
			// visible as a small x in attention ink, and the line says the code.
			const exitOnly = isExitOnly(exitSignals);
			const stepLine = (
				<View style={{ flexDirection: "row", alignItems: "center" }}>
					<FoldChevron folded={!expanded} />
					<View style={{ flex: 1, minWidth: 0 }}>
						<Button
							text
							quiet
							label={`${stepName} · ${exitOnly ? `exit ${item.detail.exitCode}` : item.state}`}
							accessibilityLabel={`${expanded ? "Collapse" : "Expand"} ${stepName}${exitOnly ? `, exit ${item.detail.exitCode}` : ""}`}
							expanded={expanded}
							onPress={toggle}
						/>
					</View>
				</View>
			);
			content = (
				<>
					{activityPresentation?.summary ? <Copy muted>{activityPresentation.summary}</Copy> : null}
					{/* Only an exit-only row needs the row wrapper: it is what seats the
					mark beside the line. A clean row keeps its line alone. */}
					{exitOnly ? (
						<View style={exitRowStyle}>
							<View style={exitTextStyle}>{stepLine}</View>
							<ExitMark />
						</View>
					) : (
						stepLine
					)}
					{expanded ? (
						<>
							<StepEvidence step={item} evidence={evidence} hubId={hubId} />
							{showDuration && item.detail.durationMs !== undefined ? (
								<Copy muted>Duration: {item.detail.durationMs} ms</Copy>
							) : null}
						</>
					) : null}
				</>
			);
			break;
		case "run":
			content = (
				<RunRow
					run={item}
					live={heldOpen}
					expanded={expanded}
					onToggle={toggle}
					hubId={hubId}
					sessionRef={sessionRef}
					evidenceOpenByDefault={expandByDefault}
				/>
			);
			break;
		case "time":
			content = <TimeMarker at={item.at} />;
			break;
	}
	return (
		// A failure and a critical notice draw their own red rule (ErrorRow).
		<View style={{ gap: 8 }}>{content}</View>
	);
}

function YourMessage({
	item,
	fork,
	quote,
}: {
	item: Extract<MobileTimelineItem, { kind: "user" }>;
	/** Set only when a fork from this message can run now. */
	fork?: (entryIndex: number, preview: string) => void;
	quote?: (text: string) => void;
}) {
	const { palette } = useColors();
	const scale = useTextScale();
	const entryIndex = item.transcriptEntryIndex;
	const menu: MenuItem[] = [
		{ name: "copy", label: "Copy", run: () => void copyText(item.text) },
		...(fork && entryIndex !== undefined
			? [
					{
						name: "fork",
						label: "Fork from here",
						run: () => fork(entryIndex, item.text),
					},
				]
			: []),
		...(quote ? [{ name: "quote", label: "Quote", run: () => quote(item.text) }] : []),
	];
	return (
		<View style={{ alignItems: "flex-end", gap: 4 }}>
			<Pressable
				accessibilityLabel={`You: ${item.text}`}
				{...menuAccessibility(menu)}
				onLongPress={() => showMenu(menu, menuPreview(item.text))}
				style={{
					alignSelf: "flex-end",
					maxWidth: "85%",
					backgroundColor: palette.bubble,
					borderRadius: 18,
					borderCurve: "continuous",
					paddingHorizontal: 14,
					paddingVertical: 10,
				}}
			>
				<Copy variant="yourMessage" selectable={false} label={`You: ${item.text}`}>
					{item.text}
				</Copy>
			</Pressable>
			{item.origin === "steered" ? (
				<Text
					allowFontScaling={allowFontScaling}
					testID="steered-caption"
					style={{ fontSize: 12 * scale, lineHeight: 16 * scale, color: palette.inkLow }}
				>
					Steered in mid-turn
				</Text>
			) : null}
		</View>
	);
}

// A shared-notes update (spec 8.2, 8.8): a 2pt left rule like a subagent row,
// a quiet caption, and the note itself in the serif prose used for your
// messages. An emptied note reads only the caption.
function NoteRow({ text }: { text: string }) {
	const { palette } = useColors();
	const scale = useTextScale();
	return (
		<View
			style={{ borderLeftWidth: 2, borderLeftColor: palette.edgeStrong, paddingLeft: 12, paddingVertical: 4, gap: 4 }}
		>
			<Text
				allowFontScaling={allowFontScaling}
				testID={`note-caption:${text ? "updated" : "cleared"}`}
				style={{ fontSize: 13 * scale, lineHeight: 18 * scale, color: palette.inkMid }}
			>
				{text ? "You updated your note" : "You cleared your note"}
			</Text>
			{text ? <Copy variant="yourMessage">{text}</Copy> : null}
		</View>
	);
}

function AgentMessage({
	markdown,
	quote,
	fileContext,
}: {
	markdown: string;
	quote?: (text: string) => void;
	fileContext?: NativeFileOpenContext;
}) {
	const colors = useColors();
	const [selecting, setSelecting] = useState(false);
	// Memoized so an unchanged message hands MarkdownResponse the same props
	// and its memo skips the render the list asks of every row on each publish.
	const menu = useMemo<MenuItem[]>(
		() => [
			{ name: "copy", label: "Copy", run: () => void copyText(markdown) },
			...(quote ? [{ name: "quote", label: "Quote in reply", run: () => quote(markdown) }] : []),
			{ name: "select", label: "Select text", run: () => setSelecting(true) },
		],
		[markdown, quote],
	);
	const accessibility = useMemo(() => menuAccessibility(menu), [menu]);
	// A Pressable here would become the touch responder and every link in the
	// prose would go dead (#4839). A long-press gesture waits for the hold and
	// leaves a short tap to the text view, so links still open. The detector
	// needs one host view, and this View is also what covers a diagram-only
	// reply and every segment of a message with a diagram in it.
	const holdForMenu = useMemo(
		() =>
			Gesture.LongPress()
				.runOnJS(true)
				.onStart(() => showMenu(menu, menuPreview(markdown))),
		[menu, markdown],
	);
	return (
		<>
			<GestureDetector gesture={holdForMenu}>
				<View>
					<MarkdownResponse
						markdown={markdown || "…"}
						selectable={false}
						fileContext={fileContext}
						{...accessibility}
					/>
				</View>
			</GestureDetector>
			<HoldingModal
				visible={selecting}
				animationType="slide"
				presentationStyle="fullScreen"
				onRequestClose={() => setSelecting(false)}
			>
				<SafeAreaView style={[styles.fill, { backgroundColor: colors.background }]}>
					<View style={[styles.row, { paddingHorizontal: 16, justifyContent: "flex-end" }]}>
						<Button text label="Done" onPress={() => setSelecting(false)} />
					</View>
					<ScrollView contentContainerStyle={{ padding: 16 }}>
						<Copy variant="agentProse">{markdown}</Copy>
					</ScrollView>
				</SafeAreaView>
			</HoldingModal>
		</>
	);
}

function Subagent({
	ownerRef,
	row,
	delegates,
	tree,
	openSubagent,
}: {
	ownerRef: string;
	row: Extract<TimelineRow, { kind: "activity" }>;
	delegates: readonly EvenerDelegateInfo[] | undefined;
	tree: ActivityTree | null | undefined;
	openSubagent: ((ref: string, title: string, runStartedAt?: string) => void) | undefined;
}) {
	// The state's time ("failed · 6m") moves on with the minute clock, and
	// its line turns Quiet the moment a silent subagent crosses into it.
	const now = useNowPastQuiet(useMinuteClock(), (at) => [subagentLine(row, delegates, at).quietForMs]);
	const line = subagentLine(row, delegates, now);
	// A finished subagent's outcome, as the Agents list gives it, once the
	// tree shows it done; until then the line says what the roster knows.
	const finished = line.state === "done" ? line.delegateId : undefined;
	const outcome = useMemo(
		() => (tree && finished ? subagentOutcome(tree, finished, now, ownerRef, line.runGeneration) : undefined),
		[tree, finished, now, ownerRef, line.runGeneration],
	);
	return (
		<SubagentRow line={outcome ? { ...line, activity: outcome, activityFrom: "tree" } : line} onOpen={openSubagent} />
	);
}

function TimeMarker({ at }: { at: number }) {
	const { palette } = useColors();
	const scale = useTextScale();
	const now = useMinuteClock();
	return (
		<Text
			allowFontScaling={allowFontScaling}
			style={{
				paddingTop: 16,
				fontSize: 12 * scale,
				lineHeight: 16 * scale,
				color: palette.inkLow,
				textAlign: "center",
				fontVariant: ["tabular-nums"],
			}}
		>
			{timeMarkerText(at, now)}
		</Text>
	);
}
