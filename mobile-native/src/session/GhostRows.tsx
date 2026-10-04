// Everything waiting to reach the agent, as the transcript's last rows (spec
// 8.5 and 14): the ghosts `ghosts()` lists, at most three of them queued. The
// rest of the queue is one quiet row that opens the Queue sheet (ruling 18).
import { SymbolView } from "expo-symbols";
import { createContext, type ReactNode, useContext } from "react";
import { Pressable, Text } from "react-native";
import { allowFontScaling, useColors, useTextScale } from "../ui";
import { GhostBubble } from "./GhostBubble";
import type { Ghost, GhostAction } from "./ghosts";
import type { GhostRow } from "./transcriptRows";

/** What a ghost row of the transcript needs from the screen besides the row.
 * It comes by context, a new value on each screen render, which re-renders
 * the ghost rows alone: the list keeps one renderItem while nothing a
 * transcript row reads changes, and its other cells stay as they are
 * (#3247). */
export interface GhostRowHost {
	disabled: boolean;
	canEdit: boolean;
	editHint: string | null;
	/** The images an unconfirmed send carried, shown in its bubble. */
	draftAttachments?: ReactNode;
	onAction(ghost: Ghost, action: GhostAction): void;
	onMore(): void;
}

export const GhostRowContext = createContext<GhostRowHost | null>(null);

/** One of the transcript's ghost rows (spec 8.5): a ghost on the page, or
 * the quiet row that opens the Queue sheet (ruling 18). */
export function GhostRowView({ row }: { row: GhostRow }) {
	const host = useContext(GhostRowContext);
	if (host === null) throw new Error("A ghost row renders inside a GhostRowContext provider.");
	if (row.kind === "moreQueued") return <MoreQueued count={row.count} onPress={host.onMore} />;
	const { ghost } = row;
	return (
		<GhostBubble
			ghost={ghost}
			disabled={host.disabled}
			canEdit={host.canEdit}
			editHint={host.editHint}
			backdrop="page"
			attachments={ghost.origin.kind === "draft" ? host.draftAttachments : undefined}
			onAction={(action) => host.onAction(ghost, action)}
		/>
	);
}

/** The queued messages past the three that show, counted: "2 more queued",
 * which opens the Queue sheet (ruling 18). */
function MoreQueued({ count, onPress }: { count: number; onPress(): void }) {
	const { palette } = useColors();
	const scale = useTextScale();
	const more = `${count} more queued`;
	return (
		<Pressable
			accessibilityRole="button"
			accessibilityLabel={more}
			onPress={onPress}
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
	);
}
