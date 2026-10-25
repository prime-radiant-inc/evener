// An error in the transcript (spec 8.2, "Error"): a red rule, the hub's words
// as they are, and at most one action (errorAction). A warning is the same row
// with an amber rule (attention): amber means a human is needed, red that
// something failed (#3387). A warning reports, so it offers no action: Retry
// answers a failure. VoiceOver hears which it is, since the rule's
// colour alone can't tell it.
import { Pressable, Text, View } from "react-native";
import { allowFontScaling, useColors, useTextScale } from "../ui";
import type { ErrorAction } from "./errorAction";

function actionLabel(action: ErrorAction): string {
	return action.kind === "signIn" ? "Sign in" : "Retry";
}

function actionDisabled(action: ErrorAction): boolean {
	return action.kind === "retry" && action.disabled;
}

export function ErrorRow({
	title,
	detail,
	action,
	onAction,
	attention = false,
}: {
	title: string;
	detail: string;
	action: ErrorAction | null;
	onAction?: (action: ErrorAction) => void;
	attention?: boolean;
}) {
	const { palette } = useColors();
	const scale = useTextScale();
	const body = { fontSize: 15 * scale, lineHeight: 20 * scale };
	const offered = attention ? null : action;
	const disabled = offered === null ? false : actionDisabled(offered);
	return (
		<View
			style={{
				borderLeftWidth: 2,
				borderLeftColor: attention ? palette.attention : palette.dangerInk,
				paddingLeft: 12,
				gap: 4,
			}}
		>
			<Text
				allowFontScaling={allowFontScaling}
				accessibilityLabel={`${attention ? "Warning" : "Error"}: ${title}`}
				style={{ ...body, fontWeight: "600", color: palette.inkHi }}
			>
				{title}
			</Text>
			{detail ? (
				<Text allowFontScaling={allowFontScaling} style={{ ...body, color: palette.inkMid }}>
					{detail}
				</Text>
			) : null}
			{offered && onAction ? (
				<Pressable
					accessibilityRole="button"
					accessibilityLabel={actionLabel(offered)}
					accessibilityState={{ disabled }}
					disabled={disabled}
					onPress={() => onAction(offered)}
					style={{ minHeight: 44, justifyContent: "center", alignSelf: "flex-start" }}
				>
					<Text allowFontScaling={allowFontScaling} style={{ ...body, color: palette.accentInk }}>
						{actionLabel(offered)}
					</Text>
				</Pressable>
			) : null}
		</View>
	);
}
