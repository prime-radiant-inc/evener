// A comment on a document (spec 10.2, ruling 26): the words it is on, and
// what should change. It stays with the document on this phone until the
// review goes out. A sheet that opens at full height, since you type in it.
import type { NativeStackScreenProps } from "@react-navigation/native-stack";
import { useState } from "react";
import { ScrollView, Text, TextInput, View } from "react-native";
import type { Routes } from "../screens";
import { Sheet } from "../sheet/Sheet";
import { useSheet } from "../sheet/useSheet";
import { allowFontScaling, useColors, useTextScale } from "../ui";
import { useReadingFace } from "../display/displayContext";
import { documentMemory } from "./nativeDocumentMemory";

/** How much of the quoted words the sheet shows. */
const QUOTE_SHOWN = 280;

export function CommentSheet({ route }: NativeStackScreenProps<Routes, "CommentSheet">) {
	const { hubId, sessionRef, path, blockIndex, blockHash, quote } = route.params;
	const [text, setText] = useState("");
	const blank = text.trim() === "";
	const sheet = useSheet({ dirty: !blank, discardTitle: "Discard this comment?" });
	const { palette } = useColors();
	const scale = useTextScale();
	const add = () => {
		documentMemory(hubId).addComment({ sessionRef, path }, { blockIndex, blockHash, quote, text: text.trim() });
		sheet.finish();
	};
	return (
		<Sheet title="Comment" onCancel={sheet.close} done={{ label: "Add", disabled: blank, onPress: add }}>
			<ScrollView
				automaticallyAdjustKeyboardInsets
				keyboardDismissMode="on-drag"
				contentContainerStyle={{ padding: 16, gap: 16 }}
			>
				<QuoteBlock words={quote} limit={QUOTE_SHOWN} size={15} lineHeight={21} />
				<TextInput
					accessibilityLabel="Comment"
					autoFocus
					multiline
					placeholder="What should change?"
					placeholderTextColor={palette.inkLow}
					value={text}
					onChangeText={setText}
					allowFontScaling={allowFontScaling}
					style={{
						minHeight: 120,
						color: palette.inkHi,
						fontSize: 17 * scale,
						lineHeight: 22 * scale,
						textAlignVertical: "top",
					}}
				/>
				<Text
					allowFontScaling={allowFontScaling}
					style={{ color: palette.inkMid, fontSize: 13 * scale, lineHeight: 18 * scale }}
				>
					Comments stay with this document until you send your review.
				</Text>
			</ScrollView>
		</Sheet>
	);
}

/** Quoted words from a document, in the reading serif behind a left rule:
 * the comment sheet, the comments list and the review all show them. */
export function QuoteBlock({
	words,
	limit,
	size,
	lineHeight,
}: {
	words: string;
	limit: number;
	size: number;
	lineHeight: number;
}) {
	const { palette } = useColors();
	const scale = useTextScale();
	const face = useReadingFace();
	const shown = words.length > limit ? `${words.slice(0, limit).trimEnd()}…` : words;
	return (
		<View style={{ borderLeftWidth: 2, borderLeftColor: palette.edgeStrong, paddingLeft: 10 }}>
			<Text
				allowFontScaling={allowFontScaling}
				style={{
					...face.regular,
					color: palette.inkMid,
					fontSize: size * scale,
					lineHeight: lineHeight * scale,
				}}
			>
				{shown}
			</Text>
		</View>
	);
}
