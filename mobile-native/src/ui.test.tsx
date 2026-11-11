import { afterEach, expect, it, vi } from "vitest";
import { Choice, Copy, useColors } from "./ui";
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

it("uses the same body and supporting line measures as grouped rows", () => {
	mode.fontScale = 2;
	const body = render(<Copy>body</Copy>).root.findByType("Text" as never);
	const supporting = render(<Copy muted>detail</Copy>).root.findByType("Text" as never);
	expect(body.props.style).toMatchObject({ fontSize: 34, lineHeight: 44 });
	expect(supporting.props.style).toMatchObject({ fontSize: 26, lineHeight: 36 });
});

it("uses a separate check slot for choices without changing their selection semantics", () => {
	const onPress = vi.fn();
	const tree = render(<Choice label="choice-data" selected disabled={false} onPress={onPress} />);
	const radio = tree.root.findByProps({ accessibilityRole: "radio" });
	const symbol = tree.root.findByType("SymbolView" as never);
	expect(symbol.props.name).toBe("checkmark");
	expect(radio.props.accessibilityState).toEqual({ checked: true, disabled: false });
	radio.props.onPress();
	expect(onPress).toHaveBeenCalledOnce();
});

it("maps the existing color keys onto the spec's light palette", () => {
	mode.scheme = "light";
	const colors = renderHook(() => useColors()).result.current;
	expect(colors).toMatchObject({
		background: "#F6F7F9",
		surface: "#F2F4F7",
		text: "#1A1D23",
		secondary: "#555B66",
		border: "#D5D9E0",
		accent: "#005FBA",
		error: "#BD1B21",
		warning: "#9C4908",
		onAccent: "#FFFFFF",
	});
	expect(colors.palette.scheme).toBe("light");
});

it("maps the existing color keys onto the spec's dark palette", () => {
	mode.scheme = "dark";
	const colors = renderHook(() => useColors()).result.current;
	expect(colors).toMatchObject({
		background: "#111214",
		surface: "#191C21",
		text: "#F1F3F6",
		secondary: "#B2B7C0",
		border: "#333842",
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
	expect(colors.background).toBe("#F6F7F9");
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
		color: "#E3E3E8",
	});
});
