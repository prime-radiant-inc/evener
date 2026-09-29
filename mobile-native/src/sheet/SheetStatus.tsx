// A sheet's word on the connection (spec 14): one line, no button. The app
// reconnects on its own (hubConnection.ts), so nothing here asks you to.
import { ActivityIndicator, Text } from "react-native";
import { useConnectionStatusText } from "../board/connectionStatus";
import { INCOMPATIBLE_VERSIONS } from "../connectionRecovery";
import { useConnection } from "../ConnectionProvider";
import { allowFontScaling, useColors, useTextScale } from "../ui";

/** The connection's line at the top of a sheet page; nothing while live. */
export function SheetStatus() {
	const line = useConnectionStatusText();
	const { palette } = useColors();
	const scale = useTextScale();
	if (!line) return null;
	return (
		<Text
			allowFontScaling={allowFontScaling}
			style={{
				color: palette.inkMid,
				fontSize: 13 * scale,
				lineHeight: 18 * scale,
				textAlign: "center",
				paddingVertical: 6,
			}}
		>
			{line}
		</Text>
	);
}

/** What a page that has never loaded shows until its first read lands: spec
 * 15's "Connecting to magic-kingdom…" only while the connection is down, spec
 * 14's sentence when no retry can help, and nothing about the connection
 * while it's live (spec 14), just the spinner, since the read is on its way. */
export function FirstLoad({ hubName }: { hubName: string }) {
	const { state, fatal } = useConnection();
	const { palette } = useColors();
	const scale = useTextScale();
	if (!fatal && state === "ready") return <Loading label="Loading" />;
	return (
		<Text
			allowFontScaling={allowFontScaling}
			style={{ color: palette.inkMid, fontSize: 15 * scale, lineHeight: 20 * scale, textAlign: "center", padding: 32 }}
		>
			{fatal ? INCOMPATIBLE_VERSIONS : `Connecting to ${hubName}…`}
		</Text>
	);
}

/** A spinner in the space a sheet's first-load line takes, so a page never
 * jumps between waiting states. `label` is what VoiceOver says it waits on. */
export function Loading({ label }: { label: string }) {
	return <ActivityIndicator accessibilityLabel={label} style={{ padding: 32 }} />;
}
