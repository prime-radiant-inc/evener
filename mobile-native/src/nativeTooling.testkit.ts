import { createRequire } from "node:module";

/** Follow each owner's actual Node resolution, including nested dependencies. */
export function requireFrom(ownerChain: readonly string[]): ReturnType<typeof createRequire> {
	let require = createRequire(new URL("../package.json", import.meta.url));
	for (const owner of ownerChain) require = createRequire(require.resolve(owner));
	return require;
}
