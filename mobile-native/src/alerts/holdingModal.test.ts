// Ruling 7: a sheet holds banners. Every React Native Modal goes through
// HoldingModal, so a sheet added later can't let a banner over it.
import { readdirSync, readFileSync } from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";
import ts from "typescript";
import { expect, it } from "vitest";

const SRC = fileURLToPath(new URL("..", import.meta.url));

function productionFiles(dir: string): string[] {
	return readdirSync(dir, { withFileTypes: true }).flatMap((entry) => {
		const full = path.join(dir, entry.name);
		if (entry.isDirectory()) return productionFiles(full);
		if (!/\.tsx?$/.test(entry.name) || /\.(test|testkit)\.tsx?$/.test(entry.name)) return [];
		return [full];
	});
}

/** Whether the module renders React Native's own Modal: a named import
 * (`import { Modal }`, renamed or not), or `X.Modal` through a namespace or
 * default import of react-native. */
function importsReactNativeModal(fileName: string, code: string): boolean {
	const source = ts.createSourceFile(fileName, code, ts.ScriptTarget.Latest, true);
	const namespaces = new Set<string>();
	for (const statement of source.statements) {
		if (!ts.isImportDeclaration(statement) || !ts.isStringLiteral(statement.moduleSpecifier)) continue;
		if (statement.moduleSpecifier.text !== "react-native") continue;
		const clause = statement.importClause;
		if (clause?.name) namespaces.add(clause.name.text);
		const bindings = clause?.namedBindings;
		if (bindings && ts.isNamespaceImport(bindings)) namespaces.add(bindings.name.text);
		if (bindings && ts.isNamedImports(bindings)) {
			if (bindings.elements.some((element) => (element.propertyName ?? element.name).text === "Modal")) return true;
		}
	}
	let found = false;
	const visit = (node: ts.Node) => {
		if (
			ts.isPropertyAccessExpression(node) &&
			ts.isIdentifier(node.expression) &&
			namespaces.has(node.expression.text) &&
			node.name.text === "Modal"
		)
			found = true;
		else ts.forEachChild(node, visit);
	};
	if (namespaces.size > 0) visit(source);
	return found;
}

it("finds React Native's Modal however it is imported, and nothing else", () => {
	expect(importsReactNativeModal("a.tsx", `import { Modal, View } from "react-native";`)).toBe(true);
	expect(importsReactNativeModal("b.tsx", `import { Modal as Sheet } from "react-native";`)).toBe(true);
	expect(importsReactNativeModal("c.tsx", `import * as RN from "react-native";\nconst c = <RN.Modal visible />;`)).toBe(
		true,
	);
	expect(importsReactNativeModal("d.tsx", `import * as RN from "react-native";\nconst d = <RN.View />;`)).toBe(false);
	expect(importsReactNativeModal("e.tsx", `import { HoldingModal as Modal } from "./alerts/HoldingModal";`)).toBe(
		false,
	);
});

it("every sheet holds alerts while it is up", () => {
	const offenders = productionFiles(SRC)
		.filter((file) => path.relative(SRC, file) !== path.join("alerts", "HoldingModal.tsx"))
		.filter((file) => importsReactNativeModal(file, readFileSync(file, "utf8")))
		.map((file) => path.relative(SRC, file));
	expect(offenders).toEqual([]);
});
