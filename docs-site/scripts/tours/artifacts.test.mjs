import test from 'node:test';
import assert from 'node:assert/strict';
import { mkdtemp, mkdir, writeFile, readFile, rm, symlink } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { createHash } from 'node:crypto';
import { execFileSync } from 'node:child_process';
import { deflateSync } from 'node:zlib';
import * as artifacts from './artifacts.mjs';
import { generateCandidate } from './refresh.mjs';

const hash = b => createHash('sha256').update(b).digest('hex');
// Independent PNG fixture encoder, producing real RGB scanlines.
function png(width, height) {
 function chunk(type, data) {
  const body = Buffer.concat([Buffer.from(type), data]);
  let crc = 0xffffffff;
  for (const b of body) { crc ^= b; for (let i=0;i<8;i++) crc = (crc >>> 1) ^ (crc & 1 ? 0xedb88320 : 0); }
  const size = Buffer.alloc(4); size.writeUInt32BE(data.length);
  const end = Buffer.alloc(4); end.writeUInt32BE((crc ^ 0xffffffff) >>> 0);
  return Buffer.concat([size, body, end]);
 }
 const header = Buffer.alloc(13); header.writeUInt32BE(width); header.writeUInt32BE(height,4); header[8]=8; header[9]=2;
 return Buffer.concat([Buffer.from('89504e470d0a1a0a','hex'), chunk('IHDR',header), chunk('IDAT',deflateSync(Buffer.alloc((width*3+1)*height))), chunk('IEND',Buffer.alloc(0))]);
}
const required = ['receipt','daily','configuration','extensions'].flatMap(scenario => ['cli','web'].flatMap(surface => ['entry','action','verified'].map(checkpoint => ({scenario,surface,checkpoint}))));
async function fixture(t) {
 const base = await mkdtemp(join(tmpdir(),'tour-artifacts-test-')); t.after(()=>rm(base,{recursive:true,force:true}));
 const repo = join(base,'repo'); await mkdir(join(repo,'docs/assets/tutorials'),{recursive:true}); await mkdir(join(repo,'docs-site'),{recursive:true});
 await writeFile(join(repo,'docs/assets/tutorials/approved.png'),png(1,1)); await writeFile(join(repo,'docs-site/publication.json'),'{}');
 await writeFile(join(repo,'go.mod'),'module example\n');
 execFileSync('git',['init','-q',repo]); execFileSync('git',['-C',repo,'add','.']); execFileSync('git',['-C',repo,'-c','user.name=Test','-c','user.email=test@example.invalid','commit','-qm','fixture']);
 const root=join(base,'candidate'); await mkdir(root);
 const rows=[];
 for (const r of required) {
  const file=`${r.scenario}/${r.surface}/${r.checkpoint}.png`; await mkdir(join(root,r.scenario,r.surface),{recursive:true});
  const dimensions=r.surface==='cli'?{width:1200,height:1800}:{width:1280,height:900}; const bytes=png(dimensions.width,dimensions.height); await writeFile(join(root,file),bytes);
  const row={...r,representation:r.surface==='cli'?'rendered-command-transcript':'web-screenshot',file,sha256:hash(bytes),dimensions,assertions:[{name:'oracle',passed:true}],result:'passed'};
  if(r.surface==='cli'){row.text={file:file.replace('.png','.txt'),sha256:hash('recorded output')};await writeFile(join(root,row.text.file),'recorded output');} rows.push(row);
 }
 const manifest={version:1,sourceRevision:execFileSync('git',['-C',repo,'rev-parse','HEAD'],{encoding:'utf8'}).trim(),sourceTreeDigest:await artifacts.sourceTreeDigest(repo),toolVersions:{node:process.version,go:'go1.26',playwright:'1.56.1'},coverage:required,complete:true,deferredSurfaces:['native'],rows};
 const save=()=>writeFile(join(root,'manifest.json'),JSON.stringify(manifest)); await save();
 return {base,repo,root,manifest,save};
}
test('validates all 24 rows and rejects a native requirement',async t=>{const f=await fixture(t);assert.equal((await artifacts.validateCandidate(f.root,required,{sourceRoot:f.repo})).rows.length,24);await assert.rejects(artifacts.validateCandidate(f.root,[{scenario:'receipt',surface:'native',checkpoint:'entry'}],{sourceRoot:f.repo}));});
const mutations={
 'missing row':async f=>f.manifest.rows.pop(),
 'missing image':async f=>rm(join(f.root,f.manifest.rows[0].file)),
 'missing text':async f=>rm(join(f.root,f.manifest.rows[0].text.file)),
 'failed assertion':async f=>f.manifest.rows[0].assertions[0].passed=false,
 'empty assertions':async f=>f.manifest.rows[0].assertions=[],
 'failed scenario':async f=>f.manifest.rows[0].result='failed',
 'zero bytes':async f=>writeFile(join(f.root,f.manifest.rows[0].file),''),
 'invalid PNG with matching hash':async f=>{const b=Buffer.from('not PNG');await writeFile(join(f.root,f.manifest.rows[0].file),b);f.manifest.rows[0].sha256=hash(b);},
 'truncated PNG with matching hash':async f=>{const b=(await readFile(join(f.root,f.manifest.rows[0].file))).subarray(0,40);await writeFile(join(f.root,f.manifest.rows[0].file),b);f.manifest.rows[0].sha256=hash(b);},
 'wrong dimensions':async f=>f.manifest.rows[0].dimensions.width=1,
 'wrong hash':async f=>f.manifest.rows[0].sha256='0'.repeat(64),
 'wrong text hash':async f=>f.manifest.rows[0].text.sha256='0'.repeat(64),
 'duplicate IDs':async f=>f.manifest.rows.push(f.manifest.rows[0]),
 'stale dirty source':async f=>writeFile(join(f.repo,'go.mod'),'module changed\n'),
 'extra asset':async f=>writeFile(join(f.root,'extra.txt'),'extra'),
 'traversal':async f=>f.manifest.rows[0].file='../escape.png',
 'symlink':async f=>{const p=join(f.root,f.manifest.rows[0].file);await rm(p);await symlink(join(f.repo,'docs/assets/tutorials/approved.png'),p);},
 'manifest canary':async f=>f.manifest.note='credential-canary-123',
 'transcript canary':async f=>{const s='credential-canary-123';await writeFile(join(f.root,f.manifest.rows[0].text.file),s);f.manifest.rows[0].text.sha256=hash(s);},
 'private path':async f=>f.manifest.note='/Users/private/home',
 'false completeness':async f=>f.manifest.complete=false,
};
for(const [name,mutate] of Object.entries(mutations)) test(`rejects ${name}`,async t=>{const f=await fixture(t);await mutate(f);await f.save();await assert.rejects(artifacts.validateCandidate(f.root,required,{sourceRoot:f.repo}));});
test('source digest includes dirty and untracked active source but excludes deferred native',async t=>{const f=await fixture(t);const before=await artifacts.sourceTreeDigest(f.repo);await mkdir(join(f.repo,'agent'));await writeFile(join(f.repo,'agent/new.go'),'package agent');assert.notEqual(await artifacts.sourceTreeDigest(f.repo),before);const active=await artifacts.sourceTreeDigest(f.repo);await mkdir(join(f.repo,'mobile-native'));await writeFile(join(f.repo,'mobile-native/a.ts'),'native');assert.equal(await artifacts.sourceTreeDigest(f.repo),active);});
test('source digest covers runtime dependencies outside agent',async t=>{const f=await fixture(t);const before=await artifacts.sourceTreeDigest(f.repo);await mkdir(join(f.repo,'llm'));await writeFile(join(f.repo,'llm/provider.go'),'package llm');assert.notEqual(await artifacts.sourceTreeDigest(f.repo),before);});
test('cancelled generation cannot mark a collected candidate complete',async t=>{
 const f=await fixture(t);const controller=new AbortController();const out=join(f.base,'cancelled');
 const publicationFile=join(f.repo,'docs-site/publication.json');const before=await readFile(publicationFile);
 const image=join(f.repo,'docs/assets/tutorials/approved.png');const imageBefore=await readFile(image);
 await assert.rejects(generateCandidate({out,sourceRoot:f.repo,required,signal:controller.signal,collect:async destination=>{const {cp}=await import('node:fs/promises');await cp(f.root,destination,{recursive:true});controller.abort();}}));
 assert.equal(JSON.parse(await readFile(join(out,'manifest.json'),'utf8')).complete,false);
 assert.deepEqual(await readFile(publicationFile),before);
 assert.deepEqual(await readFile(image),imageBefore);
 assert.equal(hash(await readFile(image)),hash(imageBefore));
});
test('failed generation never rewrites a symlinked manifest target',async t=>{
 const f=await fixture(t);const publicationFile=join(f.repo,'docs-site/publication.json');const before=await readFile(publicationFile);
 await assert.rejects(generateCandidate({out:join(f.base,'linked-manifest'),sourceRoot:f.repo,required,collect:async destination=>{await symlink(publicationFile,join(destination,'manifest.json'));}}));
 assert.deepEqual(await readFile(publicationFile),before);
});
test('subset coverage stays incomplete and cannot satisfy the full requirement',async t=>{
 const f=await fixture(t);const subset=required.filter(r=>r.scenario==='receipt'&&r.surface==='cli');
 for(const row of f.manifest.rows.slice(3)){await rm(join(f.root,row.file));if(row.text)await rm(join(f.root,row.text.file));}
 f.manifest.rows=f.manifest.rows.slice(0,3);f.manifest.coverage=subset;f.manifest.complete=false;await f.save();
 assert.equal((await artifacts.validateCandidate(f.root,subset,{sourceRoot:f.repo})).complete,false);
 await assert.rejects(artifacts.validateCandidate(f.root,required,{sourceRoot:f.repo}));
});
test('refuses unsafe output and preserves approved bytes through success, failure and interruption',async t=>{
 const f=await fixture(t);const publicationFile=join(f.repo,'docs-site/publication.json');const before=await readFile(publicationFile);const image=join(f.repo,'docs/assets/tutorials/approved.png');const imageHash=hash(await readFile(image));
 await symlink(f.base,join(f.base,'link'));
 for(const out of [f.root,join(f.repo,'..candidate'),join(f.repo,'docs/assets/tutorials/new'),join(f.repo,'docs-site/.vitepress/dist/new'),join(f.base,'link/new')]) {
  let called=false;
  await assert.rejects(generateCandidate({out,sourceRoot:f.repo,required,collect:async()=>{called=true;throw Error('must not execute');}}));
  assert.equal(called,false);
 }
 for(const outcome of ['success','failure','interrupted']) {
  const out=join(f.base,outcome);
  const collect=async destination=>{if(outcome!=='success')throw Error(outcome);const {cp}=await import('node:fs/promises');await cp(f.root,destination,{recursive:true});};
  if(outcome==='success')await generateCandidate({out,sourceRoot:f.repo,required,collect});else await assert.rejects(generateCandidate({out,sourceRoot:f.repo,required,collect}));
  assert.deepEqual(await readFile(publicationFile),before);assert.equal(hash(await readFile(image)),imageHash);
 }
});
