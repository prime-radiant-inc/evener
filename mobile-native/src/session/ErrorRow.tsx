// An error in the transcript (spec 8.2, "Error"): a red rule, the hub's words
// as they are, and at most one action (errorAction).
import { Pressable, Text, View } from "react-native";
import { allowFontScaling, useColors, useTextScale } from "../ui";
import type { ErrorAction } from "./errorAction";


const ACTION_LABELS: Record<ErrorAction, string> = { resume: "Resume", signIn: "Sign in", retry: "Retry" };

export function ErrorRow({
	title,
	detail,
	action,
	onAction,
}: {
	title: string;
	detail: string;
	action: ErrorAction | null;
	onAction?: (action: ErrorAction) => void;
}) {
	const { palette } = useColors();
	const scale = useTextScale();
	const body = { fontSize: 15 * scale, lineHeight: 20 * scale };
	return (
		<View style={{ borderLeftWidth: 2, borderLeftColor: palette.dangerInk, paddingLeft: 12, gap: 4 }}>
			<Text allowFontScaling={allowFontScaling} style={{ ...body, fontWeight: "600", color: palette.inkHi }}>
				{title}
			</Text>
			{detail ? (
				<Text allowFontScaling={allowFontScaling} style={{ ...body, color: palette.inkMid }}>
					{detail}
				</Text>
			) : null}
			{action && onAction ? (
				<Pressable
					accessibilityRole="button"
					accessibilityLabel={ACTION_LABELS[action]}
					accessibilityState={{ disabled: false }}
					onPress={() => onAction(action)}
					style={{ minHeight: 44, justifyContent: "center", alignSelf: "flex-start" }}
				>
					<Text allowFontScaling={allowFontScaling} style={{ ...body, color: palette.accentInk }}>
						{ACTION_LABELS[action]}
					</Text>
				</Pressable>
			) : null}
		</View>
	);
}
