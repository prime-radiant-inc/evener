import { createHash } from "node:crypto";
import { readFile, writeFile } from "node:fs/promises";
const target = new URL("./resources/silero_vad.onnx", import.meta.url);
const expected = "6b99cbfd39246b6706f98ec13c7c50c6b299181f2474fa05cbc8046acc274396";
let bytes;
try {
	bytes = await readFile(target);
} catch (error) {
	if (error.code !== "ENOENT") throw error;
}
if (!bytes) {
	const response = await fetch(
		"https://raw.githubusercontent.com/snakers4/silero-vad/5cd2ba54db059f961e7545d4f211b44f005a6dfb/files/silero_vad.onnx",
	);
	if (!response.ok) throw new Error(`Silero model download failed: ${response.status}`);
	bytes = Buffer.from(await response.arrayBuffer());
	if (createHash("sha256").update(bytes).digest("hex") !== expected) throw new Error("Silero model checksum mismatch");
	await writeFile(target, bytes, { flag: "wx" });
}
if (createHash("sha256").update(bytes).digest("hex") !== expected)
	throw new Error("Existing Silero model checksum mismatch; refusing to replace it");
console.log("Silero V5 model checksum verified");
