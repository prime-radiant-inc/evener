import test from 'node:test';
import assert from 'node:assert/strict';
import { mkdtemp, mkdir, writeFile, readFile, rm, symlink } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { createHash } from 'node:crypto';
import { execFileSync } from 'node:child_process';
import { deflateSync } from 'node:zlib';
import * as artifacts from './tours/artifacts.mjs';
import { checkTutorialAssets } from './check.mjs';

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
 const publication={assets:['docs/assets/tutorials/manifest.json',...rows.flatMap(r=>[r.file,...(r.text?[r.text.file]:[])].map(p=>'docs/assets/tutorials/'+p))]};
 return {base,repo,root,manifest,save,publication};
}

test('publication accepts historical provenance but current candidates reject source drift',async t=>{
 const f=await fixture(t);
 execFileSync('git',['-C',f.repo,'-c','user.name=Test','-c','user.email=test@example.invalid','commit','--allow-empty','-qm','later unrelated commit']);
 await assert.rejects(artifacts.validateCandidate(f.root,required,{sourceRoot:f.repo}),/Stale source/);
 const m=await checkTutorialAssets({root:f.repo,assetRoot:f.root,publication:f.publication});
 assert.equal(m.sourceRevision,f.manifest.sourceRevision); assert.equal(m.sourceTreeDigest,f.manifest.sourceTreeDigest); assert.equal(m.rows.length,24);
 await writeFile(join(f.repo,'go.mod'),'module later\n');
 await assert.rejects(artifacts.validateCandidate(f.root,required,{sourceRoot:f.repo}),/Stale source/);
 await checkTutorialAssets({root:f.repo,assetRoot:f.root,publication:f.publication});
});
const mutations={
 'missing row':f=>f.manifest.rows.pop(),
 'missing image':f=>rm(join(f.root,f.manifest.rows[0].file)),
 'missing selectable text':f=>rm(join(f.root,f.manifest.rows[0].text.file)),
 'changed image bytes':f=>writeFile(join(f.root,f.manifest.rows[0].file),'changed'),
 'changed text bytes':f=>writeFile(join(f.root,f.manifest.rows[0].text.file),'changed'),
 'wrong representation':f=>f.manifest.rows[0].representation='web-screenshot',
 'failed evidence':f=>f.manifest.rows[0].assertions[0].passed=false,
 'empty evidence':f=>f.manifest.rows[0].assertions=[],
 'native mislabeled as verified':f=>f.manifest.rows[0].surface='native',
 'native deferral missing':f=>f.manifest.deferredSurfaces=[],
 'invalid revision':f=>f.manifest.sourceRevision='unknown',
 'invalid digest':f=>f.manifest.sourceTreeDigest='',
 'array revision':f=>f.manifest.sourceRevision=[f.manifest.sourceRevision],
 'array digest':f=>f.manifest.sourceTreeDigest=[f.manifest.sourceTreeDigest],
 'unlisted image':f=>f.publication.assets.splice(1,1),
 'unlisted manifest':f=>f.publication.assets.shift(),
 'unlisted text':f=>f.publication.assets.splice(2,1),
};
for(const [name,mutate] of Object.entries(mutations))test(`publication rejects ${name}`,async t=>{
 const f=await fixture(t);await mutate(f);await f.save();
 await assert.rejects(checkTutorialAssets({root:f.repo,assetRoot:f.root,publication:f.publication}));
});
