import { createInterface } from 'node:readline';
import { pathToFileURL } from 'node:url';
import { open } from './web.mjs';

// Private NDJSON, never an artifact or a source of oracle evidence.
export async function runDriver(input, output) {
 let driver;
 try {
  for await (const line of createInterface({ input })) {
   const { id, action, args } = JSON.parse(line);
   try {
    let data = {};
    if (action === 'open') driver = await open(args);
    else if (action === 'close') { await driver.close(); driver = undefined; }
    else data = await driver.act(action, args);
    output.write(`${JSON.stringify({ id, ok: true, data })}\n`);
   } catch (error) {
    // Browser errors contain auth URLs and local paths. Keep them private.
    const category = error.message?.includes('strict mode violation') ? 'ambiguous locator' : error.name;
    output.write(`${JSON.stringify({ id, ok: false, data: { error: `Web driver action failed: ${action} (${error.tourPhase ?? category})` } })}\n`);
    process.exitCode = 1;
    break;
   }
  }
 } finally { await driver?.close(); }
}
if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) await runDriver(process.stdin, process.stdout);
