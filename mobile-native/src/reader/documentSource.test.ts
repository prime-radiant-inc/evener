import { describe, expect, it } from "vitest";
import type { DocPort } from "@evener/appwire-client/docContent";
import { byteSize, documentKind, documentNotice, loadDocument, refHost, truncationNote } from "./documentSource";

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

	it("doesn't ask for a document in a session on another host (S7)", async () => {
		const port = hub(() => {
			throw new Error("not called");
		});
		const document = await loadDocument(port, "paradise-park:abc", "docs/plan.md");
		expect(port.urls).toEqual([]);
		expect(documentNotice(document)).toBe("This document is on paradise-park. Open it on the host to read it.");
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
