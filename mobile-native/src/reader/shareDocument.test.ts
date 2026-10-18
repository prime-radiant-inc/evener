import { describe, expect, it, vi } from "vitest";
import { type ShareDocumentDeps, ShareUnavailable, shareDocument, shareFileBaseName } from "./shareDocument";

const target = { origin: "https://hub.test", token: "secret", sessionRef: "local:abc", path: "docs/onboarding.mp4" };

function deps(overrides: Partial<ShareDocumentDeps> = {}): ShareDocumentDeps & {
	available: ReturnType<typeof vi.fn>;
	download: ReturnType<typeof vi.fn>;
	share: ReturnType<typeof vi.fn>;
} {
	return {
		available: vi.fn(async () => true),
		download: vi.fn(async () => "file:///cache/evener-share/local_abc-onboarding.mp4"),
		share: vi.fn(async () => undefined),
		...overrides,
	} as never;
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
		expect(shareFileBaseName("local:abc", "docs/onboarding.mp4")).toBe("local_abc-onboarding.mp4");
	});

	it("replaces characters a share target would stumble on", () => {
		expect(shareFileBaseName("studio:02w", "a/b c:d?.mp4")).toBe("studio_02w-b c_d_.mp4");
	});
});
