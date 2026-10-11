// A 44pt button that shows a symbol where a word would be, so its label is
// what VoiceOver reads. A disabled one dims and tells VoiceOver it is
// disabled. The tray's Stop and the composer's +, Send and Expand editor.
import type { ReactNode } from "react";
import { Pressable, type ViewStyle } from "react-native";

export function SymbolButton({
	label,
	disabled = false,
	onPress,
	style,
	children,
}: {
	label: string;
	disabled?: boolean;
	onPress(): void;
	/** Where the button sits. Its size, centering and opacity are its own. */
	style?: ViewStyle;
	children: ReactNode;
}) {
	return (
		<Pressable
			accessibilityRole="button"
			accessibilityLabel={label}
			accessibilityState={{ disabled }}
			disabled={disabled}
			onPress={onPress}
			style={({ pressed }) => ({
				...style,
				width: 44,
				height: 44,
				alignItems: "center",
				justifyContent: "center",
				opacity: disabled ? 0.4 : pressed ? 0.6 : 1,
			})}
		>
			{children}
		</Pressable>
	);
}
