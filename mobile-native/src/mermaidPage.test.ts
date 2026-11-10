import { MERMAID_FORBID_TAGS, MERMAID_SANITIZE_CONFIG } from "@evener/appwire-client";
import { afterEach, describe, expect, it, vi } from "vitest";
// `.mjs` (not `.mts`) is the repo's convention for importing a scripts/*.mts
// tool from a test: TypeScript's bundler resolution maps it to the `.mts` file
// while an explicit `.mts` specifier trips TS5097. Precedent: demo-hub.test.ts.
import { mermaidPageDefines } from "../scripts/build-mermaid-page.mjs";
import { MERMAID_PAGE_HTML } from "./generated/mermaidPage";

const mermaid = vi.hoisted(() => ({
	initialize: vi.fn(),
	render: vi.fn(async () => ({ svg: "<svg></svg>" })),
}));
vi.mock("mermaid", () => ({ default: mermaid }));
const purify = vi.hoisted(() => ({ sanitize: vi.fn((svg: string) => svg) }));
vi.mock("dompurify", () => ({ default: purify }));

// The bundle's freshness (matches a rebuild) is `make check-mermaid-freshness`,
// not a case here: rebuilding is an esbuild run over mermaid and DOMPurify,
// and every test run paid it. The assertions below are string checks on the
// committed bundle and cost nothing.
describe("MERMAID_PAGE_HTML", () => {
	it("carries the lockdown CSP meta", () => {
		expect(MERMAID_PAGE_HTML).toContain("default-src 'none'");
	});
	it("retains the bundled third-party license notices", () => {
		// esbuild's legalComments: "eof" appends a "Bundled license information"
		// block; "none" would strip these notices, which the MIT/MPL/Apache terms
		// require retaining in the shipped bundle.
		expect(MERMAID_PAGE_HTML).toContain("Bundled license information");
		expect(MERMAID_PAGE_HTML).toContain("MIT");
	});
	it("ships the remediated DOMPurify implementation", () => {
		expect(MERMAID_PAGE_HTML).toMatch(/\.version="3\.4\.17"/);
	});
	it("bakes the shared forbid list into the generated page", () => {
		// The generator substitutes the package's list as a define, so this
		// exact literal can only come from that wiring; mermaid's vendor
		// defaults carry their own different list.
		expect(MERMAID_PAGE_HTML).toContain(JSON.stringify(MERMAID_FORBID_TAGS));
	});
	it("builds the page with the shared sanitize values", () => {
		const defines = mermaidPageDefines();
		expect(JSON.parse(defines.MERMAID_FORBID_TAGS)).toEqual([...MERMAID_FORBID_TAGS]);
		expect(JSON.parse(defines.MERMAID_SANITIZE_CONFIG)).toEqual(MERMAID_SANITIZE_CONFIG);
	});
});

// The generated page's bundle also contains mermaid's own vendor defaults, so
// asserting on those strings in the asset cannot establish what the app's
// bootstrap configures. Exercise the bootstrap itself: it is the code the
// generator inlines, and the shared package is what the generator substitutes
// into it for MERMAID_FORBID_TAGS / MERMAID_SANITIZE_CONFIG.
describe("mermaid page bootstrap", () => {
	afterEach(() => {
		vi.unstubAllGlobals();
		vi.clearAllMocks();
	});

	it("initializes strict with the shared forbid list and sanitize config", async () => {
		vi.stubGlobal("MERMAID_FORBID_TAGS", MERMAID_FORBID_TAGS);
		vi.stubGlobal("MERMAID_SANITIZE_CONFIG", MERMAID_SANITIZE_CONFIG);
		type Listener = (event: { data?: unknown }) => void;
		let onMessage: Listener | undefined;
		const viewport = { setAttribute: vi.fn() };
		const container = { innerHTML: "", scrollHeight: 240 };
		const hostMessages: unknown[] = [];
		vi.stubGlobal("document", {
			addEventListener: () => {},
			querySelector: () => viewport,
			getElementById: () => container,
		});
		vi.stubGlobal("window", {
			addEventListener: (type: string, listener: Listener) => {
				if (type === "message") onMessage = listener;
			},
			ReactNativeWebView: { postMessage: (data: string) => hostMessages.push(JSON.parse(data)) },
		});
		const { bootstrapMermaidPage } = await import("./mermaidPageContent");
		bootstrapMermaidPage();
		expect(hostMessages).toEqual([{ type: "ready" }]);

		if (!onMessage) throw new Error("bootstrap did not subscribe to window messages");
		onMessage({
			data: JSON.stringify({
				type: "render",
				id: 7,
				source: "graph TD; A-->B",
				theme: { primaryColor: "#123456" },
				mode: "fit",
			}),
		});
		await vi.waitFor(() => expect(hostMessages).toContainEqual({ type: "height", value: 240, id: 7 }));

		expect(mermaid.initialize).toHaveBeenCalledWith({
			startOnLoad: false,
			securityLevel: "strict",
			theme: "default",
			themeVariables: { primaryColor: "#123456" },
			dompurifyConfig: { FORBID_TAGS: [...MERMAID_FORBID_TAGS] },
		});
		expect(purify.sanitize).toHaveBeenCalledWith("<svg></svg>", MERMAID_SANITIZE_CONFIG);
		expect(viewport.setAttribute).toHaveBeenCalledWith("content", expect.stringContaining("maximum-scale=1"));
	});
});
