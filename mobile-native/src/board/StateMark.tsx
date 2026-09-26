import { type SFSymbol, SymbolView } from "expo-symbols";
import { View } from "react-native";
import { useColors } from "../ui";
import { type BoardState, stateWord } from "./attention";
import { PulseMeter } from "./PulseMeter";

type Tint = "danger" | "attention" | "accent" | "alive";
export type Glyph = { name: SFSymbol; tint: Tint; size: number };

/** A row's leading mark (spec 13.1). `moving` is true only on Live's working
 * rows: one meter per view, and a pinned or project copy of the same session
 * gets a still green dot. */
export function markFor(state: BoardState, moving: boolean): Glyph | "meter" | null {
	switch (state) {
		case "failed":
			return { name: "xmark.octagon.fill", tint: "danger", size: 20 };
		case "question":
			return { name: "questionmark.circle.fill", tint: "attention", size: 20 };
		case "approval":
			return { name: "hand.raised.circle.fill", tint: "attention", size: 20 };
		case "warning":
			return { name: "exclamationmark.triangle.fill", tint: "attention", size: 20 };
		case "restartNeeded":
			return { name: "arrow.triangle.2.circlepath.circle.fill", tint: "attention", size: 20 };
		case "finished":
			return { name: "circle.fill", tint: "accent", size: 8 };
		case "working":
			return moving ? "meter" : { name: "circle.fill", tint: "alive", size: 8 };
		default:
			return null;
	}
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
