// The scrolling body of an ask dock (spec 8.4): the part of a question or an
// approval that gives up height when the screen has less room than the dock
// needs (a long question, the largest text sizes, the keyboard up), while the
// dock's header and answer controls stay on screen outside it.
import { type ReactNode, useRef } from "react";
import { ScrollView, type StyleProp, type ViewStyle } from "react-native";
import { shrinkingScroller } from "./dockCard";

export function DockBody({
	contentContainerStyle,
	children,
}: {
	contentContainerStyle?: StyleProp<ViewStyle>;
	children: ReactNode;
}) {
	const scroller = useRef<ScrollView>(null);
	const viewport = useRef(0);
	const content = useRef(0);
	const flashed = useRef(false);
	// Flash the scroll indicator once, the first time the body is taller than
	// the room it has, so it's plain there is more to read.
	function cueOverflow() {
		if (flashed.current || viewport.current === 0 || content.current <= viewport.current) return;
		flashed.current = true;
		scroller.current?.flashScrollIndicators();
	}
	return (
		<ScrollView
			ref={scroller}
			// No floor: when room is short the body gives it up, down to a line,
			// so the answer controls outside it always stay on screen.
			style={shrinkingScroller}
			contentContainerStyle={contentContainerStyle}
			keyboardShouldPersistTaps="handled"
			onLayout={(event) => {
				viewport.current = event.nativeEvent.layout.height;
				cueOverflow();
			}}
			onContentSizeChange={(_width, height) => {
				content.current = height;
				cueOverflow();
			}}
		>
			{children}
		</ScrollView>
	);
}
