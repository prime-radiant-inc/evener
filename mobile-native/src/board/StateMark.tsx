import { type SFSymbol, SymbolView } from "expo-symbols";
import { View } from "react-native";
import { useColors } from "../ui";
import { type BoardState, stateWord } from "./attention";
import { PulseMeter } from "./PulseMeter";

type Tint = "danger" | "attention" | "accent" | "alive";
export type Glyph = { name: SFSymbol; tint: Tint; size: number };

const GLYPHS: Record<BoardState, Glyph | null> = {
	failed: { name: "xmark.octagon.fill", tint: "danger", size: 20 },
	question: { name: "questionmark.circle.fill", tint: "attention", size: 20 },
	approval: { name: "hand.raised.circle.fill", tint: "attention", size: 20 },
	warning: { name: "exclamationmark.triangle.fill", tint: "attention", size: 20 },
	restartNeeded: { name: "arrow.triangle.2.circlepath.circle.fill", tint: "attention", size: 20 },
	working: { name: "circle.fill", tint: "alive", size: 8 },
	finished: { name: "circle.fill", tint: "accent", size: 8 },
	idle: null,
	shutDown: null,
};

/** A row's leading mark (spec 13.1). `moving` is true only on Live's working
 * rows: one meter per view, and a pinned or project copy of the same session
 * gets a still green dot. */
export function markFor(state: BoardState, moving: boolean): Glyph | "meter" | null {
	return state === "working" && moving ? "meter" : GLYPHS[state];
}

export function StateMark({
	state,
	moving = false,
	connected = true,
}: {
	state: BoardState;
	moving?: boolean;
	connected?: boolean;
}) {
	const { palette } = useColors();
	const mark = markFor(state, moving);
	return (
		<View
			style={{ width: 28, alignItems: "center", justifyContent: "center" }}
			accessible={mark !== null}
			accessibilityLabel={mark ? stateWord(state) : undefined}
		>
			{mark === "meter" ? (
				<PulseMeter tone={connected ? "alive" : "gray"} />
			) : mark ? (
				<SymbolView name={mark.name} tintColor={palette[mark.tint]} size={mark.size} />
			) : null}
		</View>
	);
}
