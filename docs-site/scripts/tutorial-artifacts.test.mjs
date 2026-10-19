import test from 'node:test';
import assert from 'node:assert/strict';
import { writeFile, readFile, rm, symlink } from 'node:fs/promises';
import { join } from 'node:path';
import { createHash } from 'node:crypto';
import { independentPNG, candidateFixture } from './tours/artifact-test-support.mjs';
import * as artifacts from './tours/artifacts.mjs';
import { checkTutorialAssets } from './check.mjs';

const hash = b => createHash('sha256').update(b).digest('hex');
function png(width, height) {
 return independentPNG(width, height);
}
const required = ['receipt','daily','configuration','extensions'].flatMap(scenario => ['cli','web'].flatMap(surface => ['entry','action','verified'].map(checkpoint => ({scenario,surface,checkpoint}))));
async function fixture(t) {
 const {base,repo,root,manifest,save,git} = await candidateFixture(t, required);
 const rows = manifest.rows;
 const publication={assets:['docs/assets/tutorials/manifest.json',...rows.flatMap(r=>[r.file,...(r.text?[r.text.file]:[])].map(p=>'docs/assets/tutorials/'+p))]};
 return {base,repo,root,manifest,save,publication,git};
}

test('publication accepts historical provenance but current candidates reject source drift',async t=>{
 const f=await fixture(t);
 f.git('-c','user.name=Test','-c','user.email=test@example.invalid','commit','--allow-empty','-qm','later unrelated commit');
 await assert.rejects(artifacts.validateCandidate(f.root,required,{sourceRoot:f.repo}),/Stale source/);
 const {manifest:m,current}=await checkTutorialAssets({root:f.repo,assetRoot:f.root,publication:f.publication});
 assert.equal(m.sourceRevision,f.manifest.sourceRevision); assert.equal(m.sourceTreeDigest,f.manifest.sourceTreeDigest); assert.equal(m.rows.length,24);
 assert.equal(typeof current,'boolean','provenance is reported to the caller, not warned');
 await writeFile(join(f.repo,'go.mod'),'module later\n');
 await assert.rejects(artifacts.validateCandidate(f.root,required,{sourceRoot:f.repo}),/Stale source/);
 const later=await checkTutorialAssets({root:f.repo,assetRoot:f.root,publication:f.publication});
 assert.equal(later.current,false,'a capture from an older tree reports stale without failing');
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
