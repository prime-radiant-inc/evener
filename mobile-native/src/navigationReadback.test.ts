import { expect, test } from "vitest";
import type { NavigationReadParams } from "@evener/appwire-client";
import { manifest, wireV2 } from "@evener/appwire-client/testing/navigation";
import type { ConversationClientLike } from "../../mobile/src/services/conversation";
import { navigationReadback } from "./navigationReadback";

// The hub serves an omitted limit as the maximum for the resource's family
// (50 for section/pin_section, 100 for catalog/pin_catalog): a readback that
// hardcoded 50 for every kind under-fetched catalogs. Each of these four
// invalidation kinds must send no limit at all and let the hub (and
// navigationParamsToResourceKey locally) apply the right default.
test.each([
	[{ kind: "section", section: "live" }, { resource: "section", section: "live" }],
	[{ kind: "pin_catalog" }, { resource: "pin_catalog" }],
	[{ kind: "pin_section", sectionId: "pins" }, { resource: "pin_section", sectionId: "pins" }],
	[{ kind: "catalog", catalog: "projects" }, { resource: "catalog", catalog: "projects" }],
] as const)("a %j readback omits limit", async (target, expectedParams) => {
	const generation_id = "g1";
	const calls: NavigationReadParams[] = [];
	const client = {
		request: async (_method: string, params: NavigationReadParams) => {
			calls.push(params);
			if (params.resource === "manifest") return wireV2(params, manifest(), '"m"', 1, generation_id);
			return wireV2(params, { projects: [], sessions: [], remaining: 0 }, '"r"', 1, generation_id);
		},
	} as ConversationClientLike;
	await navigationReadback(client, { generation_id, targets: [target] }, () => true);
	expect(calls).toContainEqual({ ...expectedParams, representationVersion: 2 });
});
