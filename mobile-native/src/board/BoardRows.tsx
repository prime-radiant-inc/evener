// The Board's swipeable row. It lives apart from BoardRow.tsx so a
// module that uses only a row's pieces (headers, hairlines, the row itself)
// never loads react-native-gesture-handler.
import type { SessionActivity } from "@evener/appwire-client";
import type { ReactElement } from "react";
import type { ClassifiedRow } from "./attention";
import { BoardRow, type BoardRowProps } from "./BoardRow";
import { RowMenu, type RowMenuProps } from "./RowMenu";
import type { RowSwipes } from "./rowSwipes";
import { SwipeRow, swipeAccessibility } from "./SwipeRow";

/** What every row in one of the Board's lists shares. */
export type RowContext = Pick<BoardRowProps, "connected" | "usual" | "hostLabel" | "msSinceRead" | "now" | "onOpen"> & {
	draftRefs: ReadonlySet<string>;
	/** A row's swipes, by whether it sits in an archived tier. */
	swipes: (item: ClassifiedRow, archived: boolean) => RowSwipes;
	/** A row's long-press menu, by whether it sits in an archived tier. */
	menu: (item: ClassifiedRow, archived: boolean) => Omit<RowMenuProps, "item" | "hostLabel" | "children">;
	/** Each session's latest activity read (S5), by ref. */
	activityOf: (ref: string) => SessionActivity | undefined;
};

/** One Board row, swipeable and with a long-press menu. `archived` rows sit
 * in an archived tier. `onSwipeActive` hears the row's swipe actions open
 * and close. */
export function SwipeableBoardRow({
	item,
	variant,
	moving,
	archived = false,
	wash,
	context,
	onSwipeActive,
}: {
	item: ClassifiedRow;
	variant: BoardRowProps["variant"];
	moving: boolean;
	archived?: boolean;
	wash?: number;
	context: RowContext;
	onSwipeActive?: (active: boolean) => void;
}): ReactElement {
	const { draftRefs, swipes, menu, activityOf, ...shared } = context;
	const { leading, trailing, dimmed } = swipes(item, archived);
	return (
		<SwipeRow leading={leading} trailing={trailing} onActiveChange={onSwipeActive}>
			<RowMenu item={item} hostLabel={shared.hostLabel} {...menu(item, archived)}>
				<BoardRow
					item={item}
					variant={variant}
					moving={moving}
					hasDraft={draftRefs.has(item.row.ref)}
					activity={activityOf(item.row.ref)}
					dimmed={dimmed}
					wash={wash}
					{...swipeAccessibility(leading, trailing)}
					{...shared}
				/>
			</RowMenu>
		</SwipeRow>
	);
}
