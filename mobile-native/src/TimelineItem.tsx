import { type EvenerDelegateInfo, scopedDisclosureId } from "@evener/appwire-client";
import { type ReactNode, useMemo, useState } from "react";
import {
	Modal,
	Platform,
	Pressable,
	ScrollView,
	Text,
	View,
} from "react-native";
import { SafeAreaView } from "react-native-safe-area-context";
import { copyText } from "./clipboard";
import { type MenuItem, menuAccessibility, menuPreview, showMenu } from "./longPressMenu";
import { MarkdownResponse } from "./MarkdownResponse";
import { toggleDisclosure, useDisclosureOpen } from "./nativeDisclosure";
import type { MobileTimelineItem } from "./projectedRows";
import { useMinuteClock } from "./session/minuteClock";
import type { ErrorAction } from "./session/errorAction";
import { ErrorRow } from "./session/ErrorRow";
import { QuestionHistory } from "./session/QuestionHistory";
import { RunRow } from "./session/RunRow";
import { SubagentRow } from "./session/SubagentRow";
import { SystemEvent } from "./session/SystemEvent";
import { subagentLine } from "./session/subagentLine";
import { ThoughtRow } from "./session/ThoughtRow";
import { askRowQuestions, timeMarkerText } from "./session/transcriptRows";
import { TranscriptImages } from "./TranscriptImages";
import {
	isCriticalNotice,
	questionOptionKey,
	steeringNoticeLabel,
	type TimelineRow,
} from "./timeline";
import type { ActivityPresentation } from "./transcriptPresentation";
import { Action, Copy, styles, useColors, useTextScale } from "./ui";

export function TimelineItem({
	item,
	hubId,
	sessionRef,
	activityPresentation,
	expandByDefault = false,
	showDuration = true,
	fork,
	forkDisabled = false,
	quote,
	live = false,
	delegates,
	openSubagent,
	answerFor,
	errorActionFor,
	onErrorAction,
}: {
	item: TimelineRow;
	hubId: string;
	sessionRef: string;
	activityPresentation?: ActivityPresentation;
	expandByDefault?: boolean;
	showDuration?: boolean;
	fork?: (entryIndex: number, preview: string) => void;
	forkDisabled?: boolean;
	/** Quotes text into the composer's draft. */
	quote?: (text: string) => void;
	/** This row is the live run: the last run of the turn in progress. */
	live?: boolean;
	/** The session's subagents, for a subagent row's state and activity. */
	delegates?: readonly EvenerDelegateInfo[];
	/** Opens a subagent's own transcript. */
	openSubagent?: (ref: string, title: string) => void;
	/** Your answer to the question an ask_user row asked, when you gave one. */
	answerFor?: (itemId: string) => string | undefined;
	/** The one action an error row offers (errorAction), and running it. */
	errorActionFor?: (row: { id: string; title: string; detail: string; turnId?: string }) => ErrorAction | null;
	onErrorAction?: (action: ErrorAction) => void;
}) {
	const disclosureId = scopedDisclosureId(
		JSON.stringify([hubId, sessionRef]),
		JSON.stringify([item.kind, item.id]),
	);
	const defaultOpen =
		(item.kind === "activity" || item.kind === "run") && expandByDefault;
	const expanded = useDisclosureOpen(disclosureId, defaultOpen);
	const toggle = () => toggleDisclosure(disclosureId, defaultOpen);
	const colors = useColors();
	// A thought the projector didn't show reads as one quiet line, with no
	// error rule, unless the thought itself failed.
	const quietThought = item.kind === "failure" && item.thought === true && item.title !== "Thought failed";
	const textScale = useTextScale();
	const noticeLabel =
		item.kind === "notice" ? steeringNoticeLabel(item) : undefined;
	let content: ReactNode;
	switch (item.kind) {
		case "details":
			content = (
				<SystemEvent
					label={`Session details · ${item.entries.length}`}
					expanded={expanded}
					onToggle={toggle}
				>
					{item.entries.map((entry) => (
						<TimelineItem
							key={entry.id}
							item={entry}
							hubId={hubId}
							sessionRef={sessionRef}
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
		case "assistant":
			content = <AgentMessage markdown={item.markdown} quote={quote} />;
			break;
		case "notice":
			content = isCriticalNotice(item) ? (
				<ErrorRow
					title={item.text}
					detail=""
					action={errorActionFor?.({ id: item.id, title: item.text, detail: "", turnId: item.turnId }) ?? null}
					onAction={onErrorAction}
				/>
			) : (
				<SystemEvent label={noticeLabel} text={item.text} expanded={expanded} onToggle={toggle} />
			);
			break;
		case "failure":
			if (quietThought) {
				content = (
					<Text
						allowFontScaling={Platform.OS !== "ios"}
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
				/>
			);
			break;
		case "attachments":
			content = <TranscriptImages images={item.items} hubId={hubId} />;
			break;
		case "question":
			content = (
				<>
					{item.questions.map((question) => (
						<View key={question.key} style={{ gap: 8 }}>
							<Copy>{question.header}</Copy>
							<Copy>{question.question}</Copy>
							{question.options.map((option, index) => (
								// biome-ignore lint/suspicious/noArrayIndexKey: an ask's options render in the order the agent offered them; their bounded labels can cut to the same string past the display bound, so position is their only collision-free identity.
								<Copy key={questionOptionKey(question.key, index)}>
									{option.label}
									{option.recommended ? " (recommended)" : ""}
									{option.detail ? ` — ${option.detail}` : ""}
								</Copy>
							))}
							{question.why ? <Copy muted>{question.why}</Copy> : null}
						</View>
					))}
					<Copy muted>Open Questions to answer by the composer.</Copy>
				</>
			);
			break;
		case "activity":
			if (item.family === "reasoning" && item.state !== "running") {
				content = (
					<ThoughtRow durationMs={item.detail.durationMs} text={item.detail.output} expanded={expanded} onToggle={toggle} />
				);
				break;
			}
			if (item.label === "delegate" || item.label === "delegate_send") {
				content = <Subagent row={item} delegates={delegates} openSubagent={openSubagent} />;
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
				content = <Copy muted>{activityPresentation.summary}</Copy>;
				break;
			}
			content = (
				<>
					{activityPresentation?.summary ? (
						<Copy muted>{activityPresentation.summary}</Copy>
					) : null}
					<Action
						tone="quiet"
						label={`${expanded ? "Collapse" : "Expand"} ${item.label}`}
						expanded={expanded}
						onPress={toggle}
					>{`${expanded ? "▾" : "▸"} ${item.label} · ${item.state}`}</Action>
					{expanded ? (
						<>
							{item.detail.arguments ? (
								<>
									<Copy muted>Input</Copy>
									<Copy>{item.detail.arguments}</Copy>
								</>
							) : null}
							{item.detail.output ? (
								<>
									<Copy muted>Output</Copy>
									<Copy>{item.detail.output}</Copy>
								</>
							) : null}
							{item.detail.error ? (
								<>
									<Copy muted>Error</Copy>
									<Copy>{item.detail.error}</Copy>
								</>
							) : null}
							{item.detail.exitCode !== undefined ? (
								<Copy muted>Exit code: {item.detail.exitCode}</Copy>
							) : null}
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
					live={live}
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
		<View
			style={[
				{ gap: 8 },
				// A failure and a critical notice draw their own red rule (ErrorRow).
				item.kind === "question"
					? {
							borderLeftWidth: 2,
							borderLeftColor: colors.accent,
							paddingLeft: 14,
							paddingVertical: 8,
						}
					: null,
			]}
		>
			{content}
		</View>
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
		...(quote
			? [{ name: "quote", label: "Quote", run: () => quote(item.text) }]
			: []),
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
					allowFontScaling={Platform.OS !== "ios"}
					style={{ fontSize: 12 * scale, lineHeight: 16 * scale, color: palette.inkLow }}
				>
					Steered in mid-turn
				</Text>
			) : null}
		</View>
	);
}

function AgentMessage({
	markdown,
	quote,
}: {
	markdown: string;
	quote?: (text: string) => void;
}) {
	const colors = useColors();
	const [selecting, setSelecting] = useState(false);
	// Memoized so an unchanged message hands MarkdownResponse the same props
	// and its memo skips the render the list asks of every row on each publish.
	const menu = useMemo<MenuItem[]>(
		() => [
			{ name: "copy", label: "Copy", run: () => void copyText(markdown) },
			...(quote
				? [{ name: "quote", label: "Quote in reply", run: () => quote(markdown) }]
				: []),
			{ name: "select", label: "Select text", run: () => setSelecting(true) },
		],
		[markdown, quote],
	);
	const accessibility = useMemo(() => menuAccessibility(menu), [menu]);
	return (
		<>
			{/* The markdown view stays VoiceOver's element (it reads the
			formatting and its links); the pressable only adds touch and hold. */}
			<Pressable accessible={false} onLongPress={() => showMenu(menu, menuPreview(markdown))}>
				<MarkdownResponse
					markdown={markdown || "…"}
					selectable={false}
					{...accessibility}
				/>
			</Pressable>
			<Modal
				visible={selecting}
				animationType="slide"
				presentationStyle="fullScreen"
				onRequestClose={() => setSelecting(false)}
			>
				<SafeAreaView
					style={[styles.fill, { backgroundColor: colors.background }]}
				>
					<View
						style={[
							styles.row,
							{ paddingHorizontal: 16, justifyContent: "flex-end" },
						]}
					>
						<Action onPress={() => setSelecting(false)}>Done</Action>
					</View>
					<ScrollView contentContainerStyle={{ padding: 16 }}>
						<Copy variant="agentProse">{markdown}</Copy>
					</ScrollView>
				</SafeAreaView>
			</Modal>
		</>
	);
}

function Subagent({
	row,
	delegates,
	openSubagent,
}: {
	row: Extract<TimelineRow, { kind: "activity" }>;
	delegates: readonly EvenerDelegateInfo[] | undefined;
	openSubagent: ((ref: string, title: string) => void) | undefined;
}) {
	// The state's time ("failed · 6m") moves on with the minute clock.
	const now = useMinuteClock();
	return <SubagentRow line={subagentLine(row, delegates, now)} onOpen={openSubagent} />;
}

function TimeMarker({ at }: { at: number }) {
	const { palette } = useColors();
	const scale = useTextScale();
	const now = useMinuteClock();
	return (
		<Text
			allowFontScaling={Platform.OS !== "ios"}
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
