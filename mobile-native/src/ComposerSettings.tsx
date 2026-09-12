import {
	ActivityIndicator,
	Platform,
	Pressable,
	Text,
	useWindowDimensions,
	View,
} from "react-native";
import {
	effortLabel,
	sessionEffortLevels,
} from "../../cmd/evener-hub/frontend/src/shell/reasoningEffort";
import type { MobileConversation } from "../../mobile/src/conversation/model";
import { useColors } from "./ui";

export type ComposerSetting = "model" | "reasoning" | "vision";

/** Visible session choices share the input footer with the send action. */
export function ComposerSettings({
	conversation,
	disabled,
	pending,
	fullWidth = false,
	open,
}: {
	conversation: MobileConversation;
	disabled: boolean;
	pending: boolean;
	fullWidth?: boolean;
	open: (setting: ComposerSetting) => void;
}) {
	const colors = useColors();
	const { fontScale } = useWindowDimensions();
	const scale = Platform.OS === "ios" ? fontScale : 1;
	const levels = sessionEffortLevels(
		conversation.reasoningEffortLevels,
		conversation.supportsReasoning,
	);
	const reasoning = levels.length > 0;
	const currentEffort = effortLabel(conversation.reasoningEffort ?? "", levels);
	const vision = conversation.capabilities.changeVisionModel;
	return (
		<View
			style={{
				flexDirection: "row",
				alignItems: "center",
				flex: fullWidth || fontScale > 1.4 ? 0 : 1,
				flexShrink: 1,
				minWidth: fullWidth || fontScale > 1.4 ? "100%" : 120,
				gap: 8,
			}}
		>
			<Pressable
				accessibilityRole="button"
				accessibilityLabel={`Model: ${conversation.modelProvider}. Change model`}
				accessibilityState={{
					disabled: disabled || !conversation.capabilities.changeModel,
				}}
				disabled={disabled || !conversation.capabilities.changeModel}
				onPress={() => open("model")}
				style={({ pressed }) => ({
					minHeight: Platform.OS === "ios" ? 44 : 48,
					flexDirection: "row",
					alignItems: "center",
					flexShrink: 1,
					gap: 4,
					opacity: pressed ? 0.6 : 1,
				})}
			>
				<Text
					numberOfLines={1}
					allowFontScaling={Platform.OS !== "ios"}
					style={{
						fontSize: 13 * scale,
						color: colors.secondary,
						flexShrink: 1,
					}}
				>
					{conversation.modelProvider || "Model"}
				</Text>
				{conversation.capabilities.changeModel ? (
					<Text
						allowFontScaling={false}
						style={{ fontSize: 12, color: colors.secondary }}
					>
						⌄
					</Text>
				) : null}
			</Pressable>
			{reasoning ? (
				<Pressable
					accessibilityRole="button"
					accessibilityLabel={`Reasoning effort: ${currentEffort}. Change reasoning effort`}
					accessibilityState={{ disabled }}
					disabled={disabled}
					onPress={() => open("reasoning")}
					style={({ pressed }) => ({
						minHeight: Platform.OS === "ios" ? 44 : 48,
						flexDirection: "row",
						alignItems: "center",
						gap: 4,
						opacity: pressed ? 0.6 : 1,
					})}
				>
					<Text
						allowFontScaling={Platform.OS !== "ios"}
						style={{ fontSize: 13 * scale, color: colors.secondary }}
					>
						{currentEffort}
					</Text>
					<Text
						allowFontScaling={false}
						style={{ fontSize: 12, color: colors.secondary }}
					>
						⌄
					</Text>
				</Pressable>
			) : null}
			{vision ? (
				<Pressable
					accessibilityRole="button"
					accessibilityLabel={`Vision model: ${conversation.visionModel || "session model"}. Change vision model`}
					accessibilityState={{ disabled }}
					disabled={disabled}
					onPress={() => open("vision")}
					style={({ pressed }) => ({
						minHeight: Platform.OS === "ios" ? 44 : 48,
						flexDirection: "row",
						alignItems: "center",
						gap: 4,
						opacity: pressed ? 0.6 : 1,
					})}
				>
					<Text
						allowFontScaling={Platform.OS !== "ios"}
						style={{ fontSize: 13 * scale, color: colors.secondary }}
					>
						{conversation.visionModel === "off"
							? "Vision off"
							: conversation.visionModel || "Vision"}
					</Text>
					<Text
						allowFontScaling={false}
						style={{ fontSize: 12, color: colors.secondary }}
					>
						⌄
					</Text>
				</Pressable>
			) : null}
			{pending ? (
				<ActivityIndicator
					accessibilityLabel="Updating model settings"
					color={colors.accent}
					size="small"
				/>
			) : null}
		</View>
	);
}
