// Everything waiting to reach the agent, above the composer (spec 8.5 and
// 14): the ghosts `ghosts()` lists, at most three of them queued. The rest of
// the queue is one quiet row that opens the Queue sheet (ruling 18).
import { SymbolView } from "expo-symbols";
import type { ReactNode } from "react";
import { Pressable, Text, View } from "react-native";
import { allowFontScaling, useColors, useTextScale } from "../ui";
import { type GhostBackdrop, GhostBubble } from "./GhostBubble";
import { type Ghost, type GhostAction, shownGhosts } from "./ghosts";

export interface QueuedMessagesProps {
	ghosts: readonly Ghost[];
	disabled: boolean;
	canEdit: boolean;
	editHint: string | null;
	/** What the ghosts sit on: the composer, or the page while the dock
	 * takes the composer's place. */
	backdrop: GhostBackdrop;
	/** The images an unconfirmed send carried, shown in its bubble. */
	draftAttachments?: ReactNode;
	onAction(ghost: Ghost, action: GhostAction): void;
	onMore(): void;
}

export function QueuedMessages({
	ghosts,
	disabled,
	canEdit,
	editHint,
	backdrop,
	draftAttachments,
	onAction,
	onMore,
}: QueuedMessagesProps) {
	const { palette } = useColors();
	const scale = useTextScale();
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
					backdrop={backdrop}
					attachments={ghost.origin.kind === "draft" ? draftAttachments : undefined}
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
						allowFontScaling={allowFontScaling}
						style={{
							fontSize: 13 * scale,
							lineHeight: 18 * scale,
							color: palette.inkLow,
							fontVariant: ["tabular-nums"],
						}}
					>
						{more}
					</Text>
					<SymbolView name="chevron.right" tintColor={palette.inkLow} size={11 * scale} />
				</Pressable>
			) : null}
		</View>
	);
}
