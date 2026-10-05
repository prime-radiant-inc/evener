import type { ReactElement, ReactNode } from "react";
import { EnrichedMarkdownSelectionSurface } from "react-native-enriched-markdown";
import type { NativeDocumentSelection, NativeSavedRange } from "./nativeSelection";

export interface ReaderSelectionSurfaceProps {
	source: string;
	displayedSnapshotId: string;
	ranges: readonly NativeSavedRange[];
	onSelection(event: NativeDocumentSelection): void;
	onRangePress(operationIds: readonly string[]): void;
	children: ReactNode;
}

export function ReaderSelectionSurface(props: ReaderSelectionSurfaceProps): ReactElement {
	return <EnrichedMarkdownSelectionSurface {...props} style={{ flex: 1 }} />;
}
