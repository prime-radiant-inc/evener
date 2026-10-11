// A step's whole output in a sheet (spec 8.2's "Show all 412 lines").
import { AnsiOutputList } from "../AnsiOutputList";
import { ModalSheet } from "../sheet/ModalSheet";
import { MAX_ITEM_BYTES, TRUNCATION_MARKER } from "../projectedRows";
import { Copy } from "../ui";

export function LogViewer({ title, text, onClose }: { title: string; text: string; onClose: () => void }) {
	// The store cuts a field at 64 KiB and ends it with its marker.
	const cut = text.endsWith(TRUNCATION_MARKER);
	return (
		<ModalSheet title={title} done={{ onPress: onClose }} onRequestClose={onClose}>
			<AnsiOutputList
				text={text}
				contentContainerStyle={{ padding: 16 }}
				footer={cut ? <Copy muted>{`Showing the first ${MAX_ITEM_BYTES / 1024} KB`}</Copy> : null}
			/>
		</ModalSheet>
	);
}
