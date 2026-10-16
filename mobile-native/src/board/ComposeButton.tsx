// The Board's floating compose button (spec 7.1): a 56pt accent circle over
// the Board's end, with a centered pencil symbol. It stays reachable while
// the Board scrolls, clear of the home indicator, and dims while the hub is
// away, since New session needs the hub.
import { SymbolView } from "expo-symbols";
import { Pressable } from "react-native";
import { useSafeAreaInsets } from "react-native-safe-area-context";
import { FLOATING_SHADOW } from "../design/underBar";
import { useColors } from "../ui";

/** 56pt across; its own room over the Board's end, clear of the home
 * indicator and the toast slot above it. */
const DIAMETER = 56;
const EDGE = 16;

/** How much room the floating button takes at the Board's end: its diameter,
 * its edge, and the home indicator it clears. The Board pads its end by this
 * and floats the toast above it. */
export function composeButtonRoom(bottomInset: number): number {
	return DIAMETER + EDGE + bottomInset;
}

export function ComposeButton({ disabled, onPress }: { disabled: boolean; onPress: () => void }) {
	const { palette } = useColors();
	const { bottom } = useSafeAreaInsets();
	return (
		<Pressable
			accessibilityRole="button"
			accessibilityLabel="New session"
			accessibilityState={{ disabled }}
			disabled={disabled}
			onPress={disabled ? undefined : onPress}
			style={({ pressed }) => ({
				position: "absolute",
				right: EDGE,
				bottom: EDGE + bottom,
				width: DIAMETER,
				height: DIAMETER,
				borderRadius: DIAMETER / 2,
				alignItems: "center",
				justifyContent: "center",
				backgroundColor: palette.accentFill,
				opacity: disabled ? 0.4 : pressed ? 0.6 : 1,
				...FLOATING_SHADOW,
			})}
		>
			<SymbolView name="square.and.pencil" size={24} tintColor={palette.onFill} />
		</Pressable>
	);
}
