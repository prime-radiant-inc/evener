// A sheet's header action (Done, Cancel, Save): accent ink, semibold for the
// action that finishes the page, dimmed and inert while it can't run.
import { Pressable, Text } from "react-native";
import { useColors } from "../ui";

export function HeaderButton({
	label,
	onPress,
	emphasized = false,
	disabled = false,
}: {
	label: string;
	onPress(): void;
	emphasized?: boolean;
	disabled?: boolean;
}) {
	const { palette } = useColors();
	return (
		<Pressable
			accessibilityRole="button"
			accessibilityLabel={label}
			accessibilityState={{ disabled }}
			disabled={disabled}
			hitSlop={8}
			onPress={onPress}
		>
			<Text
				style={{
					color: palette.accentInk,
					fontSize: 17,
					fontWeight: emphasized ? "600" : "400",
					opacity: disabled ? 0.4 : 1,
				}}
			>
				{label}
			</Text>
		</Pressable>
	);
}
