// The horizontal chip row under a screen's nav bar: the Board's section chips
// (spec 7.1) and the Session's context chips (spec 8.1) both fill a row under
// the bar and scroll sideways. The fill and the trailing-edge fade are the
// strip's, so each screen supplies only the chips inside it.
//
// Off the glass the row fades at its trailing edge once the chips overflow, so
// a cut-off chip reads as "there's more"; the fade is into the page color, so
// on the glass it would paint an opaque band and the chips run under the
// glass's edge instead. The fade's width and its `chips-fade` id are the
// strip's own, shared by every caller.
import { type ReactNode, useState } from "react";
import { ScrollView, View } from "react-native";
import { useColors } from "../ui";
import { headerRowFill } from "./systemGlass";

export function ChipStrip({
	testID,
	onGlass,
	children,
}: {
	testID: string;
	/** The row sits on the nav bar's Liquid Glass: clear fill, no fade. */
	onGlass: boolean;
	children: ReactNode;
}) {
	const { palette } = useColors();
	const [viewportWidth, setViewportWidth] = useState(0);
	const [contentWidth, setContentWidth] = useState(0);
	const overflows = viewportWidth > 0 && contentWidth > viewportWidth;
	return (
		<View testID={testID} style={{ backgroundColor: headerRowFill(onGlass, palette) }}>
			<ScrollView
				horizontal
				showsHorizontalScrollIndicator={false}
				onLayout={(event) => setViewportWidth(event.nativeEvent.layout.width)}
				onContentSizeChange={(width) => setContentWidth(width)}
				contentContainerStyle={{ paddingHorizontal: 16, paddingVertical: 8, gap: 8 }}
			>
				{children}
			</ScrollView>
			{overflows && !onGlass ? (
				<View
					testID="chips-fade"
					pointerEvents="none"
					style={{
						position: "absolute",
						top: 0,
						bottom: 0,
						right: 0,
						width: 24,
						experimental_backgroundImage: `linear-gradient(to right, ${palette.page}00, ${palette.page})`,
					}}
				/>
			) : null}
		</View>
	);
}
