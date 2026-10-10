// The per-hub summary cache (documentSummaries.ts): one read per (session,
// path, write) shared by every chip, and one write's summary per document.
// Writing a file again forgets the older write's summary, but a newer write's
// summary stays for a screen still showing it — including when the older read
// finishes after the newer one.
import { beforeEach, expect, it, vi } from "vitest";
import type { LoadedDocument } from "./documentSource";

const harness = vi.hoisted(() => ({ read: vi.fn() }));
vi.mock("./hubDocument", () => ({ readHubDocument: harness.read }));

import { forgetDocumentSummaries, knownSummary, summaryEntry } from "./documentSummaries";

const HUB = "studio";
const ORIGIN = "https://hub.test";
const SESSION = "local:fix";
const PATH = "docs/plan.md";
const OLDER = "2026-09-26T11:00:00.000Z";
const NEWER = "2026-09-26T12:00:00.000Z";
const PLAN: LoadedDocument = { kind: "markdown", title: "Plan", text: "# Plan", blocks: [], lines: 3 };

beforeEach(() => {
	forgetDocumentSummaries(HUB);
	harness.read.mockReset();
	harness.read.mockResolvedValue(PLAN);
});

const summary = (updatedAt: string) => summaryEntry(HUB, ORIGIN, SESSION, PATH, updatedAt);
const known = (updatedAt: string) => knownSummary(HUB, SESSION, PATH, updatedAt);
const flush = () => new Promise((resolve) => setTimeout(resolve, 0));

function deferred(): { promise: Promise<LoadedDocument>; resolve: () => void } {
	let resolve!: () => void;
	const promise = new Promise<LoadedDocument>((settle) => {
		resolve = () => settle(PLAN);
	});
	return { promise, resolve };
}

it("forgets the older write's summary once the document is read at a newer write", async () => {
	summary(OLDER);
	await flush();
	expect(known(OLDER)).not.toBeNull();
	summary(NEWER);
	await flush();
	expect(known(OLDER)).toBeNull();
	expect(known(NEWER)).not.toBeNull();
});

it("keeps the newer write's summary when an older write is read later", async () => {
	summary(NEWER);
	await flush();
	summary(OLDER);
	await flush();
	expect(known(NEWER)).not.toBeNull();
});

it("does not resurrect an older write's summary when its read finishes last", async () => {
	const olderRead = deferred();
	const newerRead = deferred();
	harness.read.mockReturnValueOnce(olderRead.promise).mockReturnValueOnce(newerRead.promise);
	summary(OLDER); // left in flight
	summary(NEWER);
	newerRead.resolve();
	await flush();
	expect(known(OLDER)).toBeNull();
	olderRead.resolve();
	await flush();
	expect(known(OLDER)).toBeNull();
});
