import { mkdir, mkdtemp, readFile, writeFile, rm } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { dirname, isAbsolute, join, relative, resolve, sep } from 'node:path';
import { pathToFileURL } from 'node:url';
import { execFileSync } from 'node:child_process';
import { CandidateValidationError, fullMatrix, gitEnvironment, rejectSymlinks, repositoryRoot, validateCandidate } from './artifacts.mjs';

const canonical = 'docs/assets/tutorials';
class ReviewFailure extends Error {
 constructor(code) { super('Candidate review failed'); this.code = code; }
}
async function reviewStep(code, operation) {
 try { return await operation(); }
 catch (error) { throw new ReviewFailure(error instanceof CandidateValidationError ? error.code : code); }
}
function contains(parent, child) {
 const path = relative(resolve(parent),resolve(child));
 return path === '' || (!isAbsolute(path) && path !== '..' && !path.startsWith(`..${sep}`));
}
function files(manifest) {
 return ['manifest.json', ...manifest.rows.flatMap(row => [row.file, ...(row.text ? [row.text.file] : [])])].sort();
}
async function bytes(root, paths) {
 return new Map(await Promise.all(paths.map(async path => [path,await readFile(join(root,path))])));
}

// Only the disposable Git tree is mutated. The patch is a proposal, not approval.
export async function reviewCandidate({candidate, published=join(repositoryRoot,canonical), sourceRoot=repositoryRoot, out}) {
 await reviewStep('unsafe-path',async()=>{
  if (!out || !isAbsolute(out)) throw Error('An absolute new review directory is required');
  for (const root of [candidate,published,sourceRoot,out]) {
   if (!root) throw Error('Missing input directory');
   await rejectSymlinks(root);
  }
  for (const input of [candidate,published,sourceRoot]) {
   if (contains(input,out) || contains(out,input)) throw Error('Review output must not overlap an input');
  }
 });
 const fresh = await reviewStep('invalid-candidate',()=>validateCandidate(candidate,fullMatrix,{sourceRoot}));
 const previous = await reviewStep('invalid-published',()=>validateCandidate(published,fullMatrix,{sourceRoot,historicalProvenance:true}));
 const paths = files(fresh);
 const before = await bytes(published,paths), after = await bytes(candidate,paths);
 const sourceChanged = fresh.sourceTreeDigest !== previous.sourceTreeDigest;
 const {sourceRevision: freshRevision, ...freshContent} = fresh;
 const {sourceRevision: previousRevision, ...previousContent} = previous;
 const manifestContentChanged = JSON.stringify(freshContent) !== JSON.stringify(previousContent);
 const changed = paths.filter(path => path === 'manifest.json'
  ? manifestContentChanged
  : !before.get(path).equals(after.get(path)));
 // A revision-only capture keeps the published set, including its provenance.
 // Otherwise the proposed manifest is byte-for-byte the validated candidate.
 const summary = {status:changed.length ? 'review-required' : 'unchanged',sourceChanged,changedFiles:changed.map(path=>`${canonical}/${path}`).sort()};
 await reviewStep('output-unavailable',()=>mkdir(out,{mode:0o700}));
 const scratch = await mkdtemp(join(tmpdir(),'evener-tour-review-'));
 try {
  const tree = join(scratch,'tree'), template = join(scratch,'template');
  await mkdir(tree);await mkdir(template);
  const git = (...args) => execFileSync('git',['-C',tree,...args],{env:gitEnvironment(tree),stdio:'pipe',maxBuffer:64*1024*1024});
  git('init','-q',`--template=${template}`);
  for (const path of paths) {
   const destination=join(tree,canonical,path);await mkdir(dirname(destination),{recursive:true});await writeFile(destination,before.get(path));
  }
  git('add','--',canonical);
  for (const path of changed) await writeFile(join(tree,canonical,path),after.get(path));
  const patch=git('diff','--binary','--full-index','--no-ext-diff','--no-textconv','--',canonical);
  await writeFile(join(out,'refresh.patch'),patch,{mode:0o600});
  await writeFile(join(out,'summary.json'),JSON.stringify(summary,null,2)+'\n',{mode:0o600});
  await writeFile(join(out,'review.md'),`# Tutorial candidate review\n\nStatus: ${summary.status}. Relevant source changed: ${sourceChanged}.\n\nCapture revision: ${freshRevision}. Published revision: ${previousRevision}.\nCapture source digest: ${fresh.sourceTreeDigest}.\nPublished source digest: ${previous.sourceTreeDigest}.\n\nThe validated version 1 candidate covers 24 CLI/web checkpoints. Native remains deferred.\nInspect all 24 images and 12 selectable transcripts against their assertions, instructions, and privacy requirements before applying refresh.patch. Validation cannot detect private pixels or approve an image.\n\n${summary.changedFiles.map(path=>`- ${path}`).join('\n')}\n\nThis command did not apply the patch or publish files. Preserve capture provenance when committing reviewed assets.\n`,{mode:0o600});
  return summary;
 } finally {await rm(scratch,{recursive:true,force:true});}
}
async function main(args) {
 const options={};
 while(args.length) {
  const key=args.shift(),value=args.shift();
  if(!['--candidate','--published','--source-root','--out'].includes(key)||!value||options[key])throw new ReviewFailure('invalid-arguments');
  options[key]=value;
 }
 if(!options['--candidate']||!options['--out'])throw new ReviewFailure('invalid-arguments');
 const sourceRoot=options['--source-root']||repositoryRoot;
 console.log(JSON.stringify(await reviewCandidate({candidate:options['--candidate'],published:options['--published']||join(sourceRoot,canonical),sourceRoot,out:options['--out']})));
}
if(process.argv[1]&&import.meta.url===pathToFileURL(resolve(process.argv[1])).href) {
 try {await main(process.argv.slice(2));}catch(error) {
  // Only fixed categories cross the CLI boundary, never caught error messages.
  console.error(JSON.stringify({status:'failed',code:error instanceof ReviewFailure?error.code:'review-failed'}));process.exitCode=1;
 }
}
