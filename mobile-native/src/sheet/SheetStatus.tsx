// A sheet's word on the connection (spec 14): one line, no button. The app
// reconnects on its own (hubConnection.ts), so nothing here asks you to.
import { Text } from "react-native";
import { useConnectionStatusText } from "../board/connectionStatus";
import { INCOMPATIBLE_VERSIONS } from "../connectionRecovery";
import { useConnection } from "../ConnectionProvider";
import { scaledType, uiType } from "../design/tokens";
import { allowFontScaling, useColors, useTextScale } from "../ui";
import { Spinner } from "./Spinner";

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
				...scaledType(uiType.footnote, scale),
				textAlign: "center",
				paddingVertical: 6,
			}}
		>
			{line}
		</Text>
	);
}

/** What a page that has never loaded shows until its first read lands: spec
 * 15's "Connecting to magic-kingdom…" only while the app is connecting,
 * spec 14's sentence when no retry can help, and otherwise nothing about the
 * connection (spec 14): the quiet spinner, named by `label`, with the
 * sheet's status line above saying anything more. */
export function FirstLoad({ hubName, label }: { hubName: string; label: string }) {
	const { state, fatal } = useConnection();
	const { palette } = useColors();
	const scale = useTextScale();
	if (!fatal && state !== "connecting" && state !== "reconnecting") return <Spinner label={label} />;
	return (
		<Text
			allowFontScaling={allowFontScaling}
			style={{ color: palette.inkMid, ...scaledType(uiType.subheadline, scale), textAlign: "center", padding: 32 }}
		>
			{fatal ? INCOMPATIBLE_VERSIONS : `Connecting to ${hubName}…`}
		</Text>
	);
}
