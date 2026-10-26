import { mkdir, readdir, readFile, writeFile } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join, resolve } from 'node:path';
import { pathToFileURL } from 'node:url';

// CI evidence for a failed web tour. recordWebFailure leaves a private
// failure.json whose message and stack can carry paths and URLs. This keeps
// only allowlisted scalar fields of its phase, RPC trace and picker probe, so
// the result is safe to print and upload.
const stagePattern = /^[A-Za-z-]{1,40}$/;
const phasePattern = /^[A-Za-z -]{1,40}$/;
const text = (value, pattern) => typeof value === 'string' && pattern.test(value) ? value : null;
const flag = value => value === true;
const count = value => Number.isSafeInteger(value) ? value : null;
const rpcMethod = value => value === 'evener/path/validate' ? value : null;
const rpcEvent = value => value === 'sent' || value === 'response' ? value : null;

function rpcEntry(entry) {
 return {method: rpcMethod(entry?.method), event: rpcEvent(entry?.event), call: count(entry?.call), timeMS: count(entry?.timeMS), elapsedMS: count(entry?.elapsedMS), valid: entry?.valid === undefined ? null : flag(entry.valid), error: entry?.error === undefined ? null : flag(entry.error), errorCode: count(entry?.errorCode), requested: flag(entry?.requested)};
}

function pickerEntry(entry) {
 return {stage: text(entry?.stage, stagePattern), timeMS: count(entry?.timeMS), prevented: flag(entry?.prevented), generation: count(entry?.generation), present: flag(entry?.present), inputMatches: flag(entry?.inputMatches), pending: flag(entry?.pending), edited: flag(entry?.edited), error: flag(entry?.error), confirmEnabled: flag(entry?.confirmEnabled)};
}

export function sanitizeFailure(record) {
 const details = record?.details ?? {};
 return {
  phase: text(record?.phase, phasePattern) ?? 'unknown',
  rpc: Array.isArray(details.rpc) ? details.rpc.map(rpcEntry) : [],
  picker: Array.isArray(details.picker) ? details.picker.map(pickerEntry) : [],
 };
}

export function summarizeFailure(failure, tail = 12) {
 const lines = [`phase=${failure.phase} rpc=${failure.rpc.length} picker=${failure.picker.length}`];
 const origin = Math.min(...[...failure.rpc, ...failure.picker].map(e => e.timeMS).filter(Number.isFinite));
 const at = e => Number.isFinite(e.timeMS) ? `+${e.timeMS - origin}ms` : '+?';
 const events = [
  ...failure.rpc.map(e => ({at: e.timeMS, line: `rpc ${at(e)} ${e.event} call=${e.call} requested=${+e.requested} valid=${e.valid === null ? '-' : +e.valid} error=${e.error === null ? '-' : +e.error} code=${e.errorCode ?? '-'} elapsed=${e.elapsedMS ?? '-'}`})),
  ...failure.picker.map(e => ({at: e.timeMS, line: `picker ${at(e)} ${e.stage} gen=${e.generation} present=${+e.present} match=${+e.inputMatches} pending=${+e.pending} edited=${+e.edited} error=${+e.error} confirm=${+e.confirmEnabled} prevented=${+e.prevented}`})),
 ].sort((a, b) => (a.at ?? 0) - (b.at ?? 0));
 return [...lines, ...events.slice(-tail).map(e => e.line)].join('\n');
}

// Tours run in child processes whose TMPDIR is private, but recordWebFailure
// writes under the real /tmp, which survives them.
export async function collectDiagnostics({source = tmpdir(), out}) {
 const names = (await readdir(source)).filter(name => name.startsWith('evener-web-failure-')).sort();
 const failures = [];
 for (const name of names) {
  let record;
  try { record = JSON.parse(await readFile(join(source, name, 'failure.json'), 'utf8')); } catch { continue; }
  failures.push(sanitizeFailure(record));
 }
 await mkdir(out, {recursive: true, mode: 0o700});
 for (const [index, failure] of failures.entries()) await writeFile(join(out, `web-failure-${index + 1}.json`), JSON.stringify(failure, null, 1) + '\n', {mode: 0o600});
 return failures;
}

async function main(args) {
 const [command, flagName, out] = args;
 if (command !== 'collect' || flagName !== '--out' || !out) throw Error('Use collect --out /ABS/DIR');
 const failures = await collectDiagnostics({out: resolve(out)});
 if (!failures.length) { console.log('No web tour failure diagnostics were recorded'); return; }
 for (const failure of failures) console.log(summarizeFailure(failure));
}
if (process.argv[1] && import.meta.url === pathToFileURL(resolve(process.argv[1])).href) {
 try { await main(process.argv.slice(2)); } catch (error) { console.error(error.message); process.exitCode = 1; }
}
