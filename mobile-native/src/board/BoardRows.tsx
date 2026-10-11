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
	/** What waits for the connection on a row, by ref (boardHold.ts). */
	waiting: (ref: string) => string | null;
};

/** One Board row, swipeable and with a long-press menu. `archived` rows sit
 * in an archived tier. `onSwipeActive` hears the row's swipe actions open
 * and close. In select mode (`selected` defined) the row neither swipes nor
 * opens a menu: it shows its checkbox, and a press chooses it. */
export function BoardListRow({
	item,
	variant,
	moving,
	archived = false,
	wash,
	selected,
	context,
	onSwipeActive,
}: {
	item: ClassifiedRow;
	variant: BoardRowProps["variant"];
	moving: boolean;
	archived?: boolean;
	wash?: number;
	selected?: boolean;
	context: RowContext;
	onSwipeActive?: (active: boolean) => void;
}): ReactElement {
	const { draftRefs, swipes, menu, activityOf, waiting: waitingFor, ...shared } = context;
	const { leading, trailing, dimmed } = swipes(item, archived);
	// A row something waits on dims until it has gone, as an unconfirmed
	// archive does (spec 14).
	const waiting = waitingFor(item.row.ref);
	const row = {
		item,
		variant,
		moving,
		hasDraft: draftRefs.has(item.row.ref),
		activity: activityOf(item.row.ref),
		dimmed: dimmed || waiting !== null,
		waiting,
		wash,
		...shared,
	};
	if (selected !== undefined) return <BoardRow {...row} selected={selected} />;
	return (
		<SwipeRow leading={leading} trailing={trailing} onActiveChange={onSwipeActive}>
			<RowMenu item={item} hostLabel={shared.hostLabel} {...menu(item, archived)}>
				<BoardRow {...row} {...swipeAccessibility(leading, trailing)} />
			</RowMenu>
		</SwipeRow>
	);
}
