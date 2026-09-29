// The notes bar (spec 8.8): one 32pt line under the context chips that
// previews the session's shared notes and links. It sits in SessionHeader's
// sliding row, so it hides with the chips; the whole bar opens the Notes &
// links sheet.
import { type SFSymbol, SymbolView } from "expo-symbols";
import { Pressable, Text } from "react-native";
import { allowFontScaling, useColors, useTextScale } from "../ui";
import { headerRowFill } from "./headerGlass";
import type { NotesBarPreview, NotesGlyph } from "./sessionNotes";

const SYMBOLS: Record<NotesGlyph, SFSymbol> = {
	person: "person",
	sparkles: "sparkles",
	link: "link",
};

export function NotesBar({
	preview,
	onPress,
	onGlass = false,
}: {
	preview: NotesBarPreview;
	onPress: () => void;
	/** It sits on the header's glass, and draws clear on it. */
	onGlass?: boolean;
}) {
	const { palette } = useColors();
	const scale = useTextScale();
	return (
		<Pressable
			accessibilityRole="button"
			accessibilityLabel={preview.links ? `${preview.text}, ${preview.links}` : preview.text}
			onPress={onPress}
			// 32pt tall, 44pt to the finger.
			hitSlop={{ top: 6, bottom: 6 }}
			style={({ pressed }) => ({
				height: 32,
				flexDirection: "row",
				alignItems: "center",
				gap: 6,
				paddingHorizontal: 16,
				backgroundColor: headerRowFill(onGlass, palette),
				opacity: pressed ? 0.6 : 1,
			})}
		>
			<SymbolView name={SYMBOLS[preview.glyph]} size={13 * scale} tintColor={palette.inkMid} />
			<Text
				allowFontScaling={allowFontScaling}
				numberOfLines={1}
				ellipsizeMode="tail"
				style={{ flex: 1, fontSize: 15 * scale, lineHeight: 20 * scale, color: palette.inkHi }}
			>
				{preview.text}
			</Text>
			{preview.links ? (
				<Text
					allowFontScaling={allowFontScaling}
					numberOfLines={1}
					style={{ fontSize: 13 * scale, lineHeight: 18 * scale, color: palette.inkMid, fontVariant: ["tabular-nums"] }}
				>
					{preview.links}
				</Text>
			) : null}
		</Pressable>
	);
}
