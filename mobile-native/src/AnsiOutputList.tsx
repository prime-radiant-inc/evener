// Terminal output drawn a line at a time in a virtualized list, as the step
// output viewer and a shell job's detail show it, so a 10,000-line run
// scrolls smoothly.
import { type ReactElement, useMemo } from "react";
import { FlatList, type StyleProp, type ViewStyle } from "react-native";
import type { AnsiLine } from "../../cmd/evener-hub/frontend/src/widgets/codeblock/ansi";
import { AnsiOutputLine } from "./AnsiOutputLine";
import { parseOutputLines } from "./ansiOutputStyles";

const renderLine = ({ item }: { item: AnsiLine }) => <AnsiOutputLine line={item} />;

export function AnsiOutputList({
	text,
	header,
	footer,
	contentContainerStyle,
}: {
	text: string;
	header?: ReactElement | null;
	footer?: ReactElement | null;
	contentContainerStyle?: StyleProp<ViewStyle>;
}) {
	const lines = useMemo(() => parseOutputLines(text), [text]);
	return (
		<FlatList
			data={lines}
			// Cells re-render only for new lines: a render of the screen around
			// the list (a tree update that leaves the text alone) leaves them be.
			strictMode
			renderItem={renderLine}
			ListHeaderComponent={header}
			ListFooterComponent={footer}
			contentContainerStyle={contentContainerStyle}
		/>
	);
}
