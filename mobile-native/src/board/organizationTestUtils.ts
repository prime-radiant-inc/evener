// A hub answering the navigation reads an organization check makes
// (organizationCheck.ts): the manifest, a session's location with its tier
// and pin section, and the pin catalog. Anything else is a write, recorded
// and answered by `answer`. Tests only.
import type { NavigationReadParams } from "@evener/appwire-client";
import { manifest, wireSnapshot } from "@evener/appwire-client/testing/navigation";
import type { ConversationClientLike } from "../../../mobile/src/services/conversation";

/** A session id localSessionId accepts (22 base62 characters). */
export const SESSION_ID = "034Kc9793pXlhHyCRXdeAk";

export function organizationHub({ tier = "archived", pinSectionId = "release" } = {}) {
	const reads: NavigationReadParams[] = [];
	const writes: { method: string; params: unknown }[] = [];
	let answer: (method: string, params: unknown) => unknown = () => {
		throw new Error("unexpected mutation");
	};
	const client = {
		onNotification: () => () => {},
		request: async (method: string, params: NavigationReadParams) => {
			if (method !== "evener/navigation/read") {
				writes.push({ method, params });
				return answer(method, params);
			}
			reads.push(params);
			if (params.resource === "location") {
				const response = wireSnapshot(
					params,
					{
						session: {
							ref: params.ref,
							session_id: String(params.ref).replace(/^local:/, ""),
							host_id: "local",
							title: "Session",
							live: true,
						},
					},
					'"location"',
					3,
					"g",
				);
				const metadata = (response.data as { metadata: Record<string, unknown> }).metadata;
				metadata.tier = tier;
				metadata.project_key = "p";
				metadata.pin_section_id = pinSectionId;
				return response;
			}
			if (params.resource === "pin_catalog")
				return wireSnapshot(
					params,
					{ pin_sections: [{ id: "release", name: "Release", count: 1 }], remaining: 0 },
					'"pins"',
					3,
					"g",
				);
			return wireSnapshot(params, manifest(), '"manifest"', 3, "g");
		},
	} as unknown as ConversationClientLike;
	return {
		client,
		reads,
		writes,
		answerWrites(handler: (method: string, params: unknown) => unknown) {
			answer = handler;
		},
	};
}
