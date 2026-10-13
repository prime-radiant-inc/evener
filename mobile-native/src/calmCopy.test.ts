// Principle 2 (spec 4): nothing asks a person to do what the app does
// itself. The app reconnects on its own and keeps every screen current, so
// no text a person can read offers Reconnect or asks them to refresh.
//
// It reads string literals, template text and JSX text through the
// TypeScript parser, so comments, identifiers and import paths never trip
// it. Text handed to console.* is for developers, and is skipped. It covers
// the phone's own code and the shared mobile code it runs (mobile/src), whose
// errors the phone shows.
import { readdirSync, readFileSync } from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";
import ts from "typescript";
import { expect, it } from "vitest";

const REPO = fileURLToPath(new URL("../..", import.meta.url));
const ROOTS = ["mobile-native/src", "mobile/src"].map((root) => path.join(REPO, root));
const FORBIDDEN = /\breconnect\b|\brefresh\b|\bpull down to retry\b|\btry again when connected\b/i;

function productionFiles(dir: string): string[] {
	return readdirSync(dir, { withFileTypes: true }).flatMap((entry) => {
		const full = path.join(dir, entry.name);
		// "generated" holds vendored bundle output (src/generated/mermaidPage.ts),
		// not authored copy; its authored source (src/mermaidPageContent.ts) stays
		// scanned. "dev" is the browser/CLI dev harnesses.
		if (entry.isDirectory()) return entry.name === "dev" || entry.name === "generated" ? [] : productionFiles(full);
		if (!/\.tsx?$/.test(entry.name) || /\.(test|testkit)\.tsx?$/.test(entry.name)) return [];
		return [full];
	});
}

// Whether the node sits anywhere inside a console.* call, nested calls
// included: text handed to console is for developers.
function inConsoleCall(node: ts.Node): boolean {
	for (let parent = node.parent; parent; parent = parent.parent) {
		if (!ts.isCallExpression(parent)) continue;
		const callee = parent.expression;
		if (
			ts.isPropertyAccessExpression(callee) &&
			ts.isIdentifier(callee.expression) &&
			callee.expression.text === "console"
		)
			return true;
	}
	return false;
}

function readableText(fileName: string, code: string): string[] {
	const source = ts.createSourceFile(
		fileName,
		code,
		ts.ScriptTarget.Latest,
		true,
		fileName.endsWith(".tsx") ? ts.ScriptKind.TSX : ts.ScriptKind.TS,
	);
	const found: string[] = [];
	const visit = (node: ts.Node) => {
		if (ts.isImportDeclaration(node) || ts.isExportDeclaration(node)) return;
		if (inConsoleCall(node)) return;
		if (ts.isStringLiteral(node) || ts.isNoSubstitutionTemplateLiteral(node) || ts.isJsxText(node))
			found.push(node.text);
		else if (ts.isTemplateExpression(node))
			found.push(node.head.text, ...node.templateSpans.map((span) => span.literal.text));
		ts.forEachChild(node, visit);
	};
	visit(source);
	return found;
}

const asks = (fileName: string, code: string) => readableText(fileName, code).filter((text) => FORBIDDEN.test(text));

it("reads the text a person sees, and nothing else", () => {
	expect(asks("a.tsx", `const a = <Pressable onPress={retry}>Reconnect</Pressable>;`)).toEqual(["Reconnect"]);
	expect(asks("b.ts", "const b = `Could not load ${name}. Refresh to try again.`;")).toEqual([
		". Refresh to try again.",
	]);
	expect(asks("c.ts", `const c = { hint: "Pull down to retry" };`)).toEqual(["Pull down to retry"]);
	expect(asks("j.ts", `const j = "Could not load models. Try again when connected.";`)).toEqual([
		"Could not load models. Try again when connected.",
	]);
	expect(asks("d.ts", `const d = "Reconnecting…";`)).toEqual([]);
	expect(asks("e.ts", `// Reconnect later\nconst e = reconcileAfterReconnect(refresh);`)).toEqual([]);
	expect(asks("f.ts", `import { refresh } from "./refresh";\nconsole.warn("refresh failed");`)).toEqual([]);
	expect(asks("g.ts", `console.warn(format("refresh failed: %s", error));`)).toEqual([]);
	expect(
		asks(
			"h.ts",
			`export const MESSAGE = "Refresh to try again.";\nexport function hint() { return "Reconnect first"; }`,
		),
	).toEqual(["Refresh to try again.", "Reconnect first"]);
	expect(asks("i.ts", `export { refresh } from "./refresh";`)).toEqual([]);
});

it("no text a person can read asks them to reconnect or refresh", () => {
	const offenders = ROOTS.flatMap(productionFiles).flatMap((file) =>
		asks(file, readFileSync(file, "utf8")).map((text) => `${path.relative(REPO, file)}: ${text.trim()}`),
	);
	expect(offenders).toEqual([]);
});
