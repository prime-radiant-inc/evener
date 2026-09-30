// The reading-position restore in ConversationScreen used to bail when
// `readerHeader.current` was true: an explicit action that revealed the
// transcript's header (it scrolled the list to offset 0) was not a semantic
// reader position, so the saved reading anchor must not pull the list back
// down. The action that set the ref became the recovery panel, which scrolls
// nothing, so the writer went with it; the ref, its guard and four resets
// stayed behind as state nothing ever set true (issue #3433).
//
// A ref read as a guard is dead code unless something writes it true. This
// parses screens.tsx and flags every `useRef` whose only writes are the
// boolean literal `false` while it is still read: exactly the shape
// `readerHeader` had, and one the removal leaves behind in no ref.
import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import ts from "typescript";
import { expect, it } from "vitest";

const screens = fileURLToPath(new URL("./screens.tsx", import.meta.url));

// Every `const name = useRef(...)` in a file, with each `name.current` site
// split into reads and writes (the assigned text kept, so a ref written only
// `false` can be told from one written `true` or some value).
function refUses(fileName: string, code: string) {
	const source = ts.createSourceFile(fileName, code, ts.ScriptTarget.Latest, true, ts.ScriptKind.TSX);
	const names = new Set<string>();
	const collect = (node: ts.Node) => {
		if (
			ts.isVariableDeclaration(node) &&
			ts.isIdentifier(node.name) &&
			node.initializer &&
			ts.isCallExpression(node.initializer) &&
			ts.isIdentifier(node.initializer.expression) &&
			node.initializer.expression.text === "useRef"
		)
			names.add(node.name.text);
		ts.forEachChild(node, collect);
	};
	collect(source);

	const uses = new Map<string, { writes: string[]; read: boolean }>();
	for (const name of names) uses.set(name, { writes: [], read: false });

	const record = (node: ts.Node, parent: ts.Node) => {
		if (
			ts.isPropertyAccessExpression(node) &&
			ts.isIdentifier(node.expression) &&
			node.name.text === "current" &&
			uses.has(node.expression.text)
		) {
			const use = uses.get(node.expression.text)!;
			const assignment =
				ts.isBinaryExpression(parent) &&
				parent.operatorToken.kind >= ts.SyntaxKind.FirstAssignment &&
				parent.operatorToken.kind <= ts.SyntaxKind.LastAssignment &&
				parent.left === node;
			if (assignment) use.writes.push((parent as ts.BinaryExpression).right.getText(source));
			else use.read = true;
		}
		ts.forEachChild(node, (child) => record(child, node));
	};
	record(source, source);
	return uses;
}

it("screens.tsx carries no ref written only false while it is read", () => {
	const uses = refUses(screens, readFileSync(screens, "utf8"));
	const dead = [...uses]
		.filter(([, use]) => use.read && use.writes.length > 0 && use.writes.every((written) => written === "false"))
		.map(([name]) => name);
	expect(dead).toEqual([]);
});
