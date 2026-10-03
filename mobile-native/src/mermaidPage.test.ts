import { describe, expect, it } from "vitest";
// `.mjs` (not `.mts`) is the repo's convention for importing a scripts/*.mts
// tool from a test: TypeScript's bundler resolution maps it to the `.mts` file
// while an explicit `.mts` specifier trips TS5097. Precedent: demo-hub.test.ts.
import { buildMermaidPageHtml } from "../scripts/build-mermaid-page.mjs";
import { MERMAID_PAGE_HTML } from "./generated/mermaidPage";

describe("MERMAID_PAGE_HTML", () => {
	it("carries the lockdown CSP meta", () => {
		expect(MERMAID_PAGE_HTML).toContain("default-src 'none'");
	});
	it("initializes mermaid strict with the forbid list", () => {
		expect(MERMAID_PAGE_HTML).toContain("securityLevel");
		expect(MERMAID_PAGE_HTML).toContain("foreignObject");
	});
	it("retains the bundled third-party license notices", () => {
		// esbuild's legalComments: "eof" appends a "Bundled license information"
		// block; "none" would strip these notices, which the MIT/MPL/Apache terms
		// require retaining in the shipped bundle.
		expect(MERMAID_PAGE_HTML).toContain("Bundled license information");
		expect(MERMAID_PAGE_HTML).toContain("MIT");
	});
	it("ships the remediated DOMPurify implementation", () => {
		expect(MERMAID_PAGE_HTML).toMatch(/\.version="3\.4\.16"/);
	});
	it("is fresh (matches a rebuild)", async () => {
		expect(await buildMermaidPageHtml()).toBe(MERMAID_PAGE_HTML);
	});
});
