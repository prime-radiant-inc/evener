import { afterEach, expect, it, vi } from "vitest";
import { Copy, useColors } from "./ui";
import { render, renderHook, unmountMountedTrees } from "./renderNative.testkit";

// vi.mock is hoisted above everything else, so the scheme and fontScale it
// reads live in vi.hoisted state rather than plain top-level variables.
const mode = vi.hoisted(() => ({
	scheme: "light" as "light" | "dark" | "unspecified",
	fontScale: 1,
}));
vi.mock("react-native", async () => ({
	...(await import("./renderNative.testkit")).nativeModuleMock(),
	useColorScheme: () => mode.scheme,
	useWindowDimensions: () => ({ fontScale: mode.fontScale, scale: 2, width: 390, height: 844 }),
}));

// The Dynamic Type case below sets a non-default fontScale; reset it so a
// later test never inherits it.
afterEach(() => {
	unmountMountedTrees();
	mode.fontScale = 1;
});

it("maps the existing color keys onto the spec's light palette", () => {
	mode.scheme = "light";
	const colors = renderHook(() => useColors()).result.current;
	expect(colors).toMatchObject({
		background: "#FAF9F6",
		surface: "#F4F3EE",
		text: "#252521",
		secondary: "#5F5F57",
		border: "#DDDCD4",
		accent: "#0064C2",
		error: "#C51D23",
		warning: "#AD5209",
		onAccent: "#FFFFFF",
	});
	expect(colors.palette.scheme).toBe("light");
});

it("maps the existing color keys onto the spec's dark palette", () => {
	mode.scheme = "dark";
	const colors = renderHook(() => useColors()).result.current;
	expect(colors).toMatchObject({
		background: "#191918",
		surface: "#20201E",
		text: "#F2F1EB",
		secondary: "#B0AFA6",
		border: "#34342F",
		accent: "#459EFF",
		error: "#F17478",
		warning: "#F68F3C",
		onAccent: "#FFFFFF",
	});
	expect(colors.palette.scheme).toBe("dark");
});

it("treats an unspecified scheme as light", () => {
	mode.scheme = "unspecified";
	const colors = renderHook(() => useColors()).result.current;
	expect(colors.palette.scheme).toBe("light");
	expect(colors.background).toBe("#FAF9F6");
});

it("scales the yourMessage variant's text size with Dynamic Type", () => {
	mode.scheme = "light";
	mode.fontScale = 1.5;
	const tree = render(
		<Copy variant="yourMessage" label="You: Hi">
			Hi
		</Copy>,
	);
	const text = tree.root.findByType("Text" as never);
	expect(text.props.style).toMatchObject({
		fontFamily: "SourceSerif4-Regular",
		fontSize: 25.5,
		lineHeight: 37.5,
	});
});

it("sets the agentProse variant in the serif at 17/26 in the prose ink, scaled with Dynamic Type", () => {
	mode.scheme = "dark";
	mode.fontScale = 1.5;
	const tree = render(<Copy variant="agentProse">Done.</Copy>);
	const text = tree.root.findByType("Text" as never);
	expect(text.props.selectable).toBe(true);
	expect(text.props.style).toEqual({
		fontFamily: "SourceSerif4-Regular",
		fontSize: 25.5,
		lineHeight: 39,
		color: "#E0DED6",
	});
});
