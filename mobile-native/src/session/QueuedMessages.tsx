// Everything waiting to reach the agent, above the composer (spec 8.5 and
// 14): the ghosts `ghosts()` lists, at most three of them queued. The rest of
// the queue is one quiet row that opens the Queue sheet (ruling 18).
import { SymbolView } from "expo-symbols";
import { Platform, Pressable, Text, useWindowDimensions, View } from "react-native";
import { useColors } from "../ui";
import { GhostBubble } from "./GhostBubble";
import { type Ghost, type GhostAction, shownGhosts } from "./ghosts";

export interface QueuedMessagesProps {
	ghosts: readonly Ghost[];
	disabled: boolean;
	canEdit: boolean;
	editHint: string | null;
	onAction(ghost: Ghost, action: GhostAction): void;
	onMore(): void;
}

export function QueuedMessages({ ghosts, disabled, canEdit, editHint, onAction, onMore }: QueuedMessagesProps) {
	const { palette } = useColors();
	const { fontScale } = useWindowDimensions();
	const scale = Platform.OS === "ios" ? fontScale : 1;
	if (ghosts.length === 0) return null;
	const { shown, moreQueued } = shownGhosts(ghosts);
	const more = `${moreQueued} more queued`;
	return (
		<View style={{ gap: 8 }}>
			{shown.map((ghost) => (
				<GhostBubble
					key={ghost.key}
					ghost={ghost}
					disabled={disabled}
					canEdit={canEdit}
					editHint={editHint}
					onAction={(action) => onAction(ghost, action)}
				/>
			))}
			{moreQueued > 0 ? (
				<Pressable
					accessibilityRole="button"
					accessibilityLabel={more}
					onPress={onMore}
					style={{ alignSelf: "flex-end", minHeight: 44, flexDirection: "row", alignItems: "center", gap: 4 }}
				>
					<Text
						allowFontScaling={Platform.OS !== "ios"}
						style={{ fontSize: 13 * scale, lineHeight: 18 * scale, color: palette.inkLow, fontVariant: ["tabular-nums"] }}
					>
						{more}
					</Text>
					<SymbolView name="chevron.right" tintColor={palette.inkLow} size={11 * scale} />
				</Pressable>
			) : null}
		</View>
	);
}
