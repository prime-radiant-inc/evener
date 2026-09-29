// The find bar (spec 8.7; ruling 29). "Find in session" in the ⋯ menu puts it
// where the context chips sit: a field, where you are among the matches, a
// step to the older and the newer match, and Done. The screen owns the search
// (findInSession.ts) and moves the list.
import { SymbolView } from "expo-symbols";
import { useEffect, useRef } from "react";
import { AccessibilityInfo, Pressable, Text, TextInput, View } from "react-native";
import { allowFontScaling, searchFieldStyle, useColors, useTextScale } from "../ui";
import { headerRowFill } from "../design/systemGlass";
import { SymbolButton } from "./SymbolButton";

export function FindBar({
	query,
	label,
	searchingOlder,
	settled,
	onQuery,
	onStep,
	onDone,
	onGlass = false,
}: {
	query: string;
	/** Where you are among the matches: "2 of 3", "No matches". */
	label: string;
	/** An older page is loading to look for a match in it. */
	searchingOlder: boolean;
	/** The label is the search's answer, not a step on the way to it. */
	settled: boolean;
	onQuery(query: string): void;
	onStep(direction: 1 | -1): void;
	onDone(): void;
	/** It sits on the header's glass, and draws clear on it. */
	onGlass?: boolean;
}) {
	const { palette } = useColors();
	const scale = useTextScale();
	const canStep = query.trim() !== "" && !searchingOlder;
	const shown = searchingOlder ? "Searching older messages…" : label;
	// VoiceOver hears where you are once a step or search settles, and that
	// older messages are loading while they do; what the label passes through
	// on the way stays quiet. An announcement, since iOS ignores
	// accessibilityLiveRegion, which only Android reads.
	const announced = useRef("");
	useEffect(() => {
		if (!shown || shown === announced.current || !(settled || searchingOlder)) return;
		announced.current = shown;
		AccessibilityInfo.announceForAccessibility(shown);
	}, [shown, settled, searchingOlder]);
	return (
		<View
			style={{
				flexDirection: "row",
				alignItems: "center",
				gap: 4,
				paddingLeft: 16,
				paddingRight: 8,
				paddingVertical: 4,
				backgroundColor: headerRowFill(onGlass, palette),
			}}
		>
			<TextInput
				accessibilityLabel="Find in session"
				placeholder="Find in session"
				placeholderTextColor={palette.inkLow}
				value={query}
				onChangeText={onQuery}
				autoFocus
				autoCapitalize="none"
				autoCorrect={false}
				returnKeyType="search"
				clearButtonMode="while-editing"
				allowFontScaling={allowFontScaling}
				style={{ flex: 1, ...searchFieldStyle(palette, scale) }}
			/>
			<Text
				allowFontScaling={allowFontScaling}
				numberOfLines={1}
				style={{
					flexShrink: 1,
					color: palette.inkMid,
					fontSize: 13 * scale,
					lineHeight: 18 * scale,
					fontVariant: ["tabular-nums"],
				}}
			>
				{shown}
			</Text>
			<SymbolButton label="Older match" disabled={!canStep} onPress={() => onStep(-1)}>
				<SymbolView name="chevron.up" tintColor={palette.accentInk} size={17 * scale} />
			</SymbolButton>
			<SymbolButton label="Newer match" disabled={!canStep} onPress={() => onStep(1)}>
				<SymbolView name="chevron.down" tintColor={palette.accentInk} size={17 * scale} />
			</SymbolButton>
			<Pressable
				accessibilityRole="button"
				accessibilityLabel="Done"
				onPress={onDone}
				hitSlop={8}
				style={({ pressed }) => ({
					minHeight: 44,
					justifyContent: "center",
					paddingHorizontal: 8,
					opacity: pressed ? 0.6 : 1,
				})}
			>
				<Text
					allowFontScaling={allowFontScaling}
					style={{ color: palette.accentInk, fontSize: 17 * scale, lineHeight: 24 * scale, fontWeight: "600" }}
				>
					Done
				</Text>
			</Pressable>
		</View>
	);
}
