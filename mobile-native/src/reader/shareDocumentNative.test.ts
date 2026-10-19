// The native adapter for sharing: one sheet at a time, and each share its own
// cached file. Two chips can be held in quick succession, and iOS cannot
// present a second activity sheet over the first; the second share waits for
// the first sheet to close.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

/** Lets the queued share run until it is waiting on the download. */
const settle = () => new Promise((resolve) => setTimeout(resolve, 0));

const harness = vi.hoisted(() => ({
	downloads: [] as { url: string; destination: { uri: string }; headers: Record<string, string> }[],
	shared: [] as string[],
	release: [] as (() => void)[],
	sheet: [] as (() => void)[],
}));

vi.mock("expo-file-system", () => ({
	Paths: { cache: { uri: "file:///cache" } },
	Directory: class {
		uri: string;
		exists = true;
		folders: string[] = [];
		constructor(base: { uri: string }, name: string) {
			this.uri = `${base.uri}/${name}`;
		}
		create(options: { intermediates?: boolean; idempotent?: boolean }) {
			// The real call creates the directory; the test only records that the
			// adapter asks for an idempotent create rather than an exists check.
			this.folders.push(JSON.stringify(options));
		}
	},
	File: class {
		uri: string;
		constructor(directory: { uri: string }, name: string) {
			this.uri = `${directory.uri}/${name}`;
		}
		static downloadFileAsync(url: string, destination: { uri: string }, options: { headers: Record<string, string> }) {
			harness.downloads.push({ url, destination, headers: options.headers });
			// Held open so a test can start a second share while this one runs.
			return new Promise((resolve) => {
				harness.release.push(() => resolve({ uri: destination.uri }));
			});
		}
	},
}));

vi.mock("expo-sharing", () => ({
	isAvailableAsync: async () => true,
	shareAsync: async (url: string) => {
		harness.shared.push(url);
		// Held open, like a real sheet: a test decides when it closes.
		await new Promise<void>((resolve) => harness.sheet.push(resolve));
	},
}));

const target = (path: string) => ({
	origin: "https://hub.test",
	token: "secret",
	sessionRef: "local:abc",
	path,
});

beforeEach(() => {
	harness.downloads = [];
	harness.shared = [];
	harness.release = [];
	harness.sheet = [];
	vi.resetModules();
});
afterEach(() => vi.useRealTimers());

describe("shareSessionDocument", () => {
	it("downloads the whole file with this hub's bearer token", async () => {
		const { shareSessionDocument } = await import("./shareDocumentNative");
		const sharing = shareSessionDocument(target("docs/onboarding.mp4"));
		await settle();
		harness.release.shift()?.();
		await settle();
		harness.sheet.shift()?.();
		await sharing;
		expect(harness.downloads[0]?.url).toBe(
			"https://hub.test/doc/file?format=download&session=local%3Aabc&path=docs%2Fonboarding.mp4",
		);
		expect(harness.downloads[0]?.headers).toEqual({ Authorization: "Bearer secret" });
		expect(harness.shared).toHaveLength(1);
	});

	it("gives two same-named files different cache destinations", async () => {
		const { shareSessionDocument } = await import("./shareDocumentNative");
		const first = shareSessionDocument(target("public/report.pdf"));
		await settle();
		harness.release.shift()?.();
		await settle();
		harness.sheet.shift()?.();
		await first;
		const second = shareSessionDocument(target("private/report.pdf"));
		await settle();
		harness.release.shift()?.();
		await settle();
		harness.sheet.shift()?.();
		await second;
		const [a, b] = harness.downloads.map((download) => download.destination.uri);
		expect(a).not.toBe(b);
	});

	it("waits for the first sheet to close before downloading the second", async () => {
		const { shareSessionDocument } = await import("./shareDocumentNative");
		const first = shareSessionDocument(target("a.mp4"));
		const second = shareSessionDocument(target("b.mp4"));
		await settle();
		// Only the first download has started: the second waits its turn.
		expect(harness.downloads).toHaveLength(1);
		harness.release.shift()?.();
		await settle();
		// The first file is downloaded and its sheet is open. The second share
		// still waits: a second sheet cannot be presented over the first, and a
		// second download would replace the file the sheet is offering.
		expect(harness.shared).toHaveLength(1);
		expect(harness.downloads).toHaveLength(1);
		// Closing the sheet lets the first share finish, and only then does the
		// second download start.
		harness.sheet.shift()?.();
		await first;
		await settle();
		expect(harness.downloads).toHaveLength(2);
		harness.release.shift()?.();
		await settle();
		harness.sheet.shift()?.();
		await second;
		expect(harness.shared).toHaveLength(2);
	});
});
