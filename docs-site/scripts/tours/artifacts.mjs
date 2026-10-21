import { readFile, readdir, lstat } from 'node:fs/promises';
import { resolve, join, relative, isAbsolute, sep, dirname } from 'node:path';
import { devNull } from 'node:os';
import { fileURLToPath } from 'node:url';
import { execFileSync } from 'node:child_process';
import { createHash } from 'node:crypto';
import { inflateSync } from 'node:zlib';

export const repositoryRoot = fileURLToPath(new URL('../../../', import.meta.url));
export const scenarios = ['receipt', 'daily', 'configuration', 'extensions'];
export const surfaces = ['cli', 'web'];
export const checkpoints = ['entry', 'action', 'verified'];
export const fullMatrix = scenarios.flatMap(scenario => surfaces.flatMap(surface => checkpoints.map(checkpoint => ({ scenario, surface, checkpoint }))));
export const sha256 = bytes => createHash('sha256').update(bytes).digest('hex');
// Process output is untrusted. Return only fixed vocabulary and validated Go
// test locations, never error prose, paths, URLs, or driver argument values.
export function classifyTourFailure(output) {
 const summary={step:'unknown',code:'unknown',location:'unknown'};
 const files=['tutorial_config_test.go','tutorial_config_identity_test.go','tutorial_daily_test.go','tutorial_extensions_test.go','tutorial_receipt_test.go','tutorial_tours_test.go','tutorial_support_test.go'];
 const actions=['open','configure','start','capture','close','send','stop','steer'];
 const phases=['browser launch','authentication','new session','directory-open','directory-path','folder-confirm','model selection'];
 for(const line of output.split('\n')) {
  const location=/^\s+([a-z_]+\.go):([1-9][0-9]{0,5}): /.exec(line);
  if(location&&files.includes(location[1])&&!/checkpoint (entry|action|verified) passed$/.test(line)&&summary.location==='unknown')summary.location=`${location[1]}:${Number(location[2])}`;
  for(const action of actions) {
   const prefix=`Web driver action failed: ${action} (`;
   if(!line.includes(prefix))continue;
   for(const phase of phases)if(line.includes(`${prefix}${phase})`)||line.includes(`${prefix}${phase}; private diagnostics: `)) {
    summary.step=phase;summary.code='web-driver-failed';
   }
   for(const [category,code] of [['TimeoutError','timeout'],['ambiguous locator','ambiguous-locator'],['Error','web-driver-failed'],['AssertionError','assertion-failed']])if(line.includes(`${prefix}${category})`)) {
    summary.step=action;summary.code=code;
   }
  }
  if(line.includes('context deadline exceeded'))summary.code='deadline-exceeded';
 }
 return summary;
}
const id = row => `${row.scenario}/${row.surface}/${row.checkpoint}`;
export class CandidateValidationError extends Error {
 constructor(message, code) { super(message); this.code = code; }
}
function insist(condition, message, code) { if (!condition) throw code ? new CandidateValidationError(message, code) : new Error(message); }
// Explicit-root local Git operations must not inherit another repository or
// caller configuration. Keep repository-local configuration and hooks intact.
export function gitEnvironment(root) {
 const env = Object.fromEntries(Object.entries(process.env).filter(([key]) => !key.toUpperCase().startsWith('GIT_')));
 return { ...env, GIT_CONFIG_GLOBAL: devNull, GIT_CONFIG_SYSTEM: devNull, GIT_CONFIG_NOSYSTEM: '1', GIT_CEILING_DIRECTORIES: dirname(resolve(root)) };
}
export function revision(root = repositoryRoot) {
 return execFileSync('git', ['-C', root, 'rev-parse', 'HEAD'], { encoding: 'utf8', stdio: 'pipe', env: gitEnvironment(root) }).trim();
}
// Hash working bytes, including untracked active sources. Native is deferred.
export async function sourceTreeDigest(root = repositoryRoot) {
 const paths = execFileSync('git', ['-C', root, 'ls-files', '-z', '--cached', '--others', '--exclude-standard'], { maxBuffer: 32 * 1024 * 1024, stdio: 'pipe', env: gitEnvironment(root) }).toString().split('\0').filter(Boolean);
 const relevant = p => /^(agent\/|appwire\/|appwire-client\/|auth\/|buildinfo\/|cmd\/|cmdutil\/|execsupport\/|frontmatter\/|hubapi\/|identifier\/|internal\/|invariant\/|llm\/|rendezvous\/|server\/|test\/|envvars\/|scripts\/|make\/|Makefile$|docs\/examples\/|go\.(mod|sum|work|work.sum)$|docs-site\/(package(-lock)?\.json|scripts\/tours\/))/.test(p) && !/^docs-site\/scripts\/tours\/(native[^/]*|xctest\/)/.test(p);
 const digest = createHash('sha256');
 for (const path of [...new Set(paths)].filter(relevant).sort()) {
  let bytes;
  try { const stat = await lstat(join(root,path)); insist(stat.isFile(), `Source is not a regular file: ${path}`); bytes = await readFile(join(root,path)); }
  catch (error) { if (error.code !== 'ENOENT') throw error; bytes = Buffer.from('deleted'); }
  digest.update(JSON.stringify([path, bytes.length])).update(bytes);
 }
 return digest.digest('hex');
}

export async function rejectSymlinks(path) {
 const absolute = resolve(path); const parts = absolute.split(sep); let current = sep;
 for (const part of parts.filter(Boolean)) {
  current = join(current,part);
  try { insist(!(await lstat(current)).isSymbolicLink(), 'Symlink path refused'); }
  catch(error) { if(error.code !== 'ENOENT') throw error; }
 }
}
function scanText(text) {
 insist(!/credential[-_ ]?canary|secret[-_ ]?canary|sk-[a-zA-Z0-9_-]+|Bearer\s+(?!\[REDACTED\])\S+|(?:api[_-]?key|token|secret)\s*[=:]\s*(?!\[REDACTED\])[^\s"},]+|https?:\/\/[^\s"<>]*(?:token|auth|key)=|\/(?:Users|home|private|tmp|var\/folders)\//i.test(text), 'Private data in candidate text', 'private-data');
}
export function pngDimensions(bytes) {
 insist(bytes.length > 32 && bytes.subarray(0,8).equals(Buffer.from('89504e470d0a1a0a','hex')), 'Invalid PNG signature');
 let offset=8, header, ended=false; const data=[];
 while(offset<bytes.length) {
  insist(offset+12<=bytes.length,'Truncated PNG');
  const length=bytes.readUInt32BE(offset); insist(offset+length+12<=bytes.length,'Truncated PNG chunk');
  const body=bytes.subarray(offset+4,offset+8+length); const type=body.subarray(0,4).toString();
  let crc=0xffffffff;
  for(const b of body){crc^=b;for(let i=0;i<8;i++)crc=(crc>>>1)^(crc&1?0xedb88320:0);}
  insist(((crc^0xffffffff)>>>0)===bytes.readUInt32BE(offset+8+length),'Invalid PNG CRC');
  if(!header){insist(type==='IHDR'&&length===13,'Missing PNG header');header=body.subarray(4);}
  else insist(type!=='IHDR','Duplicate PNG header');
  if(type==='IDAT')data.push(body.subarray(4));
  offset+=length+12;
  if(type==='IEND'){insist(length===0&&offset===bytes.length,'Invalid PNG end');ended=true;break;}
 }
 insist(ended&&data.length,'Incomplete PNG');
 const width=header.readUInt32BE(0),height=header.readUInt32BE(4),channels=({2:3,6:4})[header[9]];
 insist(width>0&&height>0&&width<=4096&&height<=4096&&header[8]===8&&channels&&header[10]===0&&header[11]===0&&header[12]===0,'Unsupported PNG encoding');
 const stride=width*channels+1; const raw=inflateSync(Buffer.concat(data),{maxOutputLength:stride*height});
 insist(raw.length===stride*height,'Invalid PNG pixels');
 for(let y=0;y<height;y++)insist(raw[y*stride]<=4,'Invalid PNG filter');
 return {width,height};
}
async function inventory(root, prefix='') {
 const paths=[];
 for(const entry of await readdir(join(root,prefix),{withFileTypes:true})) {
  const path=prefix?`${prefix}/${entry.name}`:entry.name;
  insist(!entry.isSymbolicLink(),'Candidate symlink refused');
  if(entry.isDirectory()) paths.push(...await inventory(root,path));
  else {insist(entry.isFile(),'Candidate special file refused');paths.push(path);}
 }
 return paths.sort();
}
export async function validateCandidate(root, required, {sourceRoot=repositoryRoot, pending=false, historicalProvenance=false}={}) {
 await rejectSymlinks(root);
 const read = async path => {
  insist(typeof path==='string' && !isAbsolute(path) && path.split('/').every(p=>p&&p!=='.'&&p!=='..') && !path.includes('\\'),'Unsafe artifact path');
  const rel=relative(resolve(root),resolve(root,path)); insist(rel&&!rel.startsWith('..')&&!isAbsolute(rel),'Artifact escaped root');
  await rejectSymlinks(join(root,path)); return readFile(join(root,path));
 };
 const raw=await read('manifest.json'); scanText(raw.toString()); const m=JSON.parse(raw);
 insist(m.version===1 && Array.isArray(m.rows) && Array.isArray(m.coverage),'Invalid manifest');
 insist(typeof m.sourceRevision==='string' && /^[a-f0-9]{40}$/.test(m.sourceRevision) && typeof m.sourceTreeDigest==='string' && /^[a-f0-9]{64}$/.test(m.sourceTreeDigest),'Invalid source provenance');
 // Publication preserves capture provenance, it does not assert a new execution.
 // Refresh and candidate review retain strict correspondence by default.
 if (!historicalProvenance) insist(m.sourceRevision===revision(sourceRoot)&&m.sourceTreeDigest===await sourceTreeDigest(sourceRoot),'Stale source correspondence','stale-source');
 insist(m.toolVersions && ['node','go','playwright'].every(k=>typeof m.toolVersions[k]==='string'&&m.toolVersions[k]),'Missing tool versions');
 insist(JSON.stringify(m.deferredSurfaces)===JSON.stringify(['native']),'Native deferral must be explicit');
 const valid=new Set(fullMatrix.map(id)), wanted=new Set(required.map(id));
 insist(wanted.size>0&&wanted.size===required.length&&[...wanted].every(k=>valid.has(k)),'Unsupported or duplicate required rows, native is deferred');
 const actual=m.rows.map(id); insist(new Set(actual).size===actual.length,'Duplicate checkpoint');
 insist(actual.length===wanted.size&&actual.every(k=>wanted.has(k)),'Incomplete or unexpected coverage');
 const coverage=m.coverage.map(id); insist(new Set(coverage).size===wanted.size&&coverage.length===wanted.size&&coverage.every(k=>wanted.has(k)),'Incorrect coverage declaration');
 insist(m.complete===(wanted.size===fullMatrix.length) || (pending && m.complete===false),'Incorrect completeness declaration');
 const declared=new Set(['manifest.json']);
 for(const row of m.rows) {
  insist(row.result==='passed'&&Array.isArray(row.assertions)&&row.assertions.length&&row.assertions.every(a=>typeof a.name==='string'&&a.name&&a.passed===true),'Failed checkpoint evidence');
  insist(row.representation===(row.surface==='cli'?'rendered-command-transcript':'web-screenshot'),'Wrong representation');
  insist(row.file===`${id(row)}.png`,'Noncanonical image path');
  const image=await read(row.file); insist(sha256(image)===row.sha256,'Image hash mismatch'); const dimensions=pngDimensions(image);
  const expected=row.surface==='cli'?{width:1200,height:1800}:{width:1280,height:900};
  insist(dimensions.width===expected.width&&dimensions.height===expected.height&&row.dimensions?.width===dimensions.width&&row.dimensions?.height===dimensions.height,'Wrong image dimensions'); declared.add(row.file);
  if(row.surface==='cli') {
   insist(row.text?.file===`${id(row)}.txt`,'Missing selectable text'); const text=await read(row.text.file);
   insist(text.length>0&&sha256(text)===row.text.sha256,'Text hash mismatch');scanText(text.toString());declared.add(row.text.file);
  } else insist(row.text===undefined,'Unexpected text asset');
 }
 insist(JSON.stringify(await inventory(root))===JSON.stringify([...declared].sort()),'Undeclared candidate asset');
 return m;
}
