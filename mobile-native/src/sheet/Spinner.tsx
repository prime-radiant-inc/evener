// The one waiting spinner the Hub and its sheets draw: a first load, a list
// on its way, a write in flight. Every one sits in the same padded space the
// first-load line takes, so a page never jumps between waiting states.
import { ActivityIndicator } from "react-native";

/** `label` is what VoiceOver says it waits on, such as "Loading hosts". */
export function Spinner({ label }: { label: string }) {
	return <ActivityIndicator accessibilityLabel={label} style={{ padding: 32 }} />;
}
