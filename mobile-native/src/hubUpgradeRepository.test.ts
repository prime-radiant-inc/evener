import { describe, expect, it } from "vitest";
import {
	HubUpgradeRepository,
	type SyncUpgradeStorage,
} from "./hubUpgradeRepository";

const response = {
	release: "v1",
	channel: "stable",
	url: "",
	archive: "",
	prefix: "",
	binDir: "",
	shareBinDir: "",
	installed: ["evener"],
	restartMessage: "restart",
};
function fixture(): SyncUpgradeStorage & { values: Map<string, string> } {
	const values = new Map<string, string>();
	return {
		values,
		getItemSync: (key) => values.get(key) ?? null,
		setItemSync: (key, value) => {
			values.set(key, value);
		},
	};
}
describe("native hub upgrade storage", () => {
	it("isolates validated checkpoints by hub", () => {
		const raw = fixture();
		const storage = new HubUpgradeRepository(raw);
		storage.write({ hubId: "a", pending: true, startedAt: 1, response });
		expect(storage.read("a")?.response).toEqual(response);
		expect(storage.read("b")).toBeNull();
	});
	it("rejects malformed checkpoints", () => {
		const raw = fixture();
		raw.values.set(
			"evener:hub-upgrade:a",
			JSON.stringify({ hubId: "b", pending: true, startedAt: 1 }),
		);
		expect(() => new HubUpgradeRepository(raw).read("a")).toThrow(
			"Invalid upgrade checkpoint",
		);
	});
});
