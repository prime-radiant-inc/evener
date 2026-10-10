import { expect, it } from "vitest";
import { pairingFrom, suggestedHubName } from "./pairing";

it("reads a pairing link, ignoring the whitespace a paste brings", () => {
	expect(pairingFrom("  https://magic-kingdom:9180/auth/abc%2Fdef \n")).toEqual({
		origin: "https://magic-kingdom:9180",
		token: "abc/def",
	});
});

it("refuses anything that isn't a pairing link", () => {
	expect(pairingFrom("https://magic-kingdom:9180/")).toBeNull();
	expect(pairingFrom("WDJB-MJHT")).toBeNull();
	expect(pairingFrom("")).toBeNull();
});

it("suggests the hub's host name, without its port, as the hub's name", () => {
	expect(suggestedHubName("https://magic-kingdom:9180")).toBe("magic-kingdom");
	expect(suggestedHubName("http://100.113.28.18:9180")).toBe("100.113.28.18");
});

it("suggests a typed address as it is when it isn't a URL", () => {
	expect(suggestedHubName("magic-kingdom")).toBe("magic-kingdom");
});
