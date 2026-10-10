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
	needsYou: { name: "exclamationmark.bubble.fill", tint: "attention", size: 20 },
	approval: { name: "hand.raised.circle.fill", tint: "attention", size: 20 },
	warning: { name: "exclamationmark.triangle.fill", tint: "attention", size: 20 },
	restartNeeded: { name: "arrow.triangle.2.circlepath.circle.fill", tint: "attention", size: 20 },
	working: { name: "circle.fill", tint: "alive", size: 8 },
	// A finished row's blue dot is BoardRow's unseen dot.
	finished: null,
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
	stuck = false,
	perMinute,
}: {
	state: BoardState;
	moving?: boolean;
	connected?: boolean;
	/** The row's why line reads "May be stuck" (spec 13.1, 16.4): the meter
	 * goes flat and amber, same as the mark it echoes at row size. Offline
	 * still wins over stuck - a dropped connection isn't the session stalling. */
	stuck?: boolean;
	/** The meter's per-minute counts (S5's activity read); absent, the meter
	 * shows its still fallback. */
	perMinute?: readonly number[];
}) {
	const { palette } = useColors();
	const mark = markFor(state, moving);
	const tone = !connected ? "gray" : stuck ? "attention" : "alive";
	return (
		<View
			style={{ width: 28, alignItems: "center", justifyContent: "center" }}
			accessible={mark !== null}
			accessibilityLabel={mark ? stateWord(state) : undefined}
		>
			{mark === "meter" ? (
				<PulseMeter tone={tone} perMinute={perMinute} />
			) : mark ? (
				<SymbolView name={mark.name} tintColor={palette[mark.tint]} size={mark.size} />
			) : null}
		</View>
	);
}
