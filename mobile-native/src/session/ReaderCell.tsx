import type { CellRendererProps } from "@react-native/virtualized-lists";
import { Component, type RefObject } from "react";
import { View } from "react-native";
import type { ReaderMeasurement } from "../readerPosition";
import { type SessionListRow, sessionListKey } from "./transcriptRows";

/** What the reader's cell shares with its screen: the layout it keeps for
 * each row, and what to do once a row has laid out. */
export interface ReaderCellHost {
	measurements: RefObject<Map<string, ReaderMeasurement>>;
	onLaidOut(key: string): void;
}

/** The list's cell renderer: records each row's measured frame for the
 * reader's position, and forgets it when the row unmounts. FlatList needs
 * the same component type for the list's lifetime, so the screen makes this
 * once. */
export function createReaderCell({ measurements, onLaidOut }: ReaderCellHost) {
	return class ReaderCell extends Component<CellRendererProps<SessionListRow>> {
		componentWillUnmount() {
			measurements.current.delete(sessionListKey(this.props.item));
		}
		render() {
			const { children, item, onLayout, onFocusCapture, style } = this.props;
			return (
				<View
					style={style}
					{...{ onFocusCapture }}
					onLayout={(event) => {
						onLayout?.(event);
						if (!item) return;
						const key = sessionListKey(item);
						measurements.current.set(key, {
							key,
							y: event.nativeEvent.layout.y,
							height: event.nativeEvent.layout.height,
						});
						onLaidOut(key);
					}}
				>
					{children}
				</View>
			);
		}
	};
}
