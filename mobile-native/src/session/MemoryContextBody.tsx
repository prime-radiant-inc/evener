// The opened body of an automatic memory refresh (a systemMessage with the
// typed eventKind "memory-context"): the scope and index state, then the
// decoded index through the existing Markdown renderer, then a separately
// folded literal Source. It mirrors the web renderer's body, but keeps the
// phone's own disclosure affordance (SystemEvent) and Markdown component.
//
// The observation is absent when raw.memoryContext did not validate: the
// refresh then opens as its complete original text rather than invented index
// data. Even when the observation is present, the Source shows the complete
// recorded text verbatim, so syntax or content the Markdown renderer omits
// (task checkboxes, images, escaped source, the delegate read-only suffix)
// stays inspectable. That text is the row's own `text`, independent of the
// decoded content.
import { useState } from "react";
import { Text, View } from "react-native";
import {
	type MemoryContextObservation,
	memoryContextEmptyText,
	memoryContextScopeLabel,
	memoryContextStateLabel,
} from "@evener/appwire-client";
import { MarkdownResponse } from "../MarkdownResponse";
import { allowFontScaling, useColors, useTextScale } from "../ui";
import { SystemEvent } from "./SystemEvent";

/** The folded Source's label. */
const SOURCE_LABEL = "Source";

export function MemoryContextBody({ observation, source }: { observation?: MemoryContextObservation; source: string }) {
	const [sourceOpen, setSourceOpen] = useState(false);
	const { palette } = useColors();
	const scale = useTextScale();
	const quiet = { fontSize: 13 * scale, lineHeight: 18 * scale, color: palette.inkLow };

	if (!observation) {
		// Decode failure: the complete recorded text, not a guessed index.
		return (
			<View testID="memory-context-body">
				<Text allowFontScaling={allowFontScaling} selectable style={quiet} testID="memory-context-fallback">
					{source}
				</Text>
			</View>
		);
	}

	const meta = `${memoryContextScopeLabel(observation.scope)} · ${memoryContextStateLabel(observation.state)}`;
	const hasContent = observation.content.trim() !== "";
	return (
		<View testID="memory-context-body" style={{ gap: 6 }}>
			<View style={{ flexDirection: "row", alignItems: "center", gap: 6 }}>
				<Text allowFontScaling={allowFontScaling} style={quiet} testID="memory-context-scope-state">
					{meta}
				</Text>
				{observation.truncated ? (
					<Text allowFontScaling={allowFontScaling} style={quiet} testID="memory-context-truncated">
						truncated
					</Text>
				) : null}
			</View>
			{hasContent ? (
				<View testID="memory-context-content">
					<MarkdownResponse markdown={observation.content} />
				</View>
			) : (
				<Text allowFontScaling={allowFontScaling} style={quiet} testID="memory-context-empty">
					{memoryContextEmptyText(observation.state)}
				</Text>
			)}
			<SystemEvent label={SOURCE_LABEL} expanded={sourceOpen} onToggle={() => setSourceOpen((open) => !open)}>
				<Text allowFontScaling={allowFontScaling} selectable style={quiet} testID="memory-context-source-text">
					{source}
				</Text>
			</SystemEvent>
		</View>
	);
}
