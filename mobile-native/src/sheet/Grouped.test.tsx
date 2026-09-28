import { describe, expect, it, vi } from "vitest";
import { palettes } from "../design/tokens";
import { render, renderedText } from "../renderNative.testkit";
import { Group, GroupFooter, GroupLabel, Row, Segmented, SwitchRow, Tag } from "./Grouped";

vi.mock("react-native", async () => ({
	...(await import("../renderNative.testkit")).nativeModuleMock(),
}));
vi.mock("expo-symbols", () => ({ SymbolView: "SymbolView" }));

const light = palettes.light;
const symbols = (tree: ReturnType<typeof render>) =>
	tree.root.findAll((node) => node.type === ("SymbolView" as never)).map((node) => node.props.name);
const texts = (tree: ReturnType<typeof render>) => tree.root.findAllByType("Text" as never);
const merged = (style: unknown) => Object.assign({}, ...[style].flat());

describe("a row", () => {
	it("reads as its label, detail and value, and opens on a tap", () => {
		const onPress = vi.fn();
		const tree = render(
			<Row icon="server.rack" label="Host" sub="macOS · arm64" value="magic-kingdom" chevron onPress={onPress} />,
		);
		const button = tree.root.findByProps({ accessibilityRole: "button" });
		expect(button.props.accessibilityLabel).toBe("Host, macOS · arm64, magic-kingdom");
		button.props.onPress();
		expect(onPress).toHaveBeenCalledTimes(1);
		expect(symbols(tree)).toEqual(["server.rack", "chevron.right"]);
	});

	it("is one quiet element when it has no action", () => {
		const tree = render(<Row label="Version" value="0.9.412" />);
		expect(tree.root.findAllByProps({ accessibilityRole: "button" })).toHaveLength(0);
		expect(renderedText(tree)).toContain("0.9.412");
	});

	it("refuses taps while disabled", () => {
		const tree = render(<Row label="Connect" tone="accent" disabled onPress={() => {}} />);
		const button = tree.root.findByProps({ accessibilityRole: "button" });
		expect(button.props.disabled).toBe(true);
		expect(button.props.accessibilityState).toEqual({ disabled: true });
	});

	it("draws a picker's check only on the chosen row", () => {
		const chosen = render(<Row label="magic-kingdom" checked onPress={() => {}} />);
		expect(symbols(chosen)).toEqual(["checkmark"]);
		expect(chosen.root.findByProps({ accessibilityRole: "button" }).props.accessibilityState).toEqual({
			disabled: false,
			selected: true,
		});
		expect(symbols(render(<Row label="paradise-park" checked={false} onPress={() => {}} />))).toEqual([]);
	});

	it("colors action and destructive labels, and sets a machine detail in Menlo", () => {
		expect(texts(render(<Row label="Connect" tone="accent" onPress={() => {}} />))[0]?.props.style.color).toBe(
			light.accentInk,
		);
		expect(texts(render(<Row label="Remove" tone="danger" onPress={() => {}} />))[0]?.props.style.color).toBe(
			light.dangerInk,
		);
		const path = render(<Row label="evener" sub="/home/jesse/git/evener" machineSub />);
		expect(merged(texts(path)[1]?.props.style).fontFamily).toBe("Menlo");
	});
});

describe("a group", () => {
	it("draws hairlines between its rows, not around them", () => {
		const tree = render(
			<Group>
				<Row label="One" />
				<Row label="Two" />
				<Row label="Three" />
			</Group>,
		);
		expect(tree.root.findAllByProps({ testID: "hairline" })).toHaveLength(2);
	});
});

describe("a switch row", () => {
	it("names its switch, tints it with the accent, and reports a flip", () => {
		const onChange = vi.fn();
		const tree = render(<SwitchRow label="Network" sub="Lets the session's commands reach the internet" value onChange={onChange} />);
		const toggle = tree.root.findByType("Switch" as never);
		expect(toggle.props.accessibilityLabel).toBe("Network");
		expect(toggle.props.trackColor.true).toBe(light.accent);
		toggle.props.onValueChange(false);
		expect(onChange).toHaveBeenCalledWith(false);
	});
});

describe("a segmented control", () => {
	const options = [
		{ value: "system", label: "System" },
		{ value: "light", label: "Light" },
		{ value: "dark", label: "Dark" },
	] as const;

	it("marks the chosen segment in the accent tint and reports a new choice", () => {
		const onChange = vi.fn();
		const tree = render(<Segmented label="Appearance" options={options} value="light" onChange={onChange} />);
		const radios = tree.root.findAllByProps({ accessibilityRole: "radio" });
		expect(radios.map((radio) => radio.props.accessibilityState.checked)).toEqual([false, true, false]);
		expect(radios[1]?.props.style.backgroundColor).toBe(light.accentBg);
		radios[2]?.props.onPress();
		expect(onChange).toHaveBeenCalledWith("dark");
	});

	it("ignores a tap on the chosen segment", () => {
		const onChange = vi.fn();
		const tree = render(<Segmented label="Appearance" options={options} value="light" onChange={onChange} />);
		tree.root.findAllByProps({ accessibilityRole: "radio" })[1]?.props.onPress();
		expect(onChange).not.toHaveBeenCalled();
	});
});

describe("labels, footers and tags", () => {
	it("uppercases a section label and keeps a machine label as typed in Menlo", () => {
		const section = merged(texts(render(<GroupLabel>Where</GroupLabel>))[0]?.props.style);
		expect(section).toMatchObject({ textTransform: "uppercase", fontWeight: "600", letterSpacing: 0.72 });
		const machine = merged(
			texts(render(<GroupLabel machine>superpowers-marketplace</GroupLabel>))[0]?.props.style,
		);
		expect(machine.fontFamily).toBe("Menlo");
		expect(machine.textTransform).toBeUndefined();
	});

	it("colors a footer by what it reports", () => {
		expect(texts(render(<GroupFooter tone="danger">paradise-park is offline.</GroupFooter>))[0]?.props.style.color).toBe(
			light.dangerInk,
		);
	});

	it("draws version drift as a gray tag", () => {
		expect(texts(render(<Tag text="Hub runs 0.9.412" tone="gray" />))[0]?.props.style).toMatchObject({
			color: light.inkMid,
			backgroundColor: light.inset,
		});
	});
});
