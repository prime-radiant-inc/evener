// The recorded corpora are read only for the tool families session, so the
// demo's usual sessions never depend on agent/testdata being there.
import { expect, it, vi } from "vitest";
import { createDemoSessions } from "./demoSessions.js";

vi.mock("./demoToolFamilies.js", () => ({
	recordedToolCwd: () => {
		throw new Error("read the recorded corpora");
	},
	recordedToolFamilies: () => {
		throw new Error("read the recorded corpora");
	},
}));

it("builds the usual demo sessions without reading the recorded corpora", () => {
	expect(createDemoSessions().length).toBeGreaterThan(0);
	expect(() => createDemoSessions({ toolFamilies: true })).toThrow("read the recorded corpora");
});
