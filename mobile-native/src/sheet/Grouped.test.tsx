import { AccessibilityInfo, Platform, Pressable, type TextInput, View } from "react-native";
import { describe, expect, it, vi } from "vitest";
import { fonts, palettes } from "../design/tokens";
import { render, renderedText } from "../renderNative.testkit";
import { createRef } from "react";
import { act, type ReactTestInstance } from "react-test-renderer";
import {
	Button,
	FormError,
	Group,
	GroupedPage,
	GroupFooter,
	GroupLabel,
	Row,
	RowValue,
	Segmented,
	SwitchRow,
	Tag,
	TextFieldRow,
	useErrorInView,
	useFormError,
} from "./Grouped";

vi.mock("react-native", async () => ({
	...(await import("../renderNative.testkit")).nativeModuleMock(),
}));
vi.mock("expo-symbols", () => ({ SymbolView: "SymbolView" }));

const light = palettes.light;
const symbols = (tree: ReturnType<typeof render>) =>
	tree.root.findAll((node) => node.type === ("SymbolView" as never)).map((node) => node.props.name);
const texts = (tree: ReturnType<typeof render>) => tree.root.findAllByType("Text" as never);
const merged = (style: unknown) => Object.assign({}, ...[style].flat());
const isSurface = (node: { type: unknown; props: { style?: unknown } }) =>
	node.type === "View" && merged(node.props.style).backgroundColor === light.surface;
const surfaces = (tree: ReturnType<typeof render>) => tree.root.findAll(isSurface);

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

	it("pads its text 12pt above and below (spec 16.3)", () => {
		const tree = render(<Row label="Version" value="0.9.412" />);
		expect(merged(tree.root.findAllByType("View" as never)[0]?.props.style).paddingVertical).toBe(12);
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

	it("sets its second line in ink-mid", () => {
		const tree = render(<Row label="paradise-park" sub="Connected · macOS · arm64" />);
		expect(merged(texts(tree)[1]?.props.style).color).toBe(light.inkMid);
	});

	it("sets a machine label, such as a model id, in Menlo", () => {
		const model = render(<Row label="gpt-5.6" machineLabel />);
		expect(merged(texts(model)[0]?.props.style).fontFamily).toBe("Menlo");
		expect(merged(texts(render(<Row label="Status" />))[0]?.props.style).fontFamily).toBeUndefined();
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

	it("keeps 16pt from whatever comes before it when it has no label", () => {
		const tree = render(
			<Group>
				<Row label="One" />
			</Group>,
		);
		expect(merged(surfaces(tree)[0]?.props.style).marginTop).toBe(16);
	});

	it("hangs its label right over it, with no gap between them", () => {
		const tree = render(
			<Group label="Fleet">
				<Row label="Hosts" />
			</Group>,
		);
		const header = tree.root.findByProps({ accessibilityRole: "header" });
		expect(header.props.children).toBe("Fleet");
		expect(merged(header.props.style)).toMatchObject({ textTransform: "uppercase" });
		expect(merged(surfaces(tree)[0]?.props.style).marginTop).toBe(0);
	});

	it("sets a machine label, such as a marketplace, in Menlo as typed", () => {
		const tree = render(
			<Group label="superpowers-marketplace" machineLabel>
				<Row label="superpowers" />
			</Group>,
		);
		const style = merged(tree.root.findByProps({ accessibilityRole: "header" }).props.style);
		expect(style.fontFamily).toBe("Menlo");
		expect(style.textTransform).toBeUndefined();
	});

	it("never touches the group before it, and a labelled group follows its label", () => {
		const tree = render(
			<View>
				<Group>
					<Row label="Status" />
				</Group>
				<Group>
					<Row label="Edit" />
				</Group>
				<Group label="Models">
					<Row label="gpt-5.6" />
				</Group>
			</View>,
		);
		// findAll walks the tree in order, so this is the page from top to bottom.
		const blocks = tree.root
			.findAll((node) => node.props.accessibilityRole === "header" || surfaces(tree).includes(node))
			.map((node) => (node.type === ("Text" as never) ? "label" : "group"));
		expect(blocks).toEqual(["group", "group", "label", "group"]);
		expect(surfaces(tree).map((group) => merged(group.props.style).marginTop)).toEqual([16, 16, 0]);
	});

	it("starts a hairline under the text when both rows beside it carry a symbol", () => {
		const tree = render(
			<Group>
				<Row icon="server.rack" label="Hosts" />
				<Row icon="cpu" label="Providers" />
				<Row label="Version" />
			</Group>,
		);
		const insets = tree.root.findAllByProps({ testID: "hairline" }).map((line) => line.props.style.marginLeft);
		expect(insets).toEqual([50, 16]);
	});
});

describe("a switch row", () => {
	it("names its switch, tints it with the accent, and reports a flip", () => {
		const onChange = vi.fn();
		const tree = render(
			<SwitchRow label="Network" sub="Lets the session's commands reach the internet" value onChange={onChange} />,
		);
		const toggle = tree.root.findByType("Switch" as never);
		expect(toggle.props.accessibilityLabel).toBe("Network");
		expect(toggle.props.trackColor.true).toBe(light.accent);
		toggle.props.onValueChange(false);
		expect(onChange).toHaveBeenCalledWith(false);
	});

	it("sets its second line in ink-mid", () => {
		const tree = render(<SwitchRow label="Haptics" sub="A tap when a banner arrives" value onChange={() => {}} />);
		expect(merged(texts(tree)[1]?.props.style).color).toBe(light.inkMid);
	});

	it("draws its label and second line with the shared RowText, as a plain row does", () => {
		const drawn = (tree: ReturnType<typeof render>) => ({
			label: tree.root.findByProps({ testID: "row-label" }).props.children,
			sub: tree.root.findByProps({ testID: "row-sub" }).props.children,
		});
		const row = render(<Row label="Haptics" sub="A tap when a banner arrives" />);
		const sw = render(<SwitchRow label="Haptics" sub="A tap when a banner arrives" value onChange={() => {}} />);
		expect(drawn(sw)).toEqual(drawn(row));
	});

	it("dims while disabled, and gives iOS the off track's color", () => {
		const tree = render(<SwitchRow label="Network" value={false} disabled onChange={() => {}} />);
		const toggle = tree.root.findByType("Switch" as never);
		expect(toggle.props.disabled).toBe(true);
		// iOS draws the off track from ios_backgroundColor, not trackColor.false.
		expect(toggle.props.ios_backgroundColor).toBe(light.edgeStrong);
		expect(tree.root.findAllByType("View" as never)[0]?.props.style.opacity).toBe(0.4);
	});

	it("opens a detail from its text while its switch flips on its own, and says a problem in danger ink", () => {
		const onPress = vi.fn();
		const onChange = vi.fn();
		const tree = render(
			<SwitchRow
				label="cracked"
				sub="Broken"
				subTone="danger"
				accessibilityLabel="cracked, core, Broken"
				switchLabel="cracked on by default"
				value
				disabled
				onChange={onChange}
				onPress={onPress}
			/>,
		);
		const button = tree.root.findByProps({ accessibilityRole: "button" });
		expect(button.props.accessibilityLabel).toBe("cracked, core, Broken");
		button.props.onPress();
		expect(onPress).toHaveBeenCalledTimes(1);
		const toggle = tree.root.findByType("Switch" as never);
		expect(toggle.props.accessibilityLabel).toBe("cracked on by default");
		expect(toggle.props.disabled).toBe(true);
		// The detail still opens while the switch waits, so the row doesn't dim.
		expect(tree.root.findAllByType("View" as never)[0]?.props.style.opacity).toBe(1);
		const broken = texts(tree).find((node) => node.props.children === "Broken");
		expect(merged(broken?.props.style).color).toBe(light.dangerInk);
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

describe("a form's error", () => {
	it("shows in danger ink, and VoiceOver hears each new one", () => {
		const announce = vi.mocked(AccessibilityInfo.announceForAccessibility);
		announce.mockClear();
		const tree = render(<FormError error={{ message: "Name is required." }} />);
		const text = texts(tree)[0];
		expect(merged(text?.props.style).color).toBe(light.dangerInk);
		expect(text?.props.accessibilityLiveRegion).toBe("polite");
		expect(announce).toHaveBeenLastCalledWith("Name is required.");
		act(() => tree.update(<FormError error={{ message: "Select an available base provider." }} />));
		expect(announce).toHaveBeenLastCalledWith("Select an available base provider.");
		act(() => tree.update(<FormError error={null} />));
		expect(texts(tree)).toHaveLength(0);
		expect(announce).toHaveBeenCalledTimes(2);
	});

	it("leaves Android to its live region, so a new error is read once", () => {
		const announce = vi.mocked(AccessibilityInfo.announceForAccessibility);
		announce.mockClear();
		const os = Platform.OS;
		Object.assign(Platform, { OS: "android" });
		try {
			const tree = render(<FormError error={{ message: "Name is required." }} />);
			expect(texts(tree)[0]?.props.accessibilityLiveRegion).toBe("polite");
			expect(announce).not.toHaveBeenCalled();
		} finally {
			Object.assign(Platform, { OS: os });
		}
	});

	it("brings the form to its top and speaks again on each refusal, even one that repeats the last", () => {
		const announce = vi.mocked(AccessibilityInfo.announceForAccessibility);
		announce.mockClear();
		const scrolls: unknown[] = [];
		let refuse = (_message: string | null) => {};
		function Form() {
			const [error, setError] = useFormError();
			refuse = (message) => {
				// A save clears the last error and fails again in one handler.
				setError(null);
				setError(message);
			};
			const page = useErrorInView(error);
			return (
				<GroupedPage scrollRef={page}>
					<FormError error={error} />
				</GroupedPage>
			);
		}
		const options = {
			createNodeMock: (element: { type: unknown }) =>
				element.type === "ScrollView" ? { scrollTo: (to: unknown) => scrolls.push(to) } : null,
		};
		render(<Form />, options);
		expect(scrolls).toEqual([]);
		act(() => refuse("Name is required."));
		act(() => refuse("Name is required."));
		expect(scrolls).toEqual([
			{ y: 0, animated: true },
			{ y: 0, animated: true },
		]);
		expect(announce).toHaveBeenCalledTimes(2);
	});
});

describe("labels, footers and tags", () => {
	it("uppercases a section label and keeps a machine label as typed in Menlo", () => {
		const section = merged(texts(render(<GroupLabel>Where</GroupLabel>))[0]?.props.style);
		expect(section).toMatchObject({ textTransform: "uppercase", fontWeight: "600", letterSpacing: 0.72 });
		const machine = merged(texts(render(<GroupLabel machine>superpowers-marketplace</GroupLabel>))[0]?.props.style);
		expect(machine.fontFamily).toBe("Menlo");
		expect(machine.textTransform).toBeUndefined();
	});

	it("colors a footer by what it reports", () => {
		const plain = texts(render(<GroupFooter>Add hosts from the web app.</GroupFooter>))[0]?.props.style;
		expect(merged(plain).color).toBe(light.inkMid);
		const style = texts(render(<GroupFooter tone="danger">paradise-park is offline.</GroupFooter>))[0]?.props.style;
		expect(merged(style).color).toBe(light.dangerInk);
		expect(merged(style).fontFamily).toBeUndefined();
	});

	it("sets a machine footer, such as an error the hub reported, in Menlo", () => {
		const style = texts(
			render(
				<GroupFooter tone="danger" machine>
					ssh: connect refused
				</GroupFooter>,
			),
		)[0]?.props.style;
		expect(merged(style)).toMatchObject({ color: light.dangerInk, fontFamily: fonts.mono });
	});

	it("draws version drift as a gray tag", () => {
		expect(texts(render(<Tag text="Hub runs 0.9.412" tone="gray" />))[0]?.props.style).toMatchObject({
			color: light.inkMid,
			backgroundColor: light.inset,
		});
	});

	it("draws a tag at the prototype's geometry: 11pt semibold on a 16pt line, 6 by 1 padding, 6pt corners (audit L2)", () => {
		expect(texts(render(<Tag text="1 offline" tone="amber" />))[0]?.props.style).toMatchObject({
			fontSize: 11,
			lineHeight: 16,
			fontWeight: "600",
			paddingHorizontal: 6,
			paddingVertical: 1,
			borderRadius: 6,
		});
	});
});

describe("a row whose label and value can't share a line", () => {
	// The test renderer has no layout engine, so these pin the flex rules
	// Row's comment describes.
	const hostRow = (
		<Row
			icon="server.rack"
			label="paradise-park"
			sub="Connected · macOS · arm64 · 3 live"
			value={<RowValue text="0.9.409" tag={{ text: "Hub runs 0.9.412", tone: "gray" }} />}
			chevron
			onPress={() => {}}
		/>
	);

	it("wraps the value onto its own line rather than shrinking the label to fit", () => {
		const tree = render(hostRow);
		const line = merged(tree.root.findByProps({ testID: "row-line" }).props.style);
		expect(line).toMatchObject({ flexDirection: "row", flexWrap: "wrap", alignItems: "center" });
		const label = merged(tree.root.findByProps({ testID: "row-label" }).props.style);
		expect(label).toMatchObject({ flexGrow: 1, flexShrink: 1 });
		// flex: 1 would zero the label's basis, handing the line to the value.
		expect(label.flex).toBeUndefined();
		expect(label.flexBasis).toBeUndefined();
	});

	it("lets a value too wide for a line of its own wrap its text", () => {
		const tree = render(hostRow);
		expect(merged(tree.root.findByProps({ testID: "row-value" }).props.style).flexShrink).toBe(1);
		const version = texts(tree).find((node) => node.props.children === "0.9.409");
		expect(merged(version?.props.style).flexShrink).toBe(1);
	});

	it("keeps the second line out of the wrapping line, so a long one never pushes the value down", () => {
		const tree = render(hostRow);
		const line = tree.root.findByProps({ testID: "row-line" });
		const lineText = line.findAll((node) => node.type === ("Text" as never)).map((node) => node.props.children);
		expect(lineText).not.toContain("Connected · macOS · arm64 · 3 live");
		expect(lineText).toContain("paradise-park");
		expect(lineText).toContain("0.9.409");
		expect(texts(tree).map((node) => node.props.children)).toContain("Connected · macOS · arm64 · 3 live");
	});

	it("shows a plain text or number value, and leaves no slot for one that renders nothing", () => {
		expect(renderedText(render(<Row label="Hubs" value={1} />))).toContain("1");
		expect(renderedText(render(<Row label="Display" value="System" />))).toContain("System");
		for (const empty of [undefined, null, false, ""]) {
			const tree = render(<Row label="In-app alerts" value={empty} />);
			expect(tree.root.findAllByProps({ testID: "row-value" })).toHaveLength(0);
		}
	});

	it("keeps the glyph and chevron outside the wrapping line", () => {
		const tree = render(hostRow);
		const line = tree.root.findByProps({ testID: "row-line" });
		expect(line.findAll((node) => node.type === ("SymbolView" as never))).toHaveLength(0);
		expect(symbols(tree)).toEqual(["server.rack", "chevron.right"]);
	});
});

describe("a row's value with a tag", () => {
	it("sets the value in ink-mid tabular figures beside its tag, and reads as both", () => {
		const tree = render(<Row label="Hosts" value={<RowValue text="2" tag={{ text: "1 offline", tone: "amber" }} />} />);
		const [value, tag] = texts(tree).slice(1);
		expect(merged(value?.props.style)).toMatchObject({ color: light.inkMid, fontVariant: ["tabular-nums"] });
		expect(value?.props.children).toBe("2");
		expect(tag?.props.style).toMatchObject({ color: light.attentionInk, backgroundColor: light.attentionBg });
		expect(tag?.props.children).toBe("1 offline");
	});

	it("sets the value at 15pt, the prototype's .gv, so a value and a tag leave the label room (audit L3)", () => {
		const tree = render(<Row label="Version" value="0.9.412" />);
		expect(merged(texts(tree)[1]?.props.style)).toMatchObject({ fontSize: 15, lineHeight: 20 });
	});

	it("sets a value that needs a human in the attention ink", () => {
		const tree = render(<RowValue text="Offline" tone="attention" />);
		expect(merged(texts(tree)[0]?.props.style)).toMatchObject({ color: light.attentionInk });
	});

	it("shows the tag alone when there is no value", () => {
		const tree = render(<RowValue tag={{ text: "Hub runs 0.9.412", tone: "gray" }} />);
		expect(texts(tree).map((node) => node.props.children)).toEqual(["Hub runs 0.9.412"]);
	});
});

describe("a text field row", () => {
	it("edits a machine value in Menlo, named for VoiceOver, without capitalizing or correcting it", () => {
		const changes: string[] = [];
		const tree = render(
			<TextFieldRow label="SSH address" value="attic.lan" onChangeText={(text) => changes.push(text)} />,
		);
		const input = tree.root.findByType("TextInput" as never);
		expect(input.props.accessibilityLabel).toBe("SSH address");
		expect(input.props.value).toBe("attic.lan");
		expect(input.props.autoCapitalize).toBe("none");
		expect(input.props.autoCorrect).toBe(false);
		expect(merged(input.props.style)).toMatchObject({ fontFamily: fonts.mono, color: light.inkHi });
		input.props.onChangeText("attic.local");
		expect(changes).toEqual(["attic.local"]);
	});

	it("edits words someone typed, such as a hub's name, in SF Pro at the row size, and can hide a secret", () => {
		const name = render(<TextFieldRow label="Hub name" value="attic" onChangeText={() => {}} machine={false} />);
		const nameInput = name.root.findByType("TextInput" as never);
		expect(merged(nameInput.props.style).fontFamily).toBeUndefined();
		expect(merged(nameInput.props.style).fontSize).toBe(17);
		expect(nameInput.props.secureTextEntry).toBe(false);
		const token = render(
			<TextFieldRow label="New token" value="" onChangeText={() => {}} placeholder="New token" secure />,
		).root.findByType("TextInput" as never);
		expect(token.props.secureTextEntry).toBe(true);
		expect(token.props.placeholder).toBe("New token");
		expect(token.props.placeholderTextColor).toBe(light.inkLow);
	});

	it("draws a machine field's placeholder in the UI font, and switches to Menlo once it holds a value (spec 16.2)", () => {
		// React Native draws a TextInput's placeholder in the input's own
		// font, so an empty machine field must not be Menlo or the hint
		// reads as machine text.
		const empty = render(
			<TextFieldRow label="From the address" value="" onChangeText={() => {}} placeholder="From the address" />,
		).root.findByType("TextInput" as never);
		expect(merged(empty.props.style).fontFamily).toBeUndefined();
		const filled = render(
			<TextFieldRow label="From the address" value="ws://attic.lan" onChangeText={() => {}} />,
		).root.findByType("TextInput" as never);
		expect(merged(filled.props.style).fontFamily).toBe(fonts.mono);
	});

	it("lets words someone chose be capitalized and corrected, and keeps a machine value as typed", () => {
		const name = render(<TextFieldRow label="Hub name" value="" onChangeText={() => {}} machine={false} />);
		const nameInput = name.root.findByType("TextInput" as never);
		expect(nameInput.props).toMatchObject({ autoCapitalize: "sentences", autoCorrect: true, spellCheck: true });
		const address = render(<TextFieldRow label="SSH address" value="" onChangeText={() => {}} />);
		const addressInput = address.root.findByType("TextInput" as never);
		expect(addressInput.props).toMatchObject({ autoCapitalize: "none", autoCorrect: false, spellCheck: false });
	});

	it("never offers to fill or save a secret it hides", () => {
		const token = render(<TextFieldRow label="New token" value="" onChangeText={() => {}} secure />);
		const input = token.root.findByType("TextInput" as never);
		expect(input.props).toMatchObject({ secureTextEntry: true, textContentType: "none", autoComplete: "off" });
	});

	it("hands its return key to the form, and its input to a ref, so one field can lead to the next", () => {
		const onSubmit = vi.fn();
		const ref = createRef<TextInput>();
		const tree = render(
			<TextFieldRow
				label="Instance name"
				value=""
				onChangeText={() => {}}
				returnKeyType="next"
				onSubmitEditing={onSubmit}
				ref={ref}
			/>,
		);
		const input = tree.root.findByType("TextInput" as never);
		expect(input.props.returnKeyType).toBe("next");
		input.props.onSubmitEditing();
		expect(onSubmit).toHaveBeenCalledOnce();
		expect(input.props.ref).toBe(ref);
	});

	it("takes several lines when asked, and holds while disabled", () => {
		const input = render(
			<TextFieldRow label="Roots" value="" onChangeText={() => {}} multiline disabled />,
		).root.findByType("TextInput" as never);
		expect(input.props.multiline).toBe(true);
		expect(input.props.editable).toBe(false);
	});
});

describe("a row's trailing control (audit M13)", () => {
	it("draws its own control after the row's text, outside what a press on the row opens", () => {
		const open = vi.fn();
		const install = vi.fn();
		const tree = render(
			<Row
				label="code-review"
				sub="Review changes for correctness"
				onPress={open}
				accessory={<Pressable accessibilityRole="button" accessibilityLabel="Install" onPress={install} />}
			/>,
		);
		const row = tree.root.find(
			(node) => node.type === Pressable && node.props.accessibilityLabel?.startsWith("code-review"),
		);
		// The row's own button doesn't hold the control, so each is its own
		// VoiceOver element and its own target.
		expect(row.findAll((node) => node.props.accessibilityLabel === "Install")).toHaveLength(0);
		const control = tree.root.find((node) => node.type === Pressable && node.props.accessibilityLabel === "Install");
		act(() => control.props.onPress());
		expect(install).toHaveBeenCalledTimes(1);
		expect(open).not.toHaveBeenCalled();
		act(() => row.props.onPress());
		expect(open).toHaveBeenCalledTimes(1);
	});

	it("keeps a row with a control and no press a single reading for VoiceOver, beside the control", () => {
		const tree = render(
			<Row
				label="pdf"
				accessory={<Pressable accessibilityRole="button" accessibilityLabel="Install" onPress={() => {}} />}
			/>,
		);
		const elements = tree.root.findAll(
			(node) => typeof node.type === "string" && (node.props.accessible === true || String(node.type) === "Pressable"),
		);
		expect(elements.map((node) => [String(node.type), node.props.accessibilityLabel])).toEqual([
			["View", "pdf"],
			["Pressable", "Install"],
		]);
	});
});

describe("a mini button as a row's trailing control (audit M13)", () => {
	it("has the row's whole height to reach into, since React Native clips a touch at its parent's bounds", () => {
		const tree = render(<Row label="pdf" accessory={<Button label="Install" mini onPress={() => {}} />} />);
		const holder = tree.root.findByProps({ testID: "row-accessory" });
		// Stretched to the row, which is at least 44pt, the holder spans the
		// mini button's 30pt plus its reach above and below.
		expect(holder.props.style).toMatchObject({ alignSelf: "stretch", justifyContent: "center" });
		const row = tree.root.find((node) => String(node.type) === "View" && node.props.accessibilityLabel === "pdf");
		const button = holder.find((node) => String(node.type) === "Pressable");
		expect(Object.assign({}, ...[row.props.style].flat()).minHeight).toBe(
			30 + button.props.hitSlop.top + button.props.hitSlop.bottom,
		);
	});
});

describe("a switch row's text, pressed (audit M13)", () => {
	it("shades like a row does, rather than dimming", () => {
		const tree = render(<SwitchRow label="superpowers" value onChange={() => {}} onPress={() => {}} />);
		const text = tree.root.find((node) => node.type === Pressable);
		const pressed = merged(text.props.style({ pressed: true }));
		expect(pressed.backgroundColor).toBe(light.pressed);
		expect(pressed.opacity ?? 1).toBe(1);
	});
});

describe("Button", () => {
	const pressable = (tree: ReturnType<typeof render>) => tree.root.find((node) => node.type === Pressable);
	const styleOf = (node: ReactTestInstance, pressed = false) =>
		Object.assign(
			{},
			...[typeof node.props.style === "function" ? node.props.style({ pressed }) : node.props.style].flat(),
		);

	it("draws the primary call to action filled in the accent, full width and 50pt, as the prototype's .btn.primary.big", () => {
		const onPress = vi.fn();
		const tree = render(<Button label="Open sign-in page" primary onPress={onPress} />);
		const button = pressable(tree);
		expect(button.props.accessibilityRole).toBe("button");
		expect(styleOf(button)).toMatchObject({ backgroundColor: light.accentFill, minHeight: 50, alignSelf: "stretch" });
		// Pressed, it dims as the app's other buttons do.
		expect(styleOf(button, true).opacity).toBe(0.65);
		const label = tree.root.find((node) => String(node.type) === "Text");
		expect(Object.assign({}, ...[label.props.style].flat())).toMatchObject({
			color: light.onFill,
			fontSize: 17,
			fontWeight: "600",
		});
		act(() => button.props.onPress());
		expect(onPress).toHaveBeenCalledTimes(1);
	});

	it("draws a plain button as a 36pt capsule on the surface, as the prototype's .btn, touchable over 44pt", () => {
		const tree = render(<Button label="Copy code" onPress={() => {}} />);
		const button = pressable(tree);
		expect(styleOf(button)).toMatchObject({ backgroundColor: light.surface, minHeight: 36 });
		const label = tree.root.find((node) => String(node.type) === "Text");
		expect(Object.assign({}, ...[label.props.style].flat())).toMatchObject({ color: light.inkHi, fontSize: 15 });
	});

	it("draws a mini button as the prototype's .mini-btn: accent text, no fill, shaded when pressed, touchable over 44pt (audit M13)", () => {
		const tree = render(<Button label="Install" mini onPress={() => {}} />);
		const button = pressable(tree);
		expect(styleOf(button)).toMatchObject({ paddingVertical: 6, paddingHorizontal: 8, borderRadius: 8, minHeight: 30 });
		expect(styleOf(button).backgroundColor).toBeUndefined();
		expect(styleOf(button, true).backgroundColor).toBe(light.pressed);
		const label = tree.root.find((node) => String(node.type) === "Text");
		expect(Object.assign({}, ...[label.props.style].flat())).toMatchObject({
			color: light.accentInk,
			fontSize: 13,
			fontWeight: "600",
		});
	});
	const onOS = (os: string, check: () => void) => {
		const saved = Platform.OS;
		Object.assign(Platform, { OS: os });
		try {
			check();
		} finally {
			Object.assign(Platform, { OS: saved });
		}
	};

	it.each([
		["ios", 44],
		["android", 48],
	])("reaches a plain button's touch to the %s minimum of %ipt", (os, minimum) => {
		onOS(os, () => {
			const button = pressable(render(<Button label="Copy code" onPress={() => {}} />));
			// The capsule is drawn at 36; its touch reaches the platform minimum.
			expect(36 + button.props.hitSlop.top + button.props.hitSlop.bottom).toBe(minimum);
		});
	});

	it.each(["ios", "android"])(
		"reaches a mini button's touch to its 44pt row's edges on %s, never into the row above or below",
		(os) => {
			onOS(os, () => {
				const button = pressable(render(<Button label="Install" mini onPress={() => {}} />));
				expect(30 + button.props.hitSlop.top + button.props.hitSlop.bottom).toBe(44);
			});
		},
	);

	// The legacy Action's look, kept as Action's sites move to Button (#3310).
	it.each([
		["ios", 44],
		["android", 48],
	])("draws a text button as accent text with no fill, 16pt, its touch the %s minimum of %ipt", (os, minimum) => {
		onOS(os, () => {
			const tree = render(<Button label="Edit" text onPress={() => {}} />);
			const button = pressable(tree);
			expect(styleOf(button)).toMatchObject({
				minHeight: minimum,
				minWidth: minimum,
				paddingHorizontal: 8,
				paddingVertical: 8,
				justifyContent: "center",
			});
			expect(styleOf(button).backgroundColor).toBeUndefined();
			expect(button.props.hitSlop).toBeUndefined();
			expect(styleOf(button, true).opacity).toBe(0.65);
			const label = tree.root.find((node) => String(node.type) === "Text");
			expect(merged(label.props.style)).toMatchObject({
				color: light.accentInk,
				fontSize: 16,
				fontWeight: "600",
				flexShrink: 1,
			});
		});
	});

	it.each([
		["ios", 44],
		["android", 48],
	])(
		"draws a compact primary as an accent pill sized to its label, 16pt, its touch the %s minimum of %ipt",
		(os, minimum) => {
			onOS(os, () => {
				const tree = render(<Button label="Save" primary compact onPress={() => {}} />);
				const button = pressable(tree);
				expect(styleOf(button)).toMatchObject({
					backgroundColor: light.accentFill,
					borderRadius: 24,
					paddingHorizontal: 18,
					paddingVertical: 8,
					minHeight: minimum,
					minWidth: minimum,
				});
				expect(styleOf(button).alignSelf).toBeUndefined();
				expect(styleOf(button, true).opacity).toBe(0.65);
				const label = tree.root.find((node) => String(node.type) === "Text");
				expect(merged(label.props.style)).toMatchObject({ color: light.onFill, fontSize: 16, fontWeight: "600" });
			});
		},
	);

	it("dims a disabled text button and holds its press", () => {
		const button = pressable(render(<Button label="Edit" text disabled onPress={() => {}} />));
		expect(button.props.disabled).toBe(true);
		expect(styleOf(button).opacity).toBe(0.4);
	});

	it("draws a quiet text button in the secondary ink at regular weight", () => {
		const tree = render(<Button label="Cancel" text quiet onPress={() => {}} />);
		expect(styleOf(pressable(tree)).backgroundColor).toBeUndefined();
		const label = tree.root.find((node) => String(node.type) === "Text");
		expect(merged(label.props.style)).toMatchObject({
			color: light.inkMid,
			fontSize: 16,
			fontWeight: "400",
		});
	});

	it("tells VoiceOver whether a disclosure button's section is open", () => {
		const open = pressable(render(<Button label="Details" text expanded onPress={() => {}} />));
		expect(open.props.accessibilityState).toEqual({ disabled: false, expanded: true });
		const closed = pressable(render(<Button label="Details" text expanded={false} onPress={() => {}} />));
		expect(closed.props.accessibilityState).toEqual({ disabled: false, expanded: false });
		const plain = pressable(render(<Button label="Details" expanded onPress={() => {}} />));
		expect(plain.props.accessibilityState).toEqual({ disabled: false, expanded: true });
	});

	it("names a button for VoiceOver by its accessibilityLabel when given one, else by its label", () => {
		const named = pressable(
			render(<Button label="Install" mini accessibilityLabel="Install gadget from acme" onPress={() => {}} />),
		);
		expect(named.props.accessibilityLabel).toBe("Install gadget from acme");
		expect(pressable(render(<Button label="Copy code" onPress={() => {}} />)).props.accessibilityLabel).toBeUndefined();
	});

	it("leaves a disabled mini button inert: Pressable's disabled holds its press, VoiceOver hears it dimmed", () => {
		const onPress = vi.fn();
		const button = pressable(render(<Button label="Install" mini disabled onPress={onPress} />));
		// React Native's Pressable drops presses while disabled; the test
		// renderer's stand-in doesn't, so the prop is the contract checked here.
		expect(button.props.disabled).toBe(true);
		expect(button.props.accessibilityState).toEqual({ disabled: true });
		expect(styleOf(button).opacity).toBe(0.4);
	});
});
