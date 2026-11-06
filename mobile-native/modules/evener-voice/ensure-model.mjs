import { createHash } from "node:crypto";
import { readFile, writeFile } from "node:fs/promises";
import { basename, resolve } from "node:path";
import { pathToFileURL } from "node:url";

async function ensureAsset(asset, base) {
	if (basename(asset.file) !== asset.file || !asset.file.endsWith(".onnx") || !/^[a-f0-9]{64}$/.test(asset.sha256))
		throw new Error("Invalid model filename or checksum");
	const target = new URL(`./resources/${asset.file}`, import.meta.url);
	let bytes;
	try {
		bytes = await readFile(target);
	} catch (error) {
		if (error.code !== "ENOENT") throw error;
	}
	if (!bytes) {
		if (asset.source) bytes = await readFile(new URL(asset.source, base));
		else {
			const response = await fetch(asset.url);
			if (!response.ok) throw new Error(`${asset.file} download failed: ${response.status}`);
			bytes = Buffer.from(await response.arrayBuffer());
		}
		if (createHash("sha256").update(bytes).digest("hex") !== asset.sha256)
			throw new Error(`${asset.file} checksum mismatch`);
		await writeFile(target, bytes, { flag: "wx" });
	}
	if (createHash("sha256").update(bytes).digest("hex") !== asset.sha256)
		throw new Error(`Existing ${asset.file} checksum mismatch; refusing to replace it`);
	console.log(`${asset.file} checksum verified`);
	return { file: asset.file, sha256: asset.sha256 };
}

await ensureAsset({
	file: "silero_vad.onnx",
	sha256: "6b99cbfd39246b6706f98ec13c7c50c6b299181f2474fa05cbc8046acc274396",
	url: "https://raw.githubusercontent.com/snakers4/silero-vad/5cd2ba54db059f961e7545d4f211b44f005a6dfb/files/silero_vad.onnx",
});
const args = process.argv.slice(2);
if (args.length && (args.length !== 2 || args[0] !== "--wake-manifest"))
	throw new Error("usage: node ensure-model.mjs [--wake-manifest PATH]");
const manifestURL = args.length
	? pathToFileURL(resolve(args[1]))
	: new URL("./wake-models.custom.json", import.meta.url);
const manifest = JSON.parse(await readFile(manifestURL, "utf8"));
const packaged = { id: manifest.id, phrase: manifest.phrase, settings: manifest.settings };
for (const key of ["melspectrogram", "embedding", "classifier"])
	packaged[key] = await ensureAsset(manifest[key], manifestURL);
// This is a generated build input. Training artifacts are only read, never moved
// or rewritten. Do not package source URLs or a developer's absolute paths.
const output = new URL("./resources/wake-models.json", import.meta.url);
try {
	JSON.parse(await readFile(output, "utf8"));
} catch (error) {
	if (error.code !== "ENOENT") throw error;
}
await writeFile(output, `${JSON.stringify(packaged, null, 2)}\n`);
console.log(`Wake model packaged: ${manifest.id} (${manifest.phrase})`);
