import { describe, expect, test } from "vitest";
import type { MutationAttachmentRef, MutationRecoveryRecord } from "@evener/appwire-client/state/mutation";
import { recoveryToNativeDraft } from "./nativeMutationRecovery";

function recovery(
	overrides: Partial<MutationRecoveryRecord<MutationAttachmentRef>> = {},
): MutationRecoveryRecord<MutationAttachmentRef> {
	return {
		version: 1,
		clientMutationId: "mutation-1",
		targetRef: "target-1",
		method: "turn/start",
		payload: { input: [] },
		attachments: [],
		optimisticDisplay: null,
		composerText: "",
		intentSequence: 1,
		createdAt: 1,
		state: "submitting",
		recoveryKind: "rejected",
		...overrides,
	};
}

describe("recoveryToNativeDraft", () => {
	test("preserves composer text, wire bytes, and stored image metadata by order", () => {
		const composerText = "  before [image 7]\nafter [image 2]  ";
		const result = recoveryToNativeDraft(
			recovery({
				composerText,
				payload: {
					input: [
						{ type: "text", text: "before (attached image 7)" },
						{ type: "image", mediaType: "image/png", data: "AQID", name: "first.png" },
						{ type: "image", mediaType: "image/jpeg", data: "BAUG", name: "second.jpg" },
					],
				},
				attachments: [
					{ presentationId: "presentation-7", marker: 7, mediaType: "image/png", name: "first.png" },
					{ presentationId: "presentation-2", marker: 2, mediaType: "image/jpeg", name: "second.jpg" },
				],
			}),
		);

		expect(result).toEqual({
			draft: composerText,
			images: [
				{
					id: "presentation-7",
					marker: 7,
					mediaType: "image/png",
					name: "first.png",
					data: "AQID",
				},
				{
					id: "presentation-2",
					marker: 2,
					mediaType: "image/jpeg",
					name: "second.jpg",
					data: "BAUG",
				},
			],
		});
	});

	test("uses payload image order rather than guessing marker joins", () => {
		const result = recoveryToNativeDraft(
			recovery({
				composerText: "[image 9] [image 4]",
				payload: {
					input: [
						{ type: "image", mediaType: "image/png", data: "first" },
						{ type: "image", mediaType: "image/png", data: "second" },
					],
				},
				attachments: [
					{ presentationId: "stored-first", marker: 9, mediaType: "image/png", name: "first" },
					{ presentationId: "stored-second", marker: 4, mediaType: "image/png", name: "second" },
				],
			}),
		);

		expect(result?.images.map(({ id, marker, data }) => ({ id, marker, data }))).toEqual([
			{ id: "stored-first", marker: 9, data: "first" },
			{ id: "stored-second", marker: 4, data: "second" },
		]);
	});

	test.each([
		["missing composer text", { composerText: undefined }],
		[
			"unsupported skill item",
			{ payload: { input: [{ type: "skill", name: "review" }] } },
		],
		[
			"attachment count mismatch",
			{
				payload: { input: [{ type: "image", mediaType: "image/png", data: "AQID" }] },
				attachments: [],
			},
		],
		[
			"incomplete image bytes",
			{
				payload: { input: [{ type: "image", mediaType: "image/png" }] },
				attachments: [{ presentationId: "image-1", marker: 1, mediaType: "image/png", name: "one" }],
			},
		],
	])("rejects %s without inventing recovery data", (_name, overrides) => {
		expect(recoveryToNativeDraft(recovery(overrides))).toBeNull();
	});
});
