import { describe, expect, it, vi } from "vitest";
import { type ShareDocumentDeps, ShareUnavailable, shareDocument, shareFileBaseName } from "./shareDocument";

const target = { origin: "https://hub.test", token: "secret", sessionRef: "local:abc", path: "docs/onboarding.mp4" };

type Fixture = {
	available: ReturnType<typeof vi.fn<() => Promise<boolean>>>;
	download: ReturnType<
		typeof vi.fn<(target: ShareDocumentDeps["download"] extends (t: infer P) => unknown ? P : never) => Promise<string>>
	>;
	share: ReturnType<typeof vi.fn<(url: string) => Promise<void>>>;
};

function deps(overrides: Partial<Fixture> = {}): Fixture {
	return {
		available: vi.fn(async () => true),
		download: vi.fn(async () => "file:///cache/evener-share/local_abc-onboarding.mp4"),
		share: vi.fn(async () => undefined),
		...overrides,
	};
}

describe("shareDocument", () => {
	it("downloads the document, then opens the sheet for the local file", async () => {
		const d = deps();
		await shareDocument(target, d);
		expect(d.download).toHaveBeenCalledWith(target);
		expect(d.share).toHaveBeenCalledWith("file:///cache/evener-share/local_abc-onboarding.mp4");
	});

	it("opens no sheet when the download fails", async () => {
		const d = deps({ download: vi.fn(async () => Promise.reject(new Error("gone"))) });
		await expect(shareDocument(target, d)).rejects.toThrow("gone");
		expect(d.share).not.toHaveBeenCalled();
	});

	it("downloads nothing when the platform cannot share", async () => {
		const d = deps({ available: vi.fn(async () => false) });
		await expect(shareDocument(target, d)).rejects.toBeInstanceOf(ShareUnavailable);
		expect(d.download).not.toHaveBeenCalled();
	});
});

describe("shareFileBaseName", () => {
	it("keeps the file's own name and makes it unique to the session", () => {
		expect(shareFileBaseName("local:abc", "docs/onboarding.mp4")).toBe("local_abc-docs-onboarding.mp4");
	});

	it("keeps two same-named files in different folders apart", () => {
		// The cache outlives one share, and the sheet may still be open on the
		// first file when the second downloads: a shared name would overwrite it.
		const publicReport = shareFileBaseName("local:abc", "public/report.pdf");
		const privateReport = shareFileBaseName("local:abc", "private/report.pdf");
		expect(publicReport).not.toBe(privateReport);
		expect(publicReport.endsWith(".pdf")).toBe(true);
		expect(privateReport.endsWith(".pdf")).toBe(true);
	});

	it("replaces characters a share target would stumble on", () => {
		expect(shareFileBaseName("studio:02w", "a/b c:d?.mp4")).toBe("studio_02w-a-b c_d_.mp4");
	});

	it("keeps a deeply nested name inside the filesystem's limit", () => {
		// A valid source path can be far longer than one file name may be. The
		// cache name must stay under iOS's 255-byte limit or the download's
		// final move fails and sharing never works for that document.
		const deep = `${Array.from({ length: 6 }, () => "a".repeat(90)).join("/")}/onboarding.mp4`;
		const name = shareFileBaseName("local:abc", deep);
		expect(name.length).toBeLessThanOrEqual(200);
		expect(name.endsWith(".mp4")).toBe(true);
	});

	it("keeps an unusually long extension inside the limit too", () => {
		// The extension is not exempt from the bound: a 250-character one would
		// otherwise push the name past the limit on its own.
		const longExtension = `a.${"b".repeat(250)}`;
		const name = shareFileBaseName("local:abc", `docs/${longExtension}`);
		expect(name.length).toBeLessThanOrEqual(180);
	});
});
