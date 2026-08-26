import { createWriteStream } from "node:fs";
import { rename, rm } from "node:fs/promises";
import path from "node:path";
import { once } from "node:events";
import { finished } from "node:stream/promises";

let temporarySequence = 0;

async function writeChunk(stream, value) {
  if (stream.write(value)) return;
  await once(stream, "drain");
}

function serializedArrayValue(value) {
  const serialized = JSON.stringify(value);
  return serialized === undefined ? "null" : serialized;
}

export async function writeResultsJson(file, value, options = {}) {
  const createStream = options.createStream ?? createWriteStream;
  const renameFile = options.renameFile ?? rename;
  const removeFile = options.removeFile ?? rm;
  temporarySequence += 1;
  const temporary = path.join(
    path.dirname(file),
    `.${path.basename(file)}.${process.pid}.${temporarySequence}.tmp`,
  );
  let stream;
  let primaryError;
  const cleanupErrors = [];
  try {
    stream = createStream(temporary, {
      encoding: "utf8",
      flags: "wx",
    });
    await writeChunk(stream, `{"origin":${JSON.stringify(value.origin)},"results":[`);
    for (let index = 0; index < value.results.length; index += 1) {
      if (index > 0) await writeChunk(stream, ",");
      await writeChunk(stream, serializedArrayValue(value.results[index]));
    }
    await writeChunk(stream, `],"csp":${JSON.stringify(value.csp)}}\n`);
    stream.end();
    await finished(stream);
    await renameFile(temporary, file);
    return file;
  } catch (error) {
    primaryError = error;
  }

  if (stream && !stream.closed) {
    try {
      stream.destroy();
      await finished(stream).catch(() => undefined);
    } catch (error) {
      cleanupErrors.push(error);
    }
  }
  try {
    await removeFile(temporary, { force: true });
  } catch (error) {
    cleanupErrors.push(error);
  }
  if (cleanupErrors.length)
    throw new AggregateError(
      [primaryError, ...cleanupErrors],
      "results evidence write and cleanup failed",
    );
  throw primaryError;
}
