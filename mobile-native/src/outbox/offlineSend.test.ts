import { expect, it } from "vitest";
import { offlineRequest } from "./offlineSend";

const target = { hubId: "hub-1", ref: "local:thread-1", threadId: "thread-1", instanceId: "instance-7" };
const input = [{ type: "text" as const, text: "sent on the train" }];

it("fences a message to the session instance the phone last saw", () => {
	expect(offlineRequest(target, "queue", input)).toEqual({
		kind: "queue",
		hubId: "hub-1",
		targetRef: "local:thread-1",
		threadId: "thread-1",
		instanceId: "instance-7",
		input,
	});
});

it("starts or resumes a session with a send, fenced the same way", () => {
	expect(offlineRequest(target, "send", input)).toMatchObject({ kind: "send", instanceId: "instance-7" });
	expect(offlineRequest(target, "resume", input)).toMatchObject({ kind: "send", instanceId: "instance-7" });
});

it.each(["send", "queue", "resume"] as const)("forwards %s context without altering encoded input", (action) => {
	const context = { composer: { text: "original [image 3]", attachments: [] }, recoverySourceIds: ["source-1"] };
	const request = offlineRequest(target, action, input, context);
	expect(request.composer).toBe(context.composer);
	expect(request.recoverySourceIds).toBe(context.recoverySourceIds);
	expect(request.input).toBe(input);
	expect(request).not.toHaveProperty("context");
});
