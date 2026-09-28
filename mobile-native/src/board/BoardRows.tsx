// The Board's swipeable row lists. They live apart from BoardRow.tsx so a
// module that uses only a row's pieces (headers, hairlines, the row itself)
// never loads react-native-gesture-handler.
import type { SessionActivity } from "@evener/appwire-client";
import type { ReactElement } from "react";
import { View } from "react-native";
import type { ClassifiedRow } from "./attention";
import { BoardRow, type BoardRowProps, Hairline, TITLE_INSET } from "./BoardRow";
import type { RowSwipes } from "./rowSwipes";
import { SwipeRow, swipeAccessibility } from "./SwipeRow";

/** What every row in one of the Board's lists shares. */
export type RowContext = Pick<BoardRowProps, "connected" | "usual" | "hostLabel" | "msSinceRead" | "now" | "onOpen"> & {
	draftRefs: ReadonlySet<string>;
	/** A row's swipes, by whether it sits in an archived tier. */
	swipes: (item: ClassifiedRow, archived: boolean) => RowSwipes;
	/** Each session's latest activity read (S5), by ref. */
	activityOf: (ref: string) => SessionActivity | undefined;
};

/** A list of Board rows, each one swipeable, separated by hairlines inset
 * to the title. `archived` rows sit in an archived tier. */
export function BoardRows({
	items,
	variant,
	moving,
	archived = false,
	context,
}: {
	items: readonly ClassifiedRow[];
	variant: BoardRowProps["variant"];
	moving: boolean;
	archived?: boolean;
	context: RowContext;
}): ReactElement {
	const { draftRefs, swipes, activityOf, ...shared } = context;
	return (
		<>
			{items.map((item, index) => {
				const { leading, trailing, dimmed } = swipes(item, archived);
				return (
					<View key={item.row.ref}>
						{index > 0 ? <Hairline inset={TITLE_INSET} /> : null}
						<SwipeRow leading={leading} trailing={trailing}>
							<BoardRow
								item={item}
								variant={variant}
								moving={moving}
								hasDraft={draftRefs.has(item.row.ref)}
								activity={activityOf(item.row.ref)}
								dimmed={dimmed}
								{...swipeAccessibility(leading, trailing)}
								{...shared}
							/>
						</SwipeRow>
					</View>
				);
			})}
		</>
	);
}
