import type { ReactNode } from "react";
import { View } from "react-native";
import {
	scopedDisclosureId,
	toggleDisclosure,
	isDisclosureOpen as useDisclosureOpen,
} from "../../cmd/evener-hub/frontend/src/widgets/disclosure/disclosureStore";
import { MarkdownResponse } from "./MarkdownResponse";
import { TranscriptImages } from "./TranscriptImages";
import {
	isCriticalNotice,
	steeringNoticeLabel,
	type TimelineRow,
} from "./timeline";
import type { ActivityPresentation } from "./transcriptPresentation";
import { Action, Copy, useColors } from "./ui";

export function TimelineItem({
	item,
	hubId,
	sessionRef,
	activityPresentation,
	expandByDefault = false,
	showDuration = true,
	fork,
}: {
	item: TimelineRow;
	hubId: string;
	sessionRef: string;
	activityPresentation?: ActivityPresentation;
	expandByDefault?: boolean;
	showDuration?: boolean;
	fork?: (entryIndex: number, preview: string) => void;
}) {
	const disclosureId = scopedDisclosureId(
		JSON.stringify([hubId, sessionRef]),
		JSON.stringify([item.kind, item.id]),
	);
	const defaultOpen = item.kind === "activity" && expandByDefault;
	const expanded = useDisclosureOpen(disclosureId, defaultOpen);
	const toggle = () => toggleDisclosure(disclosureId, defaultOpen);
	const colors = useColors();
	const noticeLabel =
		item.kind === "notice" ? steeringNoticeLabel(item) : undefined;
	let content: ReactNode;
	switch (item.kind) {
		case "details":
			content = (
				<>
					<Action
						tone="quiet"
						label={`Session details, ${item.entries.length} ${item.entries.length === 1 ? "entry" : "entries"}`}
						expanded={expanded}
						onPress={toggle}
					>{`${expanded ? "▾" : "▸"} Session details · ${item.entries.length}`}</Action>
					{expanded
						? item.entries.map((entry) => (
								<TimelineItem
									key={entry.id}
									item={entry}
									hubId={hubId}
									sessionRef={sessionRef}
								/>
							))
						: null}
				</>
			);
			break;
		case "user":
			content = (
				<>
					<Copy label={`You: ${item.text}`}>{item.text}</Copy>
					{fork &&
					item.transcriptEntryIndex !== undefined &&
					Number.isSafeInteger(item.transcriptEntryIndex) &&
					item.transcriptEntryIndex > 0 ? (
						<Action
							tone="quiet"
							label={`Fork from message: ${item.text.slice(0, 80)}`}
							onPress={() => {
								if (item.transcriptEntryIndex !== undefined)
									fork(item.transcriptEntryIndex, item.text);
							}}
						>
							Fork from here
						</Action>
					) : null}
				</>
			);
			break;
		case "assistant":
			content = (
				<>
					{item.streaming ? <Copy muted>Writing…</Copy> : null}
					<MarkdownResponse markdown={item.markdown || "…"} />
				</>
			);
			break;
		case "notice":
			content = noticeLabel ? (
				<>
					<Action
						tone="quiet"
						label={`${noticeLabel}, ${expanded ? "hide" : "show"} notice`}
						expanded={expanded}
						onPress={toggle}
					>{`${expanded ? "▾" : "▸"} ${noticeLabel}`}</Action>
					{expanded ? <Copy>{item.text}</Copy> : null}
				</>
			) : (
				<>
					<Copy muted>{isCriticalNotice(item) ? "Warning" : "Notice"}</Copy>
					<Copy>{item.text}</Copy>
				</>
			);
			break;
		case "failure":
			content = (
				<>
					<Copy>{item.title}</Copy>
					<Copy>{item.detail}</Copy>
				</>
			);
			break;
		case "attachments":
			content = (
				<>
					<Copy muted>Attachments</Copy>
					<TranscriptImages images={item.items} hubId={hubId} />
				</>
			);
			break;
		case "question":
			content = (
				<>
					{item.batch.questions.map((question) => (
						<View key={question.key} style={{ gap: 8 }}>
							<Copy>{question.header}</Copy>
							<Copy>{question.question}</Copy>
							{question.options.map((option) => (
								<Copy key={`${question.key}:${option.label}`}>
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
	}
	return (
		<View
			style={[
				{ gap: 8 },
				item.kind === "user"
					? {
							backgroundColor: colors.surface,
							borderRadius: 18,
							borderBottomRightRadius: 5,
							padding: 14,
							marginLeft: 24,
						}
					: item.kind === "question" ||
							item.kind === "failure" ||
							(item.kind === "notice" && isCriticalNotice(item))
						? {
								borderLeftWidth: 2,
								borderLeftColor:
									item.kind === "failure" ? colors.error : colors.accent,
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
