// A sheet's word on the connection (spec 14): one line, no button. The app
// reconnects on its own (hubConnection.ts), so nothing here asks you to.
import { Text } from "react-native";
import { UPDATE_NEEDED_HINT, useConnectionStatusText } from "../board/connectionStatus";
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

/** What a page that has never loaded says while the connection comes up, in
 * place of a wall with a Reconnect button: spec 15's "Connecting to
 * magic-kingdom…", or spec 14's sentence when no retry can help. */
export function Connecting({ hubName }: { hubName: string }) {
	const { fatal } = useConnection();
	const { palette } = useColors();
	const scale = useTextScale();
	return (
		<Text
			allowFontScaling={allowFontScaling}
			style={{ color: palette.inkMid, fontSize: 15 * scale, lineHeight: 20 * scale, textAlign: "center", padding: 32 }}
		>
			{fatal ? UPDATE_NEEDED_HINT : `Connecting to ${hubName}…`}
		</Text>
	);
}
