import { expect, it, vi } from "vitest";
import { render } from "../renderNative.testkit";
import { Spinner } from "./Spinner";

vi.mock("react-native", async () => ({
	...(await import("../renderNative.testkit")).nativeModuleMock(),
}));

it("waits in one padded space, named for VoiceOver by what it waits on", () => {
	const spinner = render(<Spinner label="Loading marketplaces" />).root.findByType("ActivityIndicator" as never);
	expect(spinner.props.accessibilityLabel).toBe("Loading marketplaces");
	expect(spinner.props.style).toEqual({ padding: 32 });
});
