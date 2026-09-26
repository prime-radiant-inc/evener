import { Platform, View } from "react-native";
import { useConnection } from "./ConnectionProvider";
import { Action, Copy, ErrorMessage, styles } from "./ui";

// Its own file, not screens.tsx: screens.tsx pulls in the whole native
// screen graph (@react-navigation/elements among it), which vitest cannot
// load - a value import of this component from a test-reached module (a
// screen's wall/banner) must not drag that graph in just to render a status
// row.
export function ConnectionStatus({ inset = 16 }: { inset?: number } = {}) {
	const { state, error, retry, activeProfile } = useConnection();
	return (
		<View style={{ paddingHorizontal: inset }}>
			<View
				style={[
					styles.row,
					// Reserve the reconnect action's height so offscreen header changes
					// do not shift the conversation underneath the reader.
					{ minHeight: Platform.OS === "ios" ? 44 : 48 },
				]}
			>
				<View style={styles.fill}>
					<Copy muted>
						{activeProfile?.name ?? "No hub selected"} ·{" "}
						{state === "ready" ? "Connected" : state}
					</Copy>
				</View>
				{state !== "ready" ? <Action onPress={retry}>Reconnect</Action> : null}
			</View>
			<ErrorMessage message={error} />
		</View>
	);
}
