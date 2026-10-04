// Everything waiting to reach the agent, as the transcript's last rows (spec
// 8.5 and 14): the ghosts `ghosts()` lists, at most three of them queued. The
// rest of the queue is one quiet row that opens the Queue sheet (ruling 18).
import { SymbolView } from "expo-symbols";
import { createContext, type ReactNode, useContext, useState } from "react";
import { Pressable, Text, View } from "react-native";
import { allowFontScaling, useColors, useTextScale } from "../ui";
import { useComposerTyping } from "../useKeyboardShown";
import { type GhostBackdrop, GhostBubble, GhostButton } from "./GhostBubble";
import type { ComposerFocus } from "./composerFocus";
import { foldQueue, type Ghost, type GhostAction, type QueueFold, shownGhosts } from "./ghosts";
import type { GhostRow } from "./transcriptRows";

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
	/** The composer's focus: while you type in it (useComposerTyping), the
	 * queue folds to one line until you tap it open. */
	composerFocus: ComposerFocus;
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
	composerFocus,
	onAction,
	onMore,
}: QueuedMessagesProps) {
	const typing = useComposerTyping(composerFocus);
	// Opened while typing; the next time you type, the queue folds again.
	const [opened, setOpened] = useState(false);
	if (!typing && opened) setOpened(false);
	if (ghosts.length === 0) return null;
	const { fold, rest } = typing && !opened ? foldQueue(ghosts) : { fold: null, rest: ghosts };
	const { shown, moreQueued } = fold ? { shown: rest, moreQueued: 0 } : shownGhosts(ghosts);
	return (
		<View style={{ gap: 8 }}>
			{fold ? <FoldedQueue fold={fold} disabled={disabled} onAction={onAction} onOpen={() => setOpened(true)} /> : null}
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
			{moreQueued > 0 ? <MoreQueued count={moreQueued} onPress={onMore} /> : null}
		</View>
	);
}

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

/** The queue as one line while you type: its count, which shows the
 * messages, and with one, its action, as "1 queued · Steer now". */
function FoldedQueue({
	fold,
	disabled,
	onAction,
	onOpen,
}: {
	fold: QueueFold;
	disabled: boolean;
	onAction(ghost: Ghost, action: GhostAction): void;
	onOpen(): void;
}) {
	const { palette } = useColors();
	const scale = useTextScale();
	const { label, act } = fold;
	const text = { fontSize: 15 * scale, lineHeight: 20 * scale, color: palette.inkLow } as const;
	return (
		<View style={{ flexDirection: "row", alignItems: "center", justifyContent: "flex-end" }}>
			<Pressable
				accessibilityRole="button"
				accessibilityLabel={label}
				accessibilityHint="Shows the queued messages"
				onPress={onOpen}
				style={({ pressed }) => ({
					minHeight: 44,
					paddingHorizontal: 8,
					justifyContent: "center",
					opacity: pressed ? 0.6 : 1,
				})}
			>
				<Text allowFontScaling={allowFontScaling} style={[text, { fontVariant: ["tabular-nums"] }]}>
					{label}
				</Text>
			</Pressable>
			{act ? (
				<>
					<Text
						allowFontScaling={allowFontScaling}
						style={text}
						accessibilityElementsHidden
						importantForAccessibility="no"
					>
						·
					</Text>
					<GhostButton
						action={act.action}
						subject={act.ghost.text}
						disabled={disabled}
						onPress={() => onAction(act.ghost, act.action)}
					/>
				</>
			) : null}
		</View>
	);
}
