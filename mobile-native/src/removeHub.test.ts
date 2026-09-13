import { describe, expect, it } from "vitest";
import { removeSavedHub } from "./removeHub";

const profile = { id: "hub", name: "Studio", origin: "http://localhost:9180" };
describe("hub removal", () => {
	it("cleans a successfully removed hub even if reading the remaining list fails", async () => {
		let deleted = false;
		const result = await removeSavedHub(
			{
				remove: async () => {},
				list: async () => {
					throw new Error("read unavailable");
				},
			},
			{
				removeHub: () => {
					deleted = true;
				},
			},
			"hub",
		);
		expect(deleted).toBe(true);
		expect(result.removed).toBe(true);
		expect(result.profiles).toBeNull();
		expect(result.error).not.toBeNull();
	});

	it("cleans drafts after profile removal has completed", async () => {
		const events: string[] = [];
		const result = await removeSavedHub(
			{
				remove: async () => {
					await Promise.resolve();
					events.push("profile removed");
				},
				list: async () => [],
			},
			{
				removeHub: () => {
					events.push("drafts removed");
				},
			},
			"hub",
		);
		expect(events).toEqual(["profile removed", "drafts removed"]);
		expect(result).toEqual({ profiles: [], removed: true, error: null });
	});
	it("preserves drafts when the hub could not be removed", async () => {
		let deleted = false;
		const result = await removeSavedHub(
			{
				remove: async () => {
					throw new Error("storage unavailable");
				},
				list: async () => [profile],
			},
			{
				removeHub: () => {
					deleted = true;
				},
			},
			"hub",
		);
		expect(deleted).toBe(false);
		expect(result.profiles).toEqual([profile]);
		expect(result.error).not.toBeNull();
	});
	it("cleans drafts after partial credential removal and returns the actual remaining hubs", async () => {
		let deleted = false;
		const result = await removeSavedHub(
			{
				remove: async () => {
					throw new Error("index removed but credential delete failed");
				},
				list: async () => [],
			},
			{
				removeHub: () => {
					deleted = true;
				},
			},
			"hub",
		);
		expect(deleted).toBe(true);
		expect(result.profiles).toEqual([]);
		expect(result.error).not.toBeNull();
	});
	it("reports draft cleanup failure without putting a removed hub back in the list", async () => {
		const result = await removeSavedHub(
			{ remove: async () => {}, list: async () => [] },
			{
				removeHub: () => {
					throw new Error("disk unavailable");
				},
			},
			"hub",
		);
		expect(result.profiles).toEqual([]);
		expect(result.error).not.toBeNull();
	});
});
