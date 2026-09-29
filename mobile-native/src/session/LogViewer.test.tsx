import type { ReactTestInstance } from "react-test-renderer";
import { describe, expect, it, vi } from "vitest";
import { TRUNCATION_MARKER } from "../projectedRows";
import { render, renderedText } from "../renderNative.testkit";
import { LogViewer } from "./LogViewer";

vi.mock("react-native", async () => ({
	...(await import("../renderNative.testkit")).nativeModuleMock(),
}));
vi.mock("react-native-safe-area-context", () => ({ SafeAreaView: "SafeAreaView" }));

function lineLabels(root: ReactTestInstance) {
	return root
		.findAll((node) => String(node.type) === "Text" && /^line \d+/.test(node.props.accessibilityLabel ?? ""))
		.map((node) => node.props.accessibilityLabel);
}

describe("the full log", () => {
	const text = Array.from({ length: 412 }, (_, n) => `line ${n + 1}`).join("\n");

	it("renders every line in one virtualized list, under the step's intent", () => {
		const tree = render(<LogViewer title="Run the tests" text={text} onClose={() => {}} />);
		expect(tree.root.findAll((node) => String(node.type) === "FlatList")).toHaveLength(1);
		expect(lineLabels(tree.root)).toHaveLength(412);
		expect(renderedText(tree)).toContain("Run the tests");
	});

	it("says it shows only the first 64 KB when the text was cut", () => {
		const cut = render(<LogViewer title="t" text={`${text}${TRUNCATION_MARKER}`} onClose={() => {}} />);
		expect(renderedText(cut)).toContain("Showing the first 64 KB");
		const whole = render(<LogViewer title="t" text={text} onClose={() => {}} />);
		expect(renderedText(whole)).not.toContain("Showing the first");
	});

	it("closes with Done", () => {
		const onClose = vi.fn();
		const tree = render(<LogViewer title="t" text={text} onClose={onClose} />);
		tree.root
			.findAll((node) => node.props.accessibilityLabel === "Done" && typeof node.props.onPress === "function")[0]
			.props.onPress();
		expect(onClose).toHaveBeenCalledTimes(1);
	});

	it("frames the log in the shared modal sheet, not its own header", () => {
		const tree = render(<LogViewer title="Run the tests" text={text} onClose={() => {}} />);
		expect(tree.root.findByType("Modal" as never).props.presentationStyle).toBe("pageSheet");
		expect(tree.root.findByProps({ accessibilityRole: "header" }).props.children).toBe("Run the tests");
	});
});
