// The native typecheck gate (`npm run check` -> tsc -p tsconfig.check.json)
// must catch an unused import or local, the way the web frontend's Biome gate
// does. Issue #3053: an unused `Connecting` import in src/hub/HostsPage.tsx
// passed the native gate and was only caught by RoboRev.
//
// This test compiles a fixture through the checked-in tsconfig.check.json, so
// it pins the real gate config rather than a hand-copied option: it fails if
// noUnusedLocals is dropped from that file and passes while it is on.
import { mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import path from "node:path";
import { fileURLToPath } from "node:url";
import ts from "typescript";
import { describe, expect, it } from "vitest";

const nativeDir = fileURLToPath(new URL("..", import.meta.url));
const configPath = path.join(nativeDir, "tsconfig.check.json");

function gateCompilerOptions(): ts.CompilerOptions {
	const read = ts.readConfigFile(configPath, (file) => readFileSync(file, "utf8"));
	if (read.error) {
		throw new Error(ts.flattenDiagnosticMessageText(read.error.messageText, "\n"));
	}
	const parsed = ts.parseJsonConfigFileContent(read.config, ts.sys, nativeDir);
	if (parsed.errors.length > 0) {
		throw new Error(parsed.errors.map((error) => ts.flattenDiagnosticMessageText(error.messageText, "\n")).join("\n"));
	}
	return parsed.options;
}

// Compiles `source` through the gate config and returns the diagnostics that
// name the fixture file.
function diagnosticsForFixture(source: string): string[] {
	const dir = mkdtempSync(path.join(tmpdir(), "native-gate-"));
	const fixture = path.join(dir, "fixture.ts");
	try {
		writeFileSync(fixture, source);
		const program = ts.createProgram([fixture], gateCompilerOptions());
		return ts
			.getPreEmitDiagnostics(program)
			.filter((diagnostic) => diagnostic.file?.fileName === fixture)
			.map((diagnostic) => ts.flattenDiagnosticMessageText(diagnostic.messageText, "\n"));
	} finally {
		rmSync(dir, { recursive: true, force: true });
	}
}

describe("native typecheck gate", () => {
	it("reports an unused import", () => {
		const diagnostics = diagnosticsForFixture('import { Connecting } from "./nowhere";\n\nexport const used = 1;\n');
		expect(diagnostics.some((message) => message.includes("'Connecting'"))).toBe(true);
	});

	it("reports an unused local", () => {
		const diagnostics = diagnosticsForFixture("const unusedLocal = 1;\n\nexport const used = 2;\n");
		expect(diagnostics.some((message) => message.includes("'unusedLocal'"))).toBe(true);
	});
});
