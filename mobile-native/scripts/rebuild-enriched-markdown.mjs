#!/usr/bin/env node
// Rebuild only Task 2's public package boundary, never the upstream monorepo.
import fs from "node:fs";
import path from "node:path";
import { createRequire } from "node:module";
import { fileURLToPath } from "node:url";

const mobileRoot = fileURLToPath(new URL("../", import.meta.url));
const packageRoot = path.join(mobileRoot, "node_modules/react-native-enriched-markdown");
const sourceFiles = [
	"src/types/MarkdownCodeTextProps.ts",
	"src/native/EnrichedMarkdownCodeText.tsx",
	"src/EnrichedMarkdownCodeTextNativeComponent.ts",
	"src/types/MarkdownDocumentSelectionProps.ts",
	"src/native/EnrichedMarkdownSelectionSurface.tsx",
	"src/EnrichedMarkdownSelectionSurfaceNativeComponent.ts",
	"src/types/MarkdownTextProps.ts",
	"src/native/EnrichedMarkdownText.tsx",
	"src/EnrichedMarkdownNativeComponent.ts",
	// The rebuilt wrapper imports this unchanged, upstream typed-only spec as JS.
	"src/EnrichedMarkdownTextNativeComponent.ts",
	"src/index.tsx",
];

function assertNotSymlink(filename) {
	if (fs.realpathSync(filename) !== filename) {
		throw new Error(`Refusing symlinked path: ${filename}`);
	}
}

function main() {
	const args = process.argv.slice(2);
	if (args.length === 1 && args[0] === "--help") {
		console.log(
			"Usage: node scripts/rebuild-enriched-markdown.mjs\nRebuild allowlisted enriched-markdown 1.0.2 JS, declarations and maps using RN 0.86.3.\nRun native codegen separately before patch capture.",
		);
		return;
	}
	if (args.length !== 0) throw new Error(`Unknown arguments: ${args.join(" ")}`);
	assertNotSymlink(path.join(mobileRoot, "node_modules"));
	assertNotSymlink(packageRoot);
	const rnRoot = path.join(mobileRoot, "node_modules/react-native");
	assertNotSymlink(rnRoot);
	for (const [root, name, version] of [
		[packageRoot, "react-native-enriched-markdown", "1.0.2"],
		[rnRoot, "react-native", "0.86.3"],
	]) {
		const installed = JSON.parse(fs.readFileSync(path.join(root, "package.json"), "utf8"));
		if (installed.name !== name || installed.version !== version) {
			throw new Error(`Expected ${name}@${version}, found ${installed.name}@${installed.version}`);
		}
	}
	const require = createRequire(path.join(mobileRoot, "package.json"));
	const ts = require("typescript");
	const babel = require("@babel/core");
	const typescriptPreset = require.resolve("@babel/preset-typescript");
	const jsxPlugin = require.resolve("@babel/plugin-transform-react-jsx");
	const codegenPlugin = require.resolve("@react-native/babel-plugin-codegen");
	const inputs = sourceFiles.map((relative) => path.join(packageRoot, relative));
	inputs.forEach(assertNotSymlink);
	const options = {
		target: ts.ScriptTarget.ESNext,
		module: ts.ModuleKind.ESNext,
		moduleResolution: ts.ModuleResolutionKind.Bundler,
		jsx: ts.JsxEmit.ReactJSX,
		strict: true,
		esModuleInterop: true,
		skipLibCheck: true,
		declaration: true,
		declarationMap: true,
		emitDeclarationOnly: true,
		noEmitOnError: true,
		rootDir: path.join(packageRoot, "src"),
		outDir: path.join(packageRoot, "lib/typescript/src"),
		types: ["react", "react-native"],
	};
	const host = ts.createCompilerHost(options);
	host.getCurrentDirectory = () => mobileRoot;
	const inputSet = new Set(inputs);
	// Unchanged imports use the actual shipped declarations. All modified source
	// dependencies must be in sourceFiles and are checked from source together.
	host.resolveModuleNames = (names, containingFile) =>
		names.map((name) => {
			const resolved = ts.resolveModuleName(name, containingFile, options, host).resolvedModule;
			if (
				!resolved ||
				inputSet.has(resolved.resolvedFileName) ||
				!resolved.resolvedFileName.startsWith(`${packageRoot}/src/`)
			)
				return resolved;
			const declaration = path.join(
				packageRoot,
				"lib/typescript",
				path.relative(packageRoot, resolved.resolvedFileName).replace(/\.tsx?$/, ".d.ts"),
			);
			assertNotSymlink(declaration);
			return { ...resolved, resolvedFileName: declaration, extension: ts.Extension.Dts };
		});
	const program = ts.createProgram(inputs, options, host);
	const formatHost = {
		getCanonicalFileName: (filename) => filename,
		getCurrentDirectory: () => mobileRoot,
		getNewLine: () => "\n",
	};
	const diagnostics = ts.getPreEmitDiagnostics(program);
	if (diagnostics.length !== 0) {
		throw new Error(ts.formatDiagnosticsWithColorAndContext(diagnostics, formatHost));
	}
	const outputs = new Map();
	const allowedDeclarations = new Set(
		sourceFiles.flatMap((source) => {
			const stem = source.slice("src/".length).replace(/\.tsx?$/, "");
			return [".d.ts", ".d.ts.map"].map((suffix) => path.join(packageRoot, "lib/typescript/src", stem + suffix));
		}),
	);
	// emit(sourceFile) avoids emitting imported, unrelated package files.
	for (const input of inputs) {
		const result = program.emit(
			program.getSourceFile(input),
			(filename, text) => {
				if (!allowedDeclarations.has(filename)) {
					throw new Error(`Declaration emit escaped the allowlist: ${filename}`);
				}
				outputs.set(filename, text);
			},
			undefined,
			true,
		);
		if (result.emitSkipped || result.diagnostics.length !== 0) {
			throw new Error(
				ts.formatDiagnosticsWithColorAndContext(result.diagnostics, formatHost) || `Declaration emit skipped: ${input}`,
			);
		}
	}
	for (const filename of allowedDeclarations) {
		if (!outputs.has(filename)) throw new Error(`Missing declaration output: ${filename}`);
	}
	const allowedRuntime = new Set(
		sourceFiles.map((source) =>
			path.join(packageRoot, "lib/module", source.slice("src/".length).replace(/\.tsx?$/, ".js")),
		),
	);
	function relativeModuleExtensions(output) {
		const rewrite = (node) => {
			const specifier = node.source;
			if (!specifier || !specifier.value.startsWith(".")) return;
			specifier.value = /\.tsx?$/.test(specifier.value)
				? specifier.value.replace(/\.tsx?$/, ".js")
				: /\.[a-z]+$/i.test(specifier.value)
					? specifier.value
					: `${specifier.value}.js`;
			const target = path.resolve(path.dirname(output), specifier.value);
			if (!allowedRuntime.has(target) && !fs.existsSync(target)) {
				throw new Error(`Missing runtime dependency: ${specifier.value} from ${output}`);
			}
		};
		return {
			visitor: {
				ImportDeclaration: ({ node }) => rewrite(node),
				ExportNamedDeclaration: ({ node }) => rewrite(node),
				ExportAllDeclaration: ({ node }) => rewrite(node),
			},
		};
	}
	for (const source of sourceFiles) {
		const input = path.join(packageRoot, source);
		const output = path.join(packageRoot, "lib/module", source.slice("src/".length).replace(/\.tsx?$/, ".js"));
		const result = babel.transformSync(fs.readFileSync(input, "utf8"), {
			filename: input,
			babelrc: false,
			configFile: false,
			presets: [[typescriptPreset, { allowDeclareFields: true }]],
			// Native specs need their typed configuration compiled before erasure.
			plugins: [codegenPlugin, [jsxPlugin, { runtime: "automatic" }], () => relativeModuleExtensions(output)],
			sourceMaps: true,
			sourceFileName: path.relative(path.dirname(output), input).split(path.sep).join("/"),
		});
		if (!result?.code || !result.map) throw new Error(`Babel produced no code/map: ${source}`);
		outputs.set(output, `${result.code}\n//# sourceMappingURL=${path.basename(output)}.map\n`);
		outputs.set(`${output}.map`, `${JSON.stringify(result.map)}\n`);
	}
	// Validate every destination before writing any output.
	for (const filename of outputs.keys()) {
		let existing = filename;
		while (!fs.existsSync(existing)) existing = path.dirname(existing);
		assertNotSymlink(existing);
	}
	for (const [filename, text] of outputs) {
		fs.mkdirSync(path.dirname(filename), { recursive: true });
		fs.writeFileSync(filename, text);
	}
	console.log(`Rebuilt ${sourceFiles.length} checked sources, ${outputs.size} allowlisted output files.`);
}

try {
	main();
} catch (error) {
	console.error(error instanceof Error ? error.message : String(error));
	process.exitCode = 1;
}
