import { spawn } from 'node:child_process';
import { mkdtemp, readFile, rm, mkdir, writeFile, copyFile, stat, realpath } from 'node:fs/promises';
import { tmpdir, homedir, platform } from 'node:os';
import { join, resolve, relative, isAbsolute, dirname, sep } from 'node:path';
import { pathToFileURL } from 'node:url';
import { renderTranscript } from './transcript.mjs';
import { repositoryRoot, fullMatrix, scenarios, surfaces, checkpoints, revision, sourceTreeDigest, sha256, pngDimensions, rejectSymlinks, validateCandidate, classifyTourFailure } from './artifacts.mjs';

export async function generateCandidate({out, sourceRoot=repositoryRoot, required=fullMatrix, collect, signal}) {
 signal?.throwIfAborted();
 if(!out || !isAbsolute(out))throw Error('An absolute new --out directory is required');
 await rejectSymlinks(out);
 // No candidate belongs anywhere in the source checkout, including staging.
 const rel=relative(resolve(sourceRoot),resolve(out));
 if(rel!=='..'&&!rel.startsWith(`..${sep}`)&&!isAbsolute(rel))throw Error('Output must be outside the repository and approved assets');
 if(!(await stat(dirname(out))).isDirectory())throw Error('Output parent must exist');
 await mkdir(out,{mode:0o700}); // Exclusive creation, never merge or overwrite.
 const digest=await sourceTreeDigest(sourceRoot);
 try {
  await collect(out);
  signal?.throwIfAborted();
  if(await sourceTreeDigest(sourceRoot)!==digest)throw Error('Source changed during capture');
  const manifest=await validateCandidate(out,required,{sourceRoot,pending:true});
  signal?.throwIfAborted();
  manifest.complete=required.length===fullMatrix.length;
  await writeFile(join(out,'manifest.json'),JSON.stringify(manifest,null,2)+'\n',{mode:0o600});
  const validated=await validateCandidate(out,required,{sourceRoot});
  signal?.throwIfAborted();
  return validated;
 } catch(error) {
  // An interrupted or failed run may leave diagnostics, never complete evidence.
  try { await rejectSymlinks(join(out,'manifest.json'));const m=JSON.parse(await readFile(join(out,'manifest.json'),'utf8'));m.complete=false;await writeFile(join(out,'manifest.json'),JSON.stringify(m,null,2)+'\n'); } catch {}
  throw error;
 }
}
export async function run(command,args,options,signal) {
 signal?.throwIfAborted();
 const result=await new Promise((resolveRun,reject)=>{
  // A handled interrupt stops between bounded tours, not inside Go cleanup.
  const child=spawn(command,args,{...options,stdio:['ignore','pipe','pipe']});let stdout='',stderr='';
  child.stdout.setEncoding('utf8');child.stderr.setEncoding('utf8');
  child.stdout.on('data',data=>stdout+=data);child.stderr.on('data',data=>stderr+=data);
  child.on('error',reject);child.on('close',(exitCode,signal)=>resolveRun({exitCode,signal,stdout,stderr}));
 });
 if(result.exitCode!==0) {
  // Never put raw process output in candidates or public error messages.
  const directory=await mkdtemp(join(tmpdir(),'evener-tour-failure-'));
  const diagnosticsPath=join(directory,'process.json');
  await writeFile(diagnosticsPath,JSON.stringify(result),{mode:0o600});
  const {step,code,location}=classifyTourFailure(`${result.stdout}\n${result.stderr}`);
  const error=Error(`Command failed (${result.exitCode}); step=${step} code=${code} location=${location}; private diagnostics: ${diagnosticsPath}`);
  error.diagnosticsPath=diagnosticsPath;
  throw error;
 }
 signal?.throwIfAborted();
 return result.stdout;
}
async function collectTours(out,required,signal) {
 const privateRoot=await mkdtemp(join(tmpdir(),'evener-tour-private-'));
 try {
  const rows=[];const sourceRevision=revision();const digest=await sourceTreeDigest();
  for(const scenario of scenarios)for(const surface of surfaces) {
   if(!required.some(r=>r.scenario===scenario&&r.surface===surface))continue;
   const raw=join(privateRoot,scenario,surface);await mkdir(raw,{recursive:true,mode:0o700});
   await run('go',['test','./cmd/evener-hub','-run','^TestTutorialTours$','-count=1','-timeout=20m','-v'],{
    cwd:repositoryRoot,
    env:{...process.env,PLAYWRIGHT_BROWSERS_PATH:process.env.PLAYWRIGHT_BROWSERS_PATH||join(platform()==='darwin'?join(homedir(),'Library','Caches'):process.env.XDG_CACHE_HOME||join(homedir(),'.cache'),'ms-playwright'),EVENER_TUTORIAL_TOURS:'1',EVENER_TUTORIAL_SURFACES:surface,EVENER_TUTORIAL_SCENARIO:scenario,EVENER_TUTORIAL_OUT:raw},
   },signal);
   const result=JSON.parse(await readFile(join(raw,'result.json'),'utf8'));
   if(result.scenario!==scenario||result.surface!==surface||result.executed!==true||result.passed!==true||JSON.stringify(result.checkpoints)!==JSON.stringify(checkpoints))throw Error('Incomplete tour execution');
   await mkdir(join(out,scenario,surface),{recursive:true});
   for(const checkpoint of checkpoints) {
    const file=`${scenario}/${surface}/${checkpoint}.png`;
    if(surface==='cli')await renderTranscript(JSON.parse(await readFile(join(raw,`${checkpoint}.json`),'utf8')),join(raw,`${checkpoint}.png`));
    const bytes=await readFile(join(raw,`${checkpoint}.png`));
    const assertions=JSON.parse(await readFile(join(raw,`${checkpoint}.evidence.json`),'utf8'));
    await copyFile(join(raw,`${checkpoint}.png`),join(out,file));
    const row={scenario,surface,checkpoint,representation:surface==='cli'?'rendered-command-transcript':'web-screenshot',file,sha256:sha256(bytes),dimensions:pngDimensions(bytes),assertions,result:'passed'};
    if(surface==='cli') {const textFile=file.replace(/\.png$/,'.txt');const text=await readFile(join(raw,`${checkpoint}.txt`));await writeFile(join(out,textFile),text);row.text={file:textFile,sha256:sha256(text)};}
    rows.push(row);
   }
   console.log(`${scenario}/${surface}: executed, 3 assertion-authorized checkpoints`);
  }
  const toolVersions={node:process.version,go:(await run('go',['version'],{cwd:repositoryRoot},signal)).trim(),playwright:JSON.parse(await readFile(new URL('../../node_modules/@playwright/test/package.json',import.meta.url),'utf8')).version};
  await writeFile(join(out,'manifest.json'),JSON.stringify({version:1,sourceRevision,sourceTreeDigest:digest,toolVersions,coverage:required,complete:false,deferredSurfaces:['native'],rows},null,2)+'\n');
 } finally {await rm(privateRoot,{recursive:true,force:true});}
}
export async function createCheckRoot(parent=tmpdir()) {
 return realpath(await mkdtemp(join(parent,'evener-tour-check-')));
}
async function main(args) {
 const command=args.shift();const opts={};
 if(!['refresh','check','validate'].includes(command))throw Error('Use refresh|check|validate --surfaces cli,web [--scenario receipt|daily|configuration|extensions] --out /ABS/NEW-CANDIDATE');
 while(args.length){const key=args.shift(),value=args.shift();if(!['--surfaces','--scenario','--out'].includes(key)||!value||opts[key])throw Error('Invalid arguments');opts[key]=value;}
 const selected=opts['--surfaces']?.split(',')??[];
 if(!selected.length||new Set(selected).size!==selected.length||selected.some(s=>!surfaces.includes(s)))throw Error('Only cli,web are active, native is deferred and cannot pass');
 if(opts['--scenario']&&!scenarios.includes(opts['--scenario']))throw Error('Unknown scenario');
 if(command==='refresh'&&(opts['--scenario']||selected.length!==2))throw Error('Refresh requires all four scenarios and both cli,web surfaces');
 const required=fullMatrix.filter(r=>selected.includes(r.surface)&&(!opts['--scenario']||r.scenario===opts['--scenario']));
 if(command==='validate') {if(!opts['--out'])throw Error('--out required');const m=await validateCandidate(opts['--out'],required);console.log(`Validated ${m.rows.length} rows, complete=${m.complete}, native deferred, not publication approval`);return;}
 if(command==='refresh'&&!opts['--out'])throw Error('Refresh requires an explicit new --out');
 const controller=new AbortController(); const interrupt=()=>controller.abort();process.once('SIGINT',interrupt);process.once('SIGTERM',interrupt);
 let owned;
 try {
  if(!opts['--out']){owned=await createCheckRoot();opts['--out']=join(owned,'candidate');}
  const m=await generateCandidate({out:opts['--out'],required,signal:controller.signal,collect:out=>collectTours(out,required,controller.signal)});
  console.log(`Validated ${m.rows.length} rows, complete=${m.complete}, native deferred, not publication approval`);
 } finally {process.removeListener('SIGINT',interrupt);process.removeListener('SIGTERM',interrupt);if(owned)await rm(owned,{recursive:true,force:true});}
}
if(process.argv[1]&&import.meta.url===pathToFileURL(resolve(process.argv[1])).href) {
 try {await main(process.argv.slice(2));}catch(error){console.error(error.message);process.exitCode=1;}
}
