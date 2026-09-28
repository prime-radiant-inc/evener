// One block of a document in the Reader (spec 10.2): markdown in the document
// role with serif headings, code in the machine face on an inset box, a wide
// table in its own horizontal scroll, a rule as a line. A block that changed
// since you last read it carries an accent rule down its left edge.
import { memo } from "react";
import { ScrollView, Text, View } from "react-native";
import { EnrichedMarkdownText, type MarkdownStyle } from "react-native-enriched-markdown";
import { fonts, typeRoles } from "../design/tokens";
import { openLink, showLink } from "../MarkdownResponse";
import { type MarkdownRoles, markdownStyle } from "../markdownStyle";
import { allowFontScaling, useColors, useTextScale } from "../ui";
import type { DocumentBlock } from "./documentBlocks";

const serifHeading = (fontSize: number, lineHeight: number) => ({
	fontFamily: fonts.serifSemibold,
	fontSize,
	lineHeight,
	fontWeight: "600",
});

const READER_ROLES: MarkdownRoles = {
	body: typeRoles.document,
	headings: [serifHeading(24, 30), serifHeading(20, 26), serifHeading(18, 24)],
};

/** The styles a block draws with that carry margins of their own. */
const FLUSH = ["paragraph", "h1", "h2", "h3", "h4", "h5", "h6", "list", "blockquote", "table"] as const;

// One style per palette, shared by every block: a document can have hundreds.
const styles = new WeakMap<object, MarkdownStyle>();
function readerStyle(colors: ReturnType<typeof useColors>): MarkdownStyle {
	let style = styles.get(colors.palette);
	if (!style) {
		// A block is drawn on its own, so the 14pt gap between blocks is the
		// list's, not the markdown's margins.
		const built = markdownStyle(colors, READER_ROLES);
		style = { ...built };
		for (const name of FLUSH) style[name] = { ...built[name], marginTop: 0, marginBottom: 0 };
		styles.set(colors.palette, style);
	}
	return style;
}

function Markdown({ markdown }: { markdown: string }) {
	const colors = useColors();
	return (
		<EnrichedMarkdownText
			markdown={markdown}
			markdownStyle={readerStyle(colors)}
			flavor="github"
			allowFontScaling
			enableTaskListItemToggle={false}
			streamingAnimation={false}
			onLinkPress={({ url }) => {
				void openLink(url);
			}}
			onLinkLongPress={({ url }) => showLink(url)}
		/>
	);
}

/** Menlo 13/18 in ink-hi: a code block's text, and a code file's lines. */
export function useCodeText() {
	const { palette } = useColors();
	const scale = useTextScale();
	return { fontFamily: fonts.mono, fontSize: 13 * scale, lineHeight: 18 * scale, color: palette.inkHi };
}

function BlockBody({ block }: { block: DocumentBlock }) {
	const { palette } = useColors();
	const code = useCodeText();
	switch (block.kind) {
		case "code":
			return (
				<View style={{ backgroundColor: palette.inset, borderRadius: 12 }}>
					<ScrollView horizontal contentContainerStyle={{ paddingVertical: 12, paddingHorizontal: 14 }}>
						<Text allowFontScaling={allowFontScaling} selectable style={code}>
							{block.code?.text ?? block.text}
						</Text>
					</ScrollView>
				</View>
			);
		case "table":
			return (
				<ScrollView horizontal>
					<Markdown markdown={block.markdown} />
				</ScrollView>
			);
		case "rule":
			return <View style={{ height: 1, marginVertical: 16, backgroundColor: palette.edge }} />;
		default:
			return <Markdown markdown={block.markdown} />;
	}
}

export const ReaderBlock = memo(function ReaderBlock({ block, changed }: { block: DocumentBlock; changed: boolean }) {
	const { palette } = useColors();
	return (
		<View
			style={
				changed
					? { borderLeftWidth: 3, borderLeftColor: palette.accent, paddingLeft: 12 }
					: undefined
			}
		>
			<BlockBody block={block} />
		</View>
	);
});
