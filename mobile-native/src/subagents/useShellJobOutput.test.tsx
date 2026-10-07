import { type ActivityJob, type AppwireClient } from "@evener/appwire-client";
import { connectJobOutputPeer, type JobOutputPeer } from "@evener/appwire-client/testing/jobOutputPeer";
import { createElement } from "react";
import { act } from "react-test-renderer";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { render, renderedText, unmountMountedTrees } from "../renderNative.testkit";
import { useShellJobOutput } from "./useShellJobOutput";

vi.mock("react-native", async () => (await import("../renderNative.testkit")).nativeModuleMock());

const JOB: ActivityJob = {
	jobId: "job_x",
	ownerSessionId: "owner",
	ownerRef: "local:owner",
	type: "shell",
	status: "completed",
	terminal: true,
	background: true,
	hasOutput: true,
	description: "shell job",
	startedAt: "2026-10-05T00:00:00Z",
	outputBytes: 6,
};
const HELLO = {
	offsetBytes: 0,
	bytesReturned: 6,
	totalBytes: 6,
	retainedStartBytes: 0,
	encoding: "utf8",
	data: "hello\n",
};

function Probe({ client, job = JOB }: { client: AppwireClient; job?: ActivityJob }) {
	const output = useShellJobOutput(client, job, true);
	return createElement("output", null, output.status === "read" ? output.text : output.status);
}

let client: AppwireClient;
let peer: JobOutputPeer;

beforeEach(async () => {
	({ client, peer } = await connectJobOutputPeer());
});
afterEach(() => {
	unmountMountedTrees();
	client.close();
});

it.each([
	["UTF8", HELLO, "hello\n"],
	[
		"base64",
		{
			offsetBytes: 100,
			bytesReturned: 5,
			totalBytes: 105,
			retainedStartBytes: 100,
			encoding: "base64",
			data: "8J+YgAo=",
		},
		"😀\n",
	],
])("renders the latest %s byte page from the actual AppwireClient", async (_encoding, page, text) => {
	const screen = render(<Probe client={client} />);
	const request = await peer.request("evener/jobs/output");
	expect(request.params).toEqual({ ref: "local:owner", jobId: "job_x" });
	await act(async () => peer.reply(request, page));
	expect(renderedText(screen)).toBe(text);
});

it("rejects a byte-count mismatch instead of displaying malformed output", async () => {
	const screen = render(<Probe client={client} />);
	const request = await peer.request("evener/jobs/output");
	await act(async () => peer.reply(request, { ...HELLO, bytesReturned: 5 }));
	expect(renderedText(screen)).toBe("failed");
});

it("handles a structured pruning error without fabricating an empty page", async () => {
	const screen = render(<Probe client={client} />);
	const request = await peer.request("evener/jobs/output");
	await act(async () =>
		peer.fail(request, "job output is no longer retained", {
			evenerErrorInfo: "jobOutputPruned",
			retainedStartBytes: 100,
			totalBytes: 105,
		}),
	);
	expect(renderedText(screen)).toBe("failed");
});

it("keeps the last good page through a failed reread and replaces it after recovery", async () => {
	const screen = render(<Probe client={client} />);
	const first = await peer.request("evener/jobs/output");
	await act(async () => peer.reply(first, HELLO));
	expect(renderedText(screen)).toBe("hello\n");
	act(() => screen.update(<Probe client={client} job={{ ...JOB, outputBytes: 7 }} />));
	const failed = await peer.request("evener/jobs/output", 1);
	await act(async () => peer.fail(failed, "output unavailable"));
	expect(renderedText(screen)).toBe("hello\n");
	act(() => screen.update(<Probe client={client} job={{ ...JOB, outputBytes: 8 }} />));
	const recovered = await peer.request("evener/jobs/output", 2);
	await act(async () =>
		peer.reply(recovered, {
			offsetBytes: 0,
			bytesReturned: 8,
			totalBytes: 8,
			retainedStartBytes: 0,
			encoding: "utf8",
			data: "resumed\n",
		}),
	);
	expect(renderedText(screen)).toBe("resumed\n");
});

it("fences a held page from the previous owner", async () => {
	const screen = render(<Probe client={client} />);
	const old = await peer.request("evener/jobs/output");
	act(() => screen.update(<Probe client={client} job={{ ...JOB, ownerRef: "local:new", ownerSessionId: "new" }} />));
	const current = await peer.request("evener/jobs/output", 1);
	expect(current.params).toEqual({ ref: "local:new", jobId: "job_x" });
	await act(async () => peer.reply(current, { ...HELLO, data: "newest", bytesReturned: 6 }));
	await act(async () => peer.reply(old, HELLO));
	expect(renderedText(screen)).toBe("newest");
});
