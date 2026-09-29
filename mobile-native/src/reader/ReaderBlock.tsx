// One block of a document in the Reader (spec 10.2): markdown in the document
// role and the phone's reading font, code in the machine face on an inset box,
// a table as the markdown view draws it (it scrolls a wide one sideways
// itself), a rule as a line. A block that changed since you last read it
// carries an accent rule down its left edge. Touch and hold opens its menu
// (comment, quote, copy, select), and a pill at its top trailing corner counts
// the comments on it.
import { SymbolView } from "expo-symbols";
import { memo, useMemo } from "react";
import { Pressable, ScrollView, Text, View } from "react-native";
import { EnrichedMarkdownText, type MarkdownStyle } from "react-native-enriched-markdown";
import { fonts } from "../design/tokens";
import { readingRoles, useDisplayChoices } from "../display/displayContext";
import type { ReadingFont } from "../display/displayPreferences";
import { type MenuItem, menuAccessibility, menuPreview, showMenu } from "../longPressMenu";
import { openLink, showLink } from "../MarkdownResponse";
import { type MarkdownRoles, markdownStyle } from "../markdownStyle";
import { allowFontScaling, useColors, useTextScale } from "../ui";
import type { DocumentBlock } from "./documentBlocks";

// A document reads in the phone's reading font (spec 12): the serif, its
// headings in the serif's semibold, or under Sans the system face for both.
function readerRoles(font: ReadingFont): MarkdownRoles {
	const heading = (fontSize: number, lineHeight: number) => ({
		...(font === "serif" ? { fontFamily: fonts.serifSemibold } : {}),
		fontSize,
		lineHeight,
		fontWeight: "600" as const,
	});
	return {
		body: readingRoles(font).document,
		headings: [heading(24, 30), heading(20, 26), heading(18, 24)],
	};
}

/** The styles a block draws with that carry margins of their own. */
const FLUSH = ["paragraph", "h1", "h2", "h3", "h4", "h5", "h6", "list", "blockquote", "table"] as const;

// One style per palette and reading font, shared by every block: a document
// can have hundreds.
const styles = new WeakMap<object, Partial<Record<ReadingFont, MarkdownStyle>>>();
function readerStyle(colors: ReturnType<typeof useColors>, font: ReadingFont): MarkdownStyle {
	const perFont = styles.get(colors.palette) ?? {};
	let style = perFont[font];
	if (!style) {
		// A block is drawn on its own, so the 14pt gap between blocks is the
		// list's, not the markdown's margins.
		const built = markdownStyle(colors, readerRoles(font));
		style = { ...built };
		for (const name of FLUSH) style[name] = { ...built[name], marginTop: 0, marginBottom: 0 };
		perFont[font] = style;
		styles.set(colors.palette, perFont);
	}
	return style;
}

type Accessibility = ReturnType<typeof menuAccessibility>;

function Markdown({
	markdown,
	selecting,
	onSelection,
	accessibility,
}: {
	markdown: string;
	selecting: boolean;
	onSelection?: (action: "comment" | "quote", words: string) => void;
	accessibility?: Accessibility;
}) {
	const colors = useColors();
	const { readingFont } = useDisplayChoices();
	return (
		<EnrichedMarkdownText
			markdown={markdown}
			markdownStyle={readerStyle(colors, readingFont)}
			flavor="github"
			// Touch and hold belongs to the block's menu until you choose
			// Select text; then the system's selection takes it.
			selectable={selecting}
			allowFontScaling
			enableTaskListItemToggle={false}
			streamingAnimation={false}
			onLinkPress={({ url }) => {
				void openLink(url);
			}}
			onLinkLongPress={({ url }) => showLink(url)}
			contextMenuItems={
				selecting && onSelection
					? [
							{ text: "Comment", onPress: ({ text }) => onSelection("comment", text) },
							{ text: "Quote in reply", onPress: ({ text }) => onSelection("quote", text) },
						]
					: undefined
			}
			{...accessibility}
		/>
	);
}

/** Menlo 13/18 in ink-hi: a code block's text, and a code file's lines. */
export function useCodeText() {
	const { palette } = useColors();
	const scale = useTextScale();
	return { fontFamily: fonts.mono, fontSize: 13 * scale, lineHeight: 18 * scale, color: palette.inkHi };
}

function BlockBody({
	block,
	selecting,
	onSelection,
	accessibility,
}: {
	block: DocumentBlock;
	selecting: boolean;
	onSelection?: (action: "comment" | "quote", words: string) => void;
	accessibility?: Accessibility;
}) {
	const { palette } = useColors();
	const code = useCodeText();
	const markdown = (
		<Markdown markdown={block.markdown} selecting={selecting} onSelection={onSelection} accessibility={accessibility} />
	);
	switch (block.kind) {
		case "code":
			return (
				<View style={{ backgroundColor: palette.inset, borderRadius: 12 }}>
					<ScrollView horizontal contentContainerStyle={{ paddingVertical: 12, paddingHorizontal: 14 }}>
						<Text allowFontScaling={allowFontScaling} selectable={selecting} style={code} {...accessibility}>
							{block.code?.text ?? block.text}
						</Text>
					</ScrollView>
				</View>
			);
		case "rule":
			return <View style={{ height: 1, marginVertical: 16, backgroundColor: palette.edge }} />;
		default:
			return markdown;
	}
}

/** What a block's menu, its selection menu and its marker ask of the Reader. */
export type BlockAction = "comment" | "quote" | "copy" | "select" | "comments";

export interface ReaderBlockProps {
	block: DocumentBlock;
	/** Changed since you last read it: an accent rule down its left edge. */
	changed: boolean;
	/** Its menu is open: the block under your finger is highlighted. */
	selected?: boolean;
	/** Select text chose it: its words take the system's selection. */
	selecting?: boolean;
	/** The comments whose marker sits on it. */
	commentCount?: number;
	/** `words` is the selection, for Comment and Quote in reply on selected text. */
	onAction?: (action: BlockAction, block: DocumentBlock, words?: string) => void;
	/** The block's menu opened (its index) or closed (null). */
	onMenu?: (index: number | null) => void;
	/** A tap on the block, which ends a selection. */
	onTap?: () => void;
}

export const ReaderBlock = memo(function ReaderBlock({
	block,
	changed,
	selected = false,
	selecting = false,
	commentCount = 0,
	onAction,
	onMenu,
	onTap,
}: ReaderBlockProps) {
	const { palette } = useColors();
	const menu = useMemo<MenuItem[]>(
		() =>
			onAction && block.kind !== "rule"
				? [
						{ name: "comment", label: "Comment", run: () => onAction("comment", block) },
						{ name: "quote", label: "Quote in reply", run: () => onAction("quote", block) },
						{ name: "copy", label: "Copy", run: () => onAction("copy", block) },
						{ name: "select", label: "Select text", run: () => onAction("select", block) },
					]
				: [],
		[block, onAction],
	);
	const accessibility = useMemo(() => (menu.length > 0 ? menuAccessibility(menu) : undefined), [menu]);
	const onSelection = useMemo(
		() => (onAction ? (action: "comment" | "quote", words: string) => onAction(action, block, words) : undefined),
		[block, onAction],
	);
	const scale = useTextScale();
	const style = {
		...(changed ? { borderLeftWidth: 3, borderLeftColor: palette.accent, paddingLeft: 12 } : {}),
		...(selected ? { backgroundColor: palette.accentBg } : {}),
	};
	const body = (
		<BlockBody block={block} selecting={selecting} onSelection={onSelection} accessibility={accessibility} />
	);
	// A rule carries no menu of its own, but a tap on it still ends a
	// selection, as a tap on any other block does.
	if (menu.length === 0)
		return onTap ? (
			<Pressable testID={`block-${block.index}`} accessible={false} onPress={onTap} style={style}>
				{body}
			</Pressable>
		) : (
			<View style={style}>{body}</View>
		);
	return (
		// The words stay VoiceOver's element, with the menu as its actions;
		// the pressable only adds touch and hold.
		<Pressable
			testID={`block-${block.index}`}
			accessible={false}
			onPress={onTap}
			onLongPress={() => {
				onMenu?.(block.index);
				showMenu(menu, menuPreview(block.text), () => onMenu?.(null));
			}}
			// Room for the comment pill at the top trailing corner, so it never
			// sits on the block's words.
			style={commentCount > 0 ? { ...style, paddingRight: 48 * scale } : style}
		>
			{body}
			{commentCount > 0 ? <CommentMarker count={commentCount} onPress={() => onAction?.("comments", block)} /> : null}
		</Pressable>
	);
});

/** The pill at a block's top trailing corner: how many comments sit on it. */
function CommentMarker({ count, onPress }: { count: number; onPress(): void }) {
	const { palette } = useColors();
	const scale = useTextScale();
	return (
		<Pressable
			accessibilityRole="button"
			accessibilityLabel={`${count} ${count === 1 ? "comment" : "comments"}`}
			onPress={onPress}
			// The pill is small; its touch area is 44pt.
			hitSlop={12}
			style={{
				position: "absolute",
				top: 0,
				right: 0,
				flexDirection: "row",
				alignItems: "center",
				gap: 4,
				paddingHorizontal: 8,
				paddingVertical: 4,
				borderRadius: 10,
				backgroundColor: palette.accentBg,
			}}
		>
			<SymbolView name="bubble.left" size={12} tintColor={palette.accentInk} />
			<Text
				allowFontScaling={allowFontScaling}
				style={{ color: palette.accentInk, fontSize: 12 * scale, lineHeight: 16 * scale, fontWeight: "600" }}
			>
				{count}
			</Text>
		</Pressable>
	);
}
