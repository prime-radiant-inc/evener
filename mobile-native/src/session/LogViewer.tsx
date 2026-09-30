// A step's whole output in a sheet (spec 8.2's "Show all 412 lines"). The
// lines sit in a virtualized list, so a 10,000-line test run scrolls smoothly.
import { useMemo } from "react";
import { FlatList } from "react-native";
import { parseOutputLines } from "../ansiOutputStyles";
import { ModalSheet } from "../sheet/ModalSheet";
import { AnsiOutputLine } from "../AnsiOutputLine";
import { MAX_ITEM_BYTES, TRUNCATION_MARKER } from "../projectedRows";
import { Copy } from "../ui";

export function LogViewer({ title, text, onClose }: { title: string; text: string; onClose: () => void }) {
	const lines = useMemo(() => parseOutputLines(text), [text]);
	// The store cuts a field at 64 KiB and ends it with its marker.
	const cut = text.endsWith(TRUNCATION_MARKER);
	return (
		<ModalSheet title={title} done={{ onPress: onClose }} onRequestClose={onClose}>
			<FlatList
				data={lines}
				contentContainerStyle={{ padding: 16 }}
				renderItem={({ item }) => <AnsiOutputLine line={item} />}
				ListFooterComponent={cut ? <Copy muted>{`Showing the first ${MAX_ITEM_BYTES / 1024} KB`}</Copy> : null}
			/>
		</ModalSheet>
	);
}
