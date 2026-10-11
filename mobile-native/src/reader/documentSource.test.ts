import { once } from "node:events";
import { createServer } from "node:http";
import { describe, expect, it } from "vitest";
import type { DocPort } from "@evener/appwire-client/docContent";
import { byteSize, documentKind, documentNotice, loadDocument, refHost, truncationNote } from "./documentSource";

it.each([
	[200, "markdown"],
	[403, "forbidden"],
	[404, "missing"],
	[501, "host-unsupported"],
	[503, "failed"],
] as const)("routes remote text through actual HTTP, status %d", async (status, kind) => {
	const requests: string[] = [];
	const server = createServer((request, response) => {
		requests.push(request.url ?? "");
		response.writeHead(status, { "Content-Type": "text/plain; charset=utf-8" });
		response.end("# Remote bytes\n\nUseful content.");
	});
	server.listen(0, "127.0.0.1");
	await once(server, "listening");
	try {
		const address = server.address();
		if (!address || typeof address === "string") throw new Error("expected TCP fixture");
		const port: DocPort = { origin: `http://127.0.0.1:${address.port}`, fetch };
		const result = await loadDocument(port, "h1:local:02wMz5Txv1C3Hut0M8GCeB", "/work/b/docs/a.md");
		expect(result.kind).toBe(kind);
		expect(requests).toHaveLength(1);
		const url = new URL(requests[0] ?? "", port.origin);
		expect(url.pathname).toBe("/doc/file");
		expect(url.searchParams.get("session")).toBe("h1:local:02wMz5Txv1C3Hut0M8GCeB");
		expect(url.searchParams.get("path")).toBe("/work/b/docs/a.md");
		if (status === 200)
			expect(result).toMatchObject({ text: "# Remote bytes\n\nUseful content.", title: "Remote bytes" });
	} finally {
		server.closeAllConnections();
		await new Promise<void>((resolve, reject) => server.close((error) => (error ? reject(error) : resolve())));
	}
});

function hub(respond: (url: string) => Response | Promise<Response>): DocPort & { urls: string[] } {
	const urls: string[] = [];
	return {
		origin: "https://hub.test",
		urls,
		fetch: async (url) => {
			urls.push(url);
			return respond(url);
		},
	};
}
const text = (body: string, headers: Record<string, string> = {}) =>
	new Response(body, { headers: { "Content-Type": "text/plain; charset=utf-8", ...headers } });

it("preserves actual HTTP binary bytes and explicit capped/truncated text without treating an exact cap as truncated", async () => {
	const server = createServer((request, response) => {
		const path = new URL(request.url ?? "", "http://fixture.test").searchParams.get("path");
		if (path === "/work/b/blob.bin") {
			response.writeHead(200, { "Content-Type": "application/octet-stream" });
			response.end(Buffer.from([0, 1, 2, 3]));
			return;
		}
		response.writeHead(200, {
			"Content-Type": "text/plain; charset=utf-8",
			...(path === "/work/b/truncated.txt" ? { "X-Doc-Truncated": "true", "X-Doc-Total-Size": "1363149" } : {}),
		});
		response.end("a".repeat(524288));
	});
	server.listen(0, "127.0.0.1");
	await once(server, "listening");
	try {
		const address = server.address();
		if (!address || typeof address === "string") throw new Error("expected TCP fixture");
		const port = { origin: `http://127.0.0.1:${address.port}`, fetch };
		expect(await loadDocument(port, "remote:owner", "/work/b/blob.bin")).toEqual({
			kind: "binary",
			title: "blob.bin",
			sizeBytes: 4,
		});
		const truncated = await loadDocument(port, "remote:owner", "/work/b/truncated.txt");
		expect(truncated).toMatchObject({ kind: "code", truncated: { shownBytes: 524288, totalBytes: 1363149 } });
		expect(truncated.kind === "code" ? truncated.text.length : 0).toBe(524288);
		const exact = await loadDocument(port, "remote:owner", "/work/b/exact.txt");
		expect(exact.kind === "code" ? exact.text.length : 0).toBe(524288);
		expect(exact.kind === "code" ? exact.truncated : "wrong-kind").toBeUndefined();
	} finally {
		server.closeAllConnections();
		await new Promise<void>((resolve, reject) => server.close((error) => (error ? reject(error) : resolve())));
	}
});

describe("a document's kind (ruling 19)", () => {
	it.each([
		["docs/superpowers/plans/2026-09-25-settle-race.md", "Plan"],
		["docs/superpowers/specs/2026-09-22-lanes.markdown", "Spec"],
		["README.md", "Doc"],
		["docs/design/flake-triage.MD", "Doc"],
		["agent/retirement.go", "Code"],
		["out/shot.PNG", "Image"],
		["a/b.jpeg", "Image"],
	] as const)("%s is a %s", (path, kind) => {
		expect(documentKind(path)).toBe(kind);
	});

	it("reads a ref's host", () => {
		expect(refHost("local:s-pr2138")).toBe("local");
		expect(refHost("paradise-park:abc")).toBe("paradise-park");
		expect(refHost("bare")).toBe("");
	});
});

describe("loading a document (Review Focus 5)", () => {
	it("reads a plan into blocks, titled by its first heading", async () => {
		const port = hub(() => text("# Fix the settle/drain race\n\nBoth take the tree lock.\n"));
		const document = await loadDocument(port, "local:s-pr2138", "docs/superpowers/plans/settle.md");
		expect(port.urls).toEqual([
			"https://hub.test/doc/file?format=raw&session=local%3As-pr2138&path=docs%2Fsuperpowers%2Fplans%2Fsettle.md",
		]);
		expect(document).toMatchObject({ kind: "markdown", title: "Fix the settle/drain race", lines: 3 });
		expect(document.kind === "markdown" ? document.blocks.map((block) => block.kind) : []).toEqual([
			"heading",
			"paragraph",
		]);
		expect(documentNotice(document)).toBeNull();
	});

	it("reads code as text with its line count", async () => {
		const document = await loadDocument(
			hub(() => text("package agent\n\nfunc settle() {}\n")),
			"local:s",
			"agent/retirement.go",
		);
		expect(document).toEqual({
			kind: "code",
			title: "retirement.go",
			text: "package agent\n\nfunc settle() {}\n",
			lines: 3,
		});
	});

	it("says how much of a large document it shows", async () => {
		const port = hub(() => text("a".repeat(524_288), { "X-Doc-Truncated": "true", "X-Doc-Total-Size": "1363149" }));
		const document = await loadDocument(port, "local:s", "logs/big.txt");
		expect(document.kind === "code" ? document.truncated : undefined).toEqual({
			shownBytes: 524_288,
			totalBytes: 1_363_149,
		});
		expect(truncationNote({ shownBytes: 524_288, totalBytes: 1_363_149 })).toBe("Showing the first 512 KB of 1.3 MB");
		expect(truncationNote({ shownBytes: 524_288 })).toBe("Showing the first 512 KB");
	});

	it("never shows a binary file's bytes", async () => {
		const port = hub(
			() => new Response(new Uint8Array(10), { headers: { "Content-Type": "application/octet-stream" } }),
		);
		const document = await loadDocument(port, "local:s", "build/blob.bin");
		expect(document).toEqual({ kind: "binary", title: "blob.bin", sizeBytes: 10 });
		expect(documentNotice(document)).toBe("blob.bin isn't text, so it can't be shown here (10 bytes).");
	});

	it("asks transport for another host and shows its unsupported-host outcome", async () => {
		const port = hub(() => new Response("unsupported", { status: 501 }));
		const document = await loadDocument(port, "paradise-park:abc", "docs/plan.md");
		expect(port.urls).toEqual(["https://hub.test/doc/file?format=raw&session=paradise-park%3Aabc&path=docs%2Fplan.md"]);
		expect(document.kind).toBe("host-unsupported");
		expect(documentNotice(document)).toContain("does not support document reads");
	});

	it("shows an image without reading it as text", async () => {
		const port = hub(() => text("x"));
		expect(await loadDocument(port, "paradise-park:abc", "out/shot.png")).toEqual({ kind: "image", title: "shot.png" });
		expect(port.urls).toEqual([]);
	});

	it.each([
		[404, "a.md isn't in this session's folder any more."],
		[403, "a.md is outside this session's folder, so it can't be shown."],
		[500, "a.md couldn't be loaded right now."],
	] as const)("says what a %d means in one sentence", async (status, notice) => {
		const document = await loadDocument(
			hub(() => new Response("", { status })),
			"local:s",
			"docs/a.md",
		);
		expect(documentNotice(document)).toBe(notice);
	});

	it("treats a request that never answered as a failed read", async () => {
		const document = await loadDocument(
			hub(() => {
				throw new TypeError("Network request failed");
			}),
			"local:s",
			"docs/a.md",
		);
		expect(documentNotice(document)).toBe("a.md couldn't be loaded right now.");
	});

	it("says sizes the way the spec does", () => {
		expect([0, 1, 1023, 1024, 524_288, 1_048_575, 1_048_576, 1_363_149].map(byteSize)).toEqual([
			"0 bytes",
			"1 byte",
			"1023 bytes",
			"1 KB",
			"512 KB",
			"1023 KB",
			"1 MB",
			"1.3 MB",
		]);
	});
});
