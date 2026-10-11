// A system event in the transcript (spec 8.2): a diamond in a 16pt gutter and
// the event in 13pt ink-low, two lines at most until tapped — its text, and a
// warning's hint beneath when it has one, two lines each. A labelled event
// (a steering notice, or the Session details group) shows its label with a
// chevron and opens to what it says.
import { SymbolView } from "expo-symbols";
import type { ReactNode } from "react";
import { Pressable, Text, View } from "react-native";
import { allowFontScaling, useColors, useTextScale } from "../ui";

/** The diamond in a system event's 16pt gutter. */
export function SystemEventMark() {
	const { palette } = useColors();
	const scale = useTextScale();
	return (
		<View style={{ width: 16 }}>
			<SymbolView name="diamond" tintColor={palette.inkLow} size={8 * scale} />
		</View>
	);
}

export function SystemEvent({
	label,
	text,
	hint,
	expanded,
	onToggle,
	children,
}: {
	/** Shown in place of the text until opened. */
	label?: string;
	text?: string;
	/** A warning's what-to-do, a quiet second line under the text. */
	hint?: string;
	expanded: boolean;
	onToggle: () => void;
	/** What opens under a labelled event, when it isn't plain text. */
	children?: ReactNode;
}) {
	const { palette } = useColors();
	const scale = useTextScale();
	const quiet = { fontSize: 13 * scale, lineHeight: 18 * scale, color: palette.inkLow };
	// A long label truncates to one line so its chevron stays in view; the
	// button's accessibility label still says it in full.
	const line = label ? (
		<View style={{ flexDirection: "row", alignItems: "center", gap: 4 }}>
			<Text allowFontScaling={allowFontScaling} numberOfLines={1} style={{ ...quiet, flexShrink: 1 }}>
				{label}
			</Text>
			<SymbolView name={expanded ? "chevron.down" : "chevron.right"} tintColor={palette.inkLow} size={10 * scale} />
		</View>
	) : (
		<>
			<Text allowFontScaling={allowFontScaling} numberOfLines={expanded ? undefined : 2} style={quiet}>
				{text}
			</Text>
			{hint ? (
				<Text allowFontScaling={allowFontScaling} numberOfLines={expanded ? undefined : 2} style={quiet}>
					{hint}
				</Text>
			) : null}
		</>
	);
	return (
		<View>
			<Pressable
				accessibilityRole="button"
				accessibilityLabel={label ?? [text, hint].filter(Boolean).join("\n")}
				accessibilityState={{ expanded }}
				onPress={onToggle}
				style={{ flexDirection: "row", alignItems: "center", minHeight: 44 }}
			>
				<SystemEventMark />
				<View style={{ flex: 1, minWidth: 0 }}>{line}</View>
			</Pressable>
			{label && expanded ? (
				<View style={{ paddingLeft: 16, gap: 4 }}>
					{children ?? (
						<Text allowFontScaling={allowFontScaling} style={quiet}>
							{text}
						</Text>
					)}
				</View>
			) : null}
		</View>
	);
}
