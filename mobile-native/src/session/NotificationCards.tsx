// A steer that delivers delegate or job notifications (spec 8.2, 9): each
// notification reads as a system event, a diamond in the gutter and one line
// saying who and what happened, in red when it failed, with at most two lines
// beneath. It looks nothing like the subagent's own row, so a subagent that
// reported never reads as two subagents. Tapping opens the subagent, or shows
// a job's output in place the way a step's evidence opens.
import { type EvenerDelegateInfo, lineCount, type SteeringFragment } from "@evener/appwire-client";
import { Pressable, Text, View } from "react-native";
import { toggleDisclosure, useDisclosureOpen } from "../nativeDisclosure";
import { allowFontScaling, useColors, useTextScale } from "../ui";
import { type NotificationLine, notificationLine, notificationText } from "./notificationLine";
import { EvidenceView } from "./StepEvidence";
import { SystemEventMark } from "./SystemEvent";

export function NotificationCards({
	fragments,
	delegates,
	openSubagent,
	disclosureId,
}: {
	fragments: readonly SteeringFragment[];
	delegates?: readonly EvenerDelegateInfo[];
	openSubagent?: (ref: string, title: string) => void;
	/** Scopes each card's open state: the row's own disclosure id. */
	disclosureId: string;
}) {
	return (
		<View>
			{fragments.map((fragment, index) => {
				// Two frames in one steer can be byte-identical, so neither a
				// frame's text nor a text span is a safe key. fragments is an
				// order-stable parse of this one row's text, so its position is.
				const key = `${disclosureId}:${index}`;
				return fragment.kind === "notification" ? (
					<NotificationCard
						key={key}
						line={notificationLine(fragment.notification, delegates)}
						openSubagent={openSubagent}
						disclosureId={key}
					/>
				) : (
					<QuietText key={key} text={notificationText(fragment.text)} />
				);
			})}
		</View>
	);
}

function useQuietText() {
	const { palette } = useColors();
	const scale = useTextScale();
	return { fontSize: 13 * scale, lineHeight: 18 * scale, color: palette.inkLow };
}

function NotificationCard({
	line,
	openSubagent,
	disclosureId,
}: {
	line: NotificationLine;
	openSubagent?: (ref: string, title: string) => void;
	disclosureId: string;
}) {
	const { palette } = useColors();
	const quiet = useQuietText();
	const expanded = useDisclosureOpen(disclosureId, false);
	const { subagent, output } = line;
	const open = subagent && openSubagent ? () => openSubagent(subagent.ref, subagent.title) : undefined;
	const onPress = open ?? (output ? () => toggleDisclosure(disclosureId, false) : undefined);
	const label = line.detail ? `${line.headline}, ${line.detail}` : line.headline;
	const body = (
		<>
			<View style={{ flexDirection: "row", alignItems: "center" }}>
				<SystemEventMark />
				<Text
					allowFontScaling={allowFontScaling}
					style={{ ...quiet, flex: 1, color: line.failed ? palette.dangerInk : palette.inkLow }}
				>
					{line.headline}
				</Text>
			</View>
			{line.detail ? (
				<Text allowFontScaling={allowFontScaling} numberOfLines={2} style={{ ...quiet, paddingLeft: 16 }}>
					{line.detail}
				</Text>
			) : null}
		</>
	);
	return (
		<View style={{ gap: 4 }}>
			{onPress ? (
				<Pressable
					accessibilityRole="button"
					accessibilityLabel={label}
					accessibilityHint={open ? "Opens the subagent" : "Shows the output"}
					accessibilityState={open ? undefined : { expanded }}
					onPress={onPress}
					style={{ minHeight: 44, justifyContent: "center" }}
				>
					{body}
				</Pressable>
			) : (
				<View accessible accessibilityLabel={label} style={{ minHeight: 44, justifyContent: "center" }}>
					{body}
				</View>
			)}
			{output && !open && expanded ? (
				<View style={{ paddingLeft: 16 }}>
					<EvidenceView evidence={{ kind: "output", text: output, lines: lineCount(output) }} title={line.headline} />
				</View>
			) : null}
		</View>
	);
}

function QuietText({ text }: { text: string }) {
	const quiet = useQuietText();
	return (
		<Text allowFontScaling={allowFontScaling} numberOfLines={2} style={{ ...quiet, paddingLeft: 16 }}>
			{text}
		</Text>
	);
}
