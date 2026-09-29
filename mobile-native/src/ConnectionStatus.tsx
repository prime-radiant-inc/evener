import { View } from "react-native";
import { useConnectionStatusText } from "./board/connectionStatus";
import { useConnection } from "./ConnectionProvider";
import { Copy, ErrorMessage } from "./ui";

// Its own file, not screens.tsx: screens.tsx pulls in the whole native
// screen graph (@react-navigation/elements among it), which vitest cannot
// load - a value import of this component from a test-reached module (a
// screen's wall/banner) must not drag that graph in just to render a status
// row.
/** The connection's status row for the older screens and their modals: the
 * hub's name and the words every screen says, on the provider's one clock
 * (spec 14), with the connection's own failure copy. Nothing while live or
 * during a blip shorter than 2 seconds. */
export function ConnectionStatus({ inset = 16 }: { inset?: number } = {}) {
	const { error, activeProfile } = useConnection();
	const status = useConnectionStatusText();
	if (status === null) return null;
	return (
		<View style={{ paddingHorizontal: inset, paddingVertical: 8 }}>
			<Copy muted>{`${activeProfile?.name ?? "No hub selected"} · ${status}`}</Copy>
			<ErrorMessage message={error} />
		</View>
	);
}
