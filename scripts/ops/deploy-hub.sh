#!/usr/bin/env bash
# Build and restart THIS worktree's existing launchd-managed hub.
# Usage: scripts/ops/deploy-hub.sh [--label LABEL] [--config PATH] [--timeout N]
# --label selects one gui/UID job, otherwise exactly one *evener* label is required.
# --config must agree with that job's configuration, it never changes the job.
# --timeout is the successor verification budget in seconds (default 30).
# The former health-destination --addr is rejected. Listener flags in the job
# are preserved, including port zero. No URL is guessed or installed as config.
# Requires Node/npm, already required by make build-hub's frontend build.
# Build replacement remains make build-hub's job. A failed restart does not
# roll back the built file or delete endpoint evidence. See the remote runbook.
# Build output is private, not printed. A failed build retains a named bounded
# diagnostic file, remove its private directory after inspection. Success cleans it.
set -euo pipefail
label=''
config=''
timeout=30
while [ "$#" -gt 0 ]; do
	case "$1" in
	--label | --config | --timeout)
		if [ "$#" -lt 2 ] || [ -z "$2" ] || [[ "$2" == --* ]]; then
			printf '%s\n' 'deploy-hub: option requires a nonempty value' >&2
			exit 2
		fi
		case "$1" in
		--label) label=$2 ;;
		--config) config=$2 ;;
		--timeout) timeout=$2 ;;
		esac
		shift 2
		;;
	-h | --help)
		while IFS= read -r line; do
			case "$line" in '#!'*) ;; '# '*) printf '%s\n' "${line#\# }" ;; *) break ;; esac
		done < "$0"
		exit 0
		;;
	*) printf '%s\n' 'deploy-hub: unknown argument, try --help' >&2; exit 2 ;;
	esac
done
case "$timeout" in '' | *[!0-9]* | 0*) printf '%s\n' 'deploy-hub: timeout must be a positive integer' >&2; exit 2 ;; esac
# A practical finite ceiling also avoids overflow in subprocess timer arithmetic.
if [ "${#timeout}" -gt 4 ] || [ "$timeout" -gt 3600 ]; then
	printf '%s\n' 'deploy-hub: timeout must be at most 3600 seconds' >&2
	exit 2
fi
if ! command -v node >/dev/null 2>&1 || ! command -v npm >/dev/null 2>&1; then
	printf '%s\n' 'deploy-hub: Node/npm are required by make build-hub and endpoint inspection' >&2
	exit 1
fi
exec node - "$label" "$config" "$timeout" <<'JS'
'use strict';
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const {isIP} = require('node:net');
const {spawn} = require('node:child_process');

let [label, explicitConfig, seconds] = process.argv.slice(2);
const uid = process.getuid();
const relevant = ['HOME', 'XDG_CONFIG_HOME', 'XDG_STATE_HOME', 'EVENER_HUB_LISTEN_ADDR'];
const inspectMS = 5000;
let stage = 'preflight', oldPID = 0, lastPID = 0, built = false, attempted = false;
const active = new Set();
function stop(child) {
    if (!child.pid) return;
    try { process.kill(-child.pid, 'SIGKILL'); } catch (e) { if (e.code !== 'ESRCH') throw e; }
}
for (const sig of ['SIGINT', 'SIGTERM', 'SIGHUP']) process.on(sig, () => {
    for (const child of active) stop(child);
    process.exit(1);
});
function need(ok, message) { if (!ok) throw new Error(message); }
// Own each child's process group, bound time and BOTH output streams, and never
// interpolate captured data into shell. Diagnostics deliberately omit raw tool
// output, in particular launchctl's secret-bearing environment and argv.
function run(file, args, options = {}) {
    return new Promise((resolve, reject) => {
        const duration = typeof options.ms === 'function' ? options.ms() : options.ms || inspectMS;
        const child = spawn(file, args, {cwd: options.cwd, env: options.env || process.env,
            detached: true, stdio: ['ignore', 'pipe', 'pipe']});
        active.add(child);
        let output = [], size = 0, failure;
        const cap = options.cap || 65536;
        const fail = message => { failure = failure || new Error(message); if (child.pid) stop(child); };
        const timer = setTimeout(() => fail('command deadline exceeded'), duration);
        const record = chunk => {
            if (options.diagnostic === undefined) return;
            try { fs.writeFileSync(options.diagnostic, chunk.subarray(0, Math.max(0, cap-size))); }
            catch (_) { fail('private build diagnostic write failed'); }
        };
        child.stdout.on('data', chunk => {
            record(chunk);
            size += chunk.length;
            if (size > cap) fail('command output limit exceeded');
            else if (options.diagnostic === undefined) output.push(chunk);
        });
        child.stderr.on('data', chunk => { record(chunk); size += chunk.length; if (size > cap) fail('command output limit exceeded'); });
        child.on('error', () => { failure = new Error('required command could not start'); });
        // A child may exit while a descendant still holds the capture pipes.
        child.on('exit', () => stop(child));
        child.on('close', code => {
            clearTimeout(timer); active.delete(child);
            if (failure) reject(failure);
            else if (code !== 0 && !options.status) reject(new Error('command failed, exit '+code));
            else resolve({text: Buffer.concat(output).toString('utf8'), code});
        });
    });
}
async function build(repo) {
    // Arbitrary compiler/tool output can contain secrets. Keep it out of the
    // terminal and separate from all service/environment inspection output.
    let directory, fd;
    try {
        directory = fs.mkdtempSync(path.join(os.tmpdir(), 'evener-deploy-build-'));
        fs.chmodSync(directory, 0o700);
        fd = fs.openSync(path.join(directory, 'build.log'), 'wx', 0o600);
        fs.fchmodSync(fd, 0o600);
    } catch (_) {
        if (fd !== undefined) fs.closeSync(fd);
        if (directory) fs.rmSync(directory, {recursive: true, force: true});
        throw new Error('cannot create private build diagnostics, no build attempted');
    }
    console.log('deploy-hub: private build diagnostics '+JSON.stringify(path.join(directory, 'build.log'))+
        ', removed on successful build, otherwise remove the private directory after inspection');
    let complete = false;
    try {
        await run('make', ['build-hub'], {cwd: repo, ms: 15*60*1000, cap: 8*1024*1024, diagnostic: fd});
        complete = true;
    } finally {
        fs.closeSync(fd);
        if (complete) { fs.unlinkSync(path.join(directory, 'build.log')); fs.rmdirSync(directory); }
    }
}
const rawText = async (file, args, options) => (await run(file, args, options)).text;
const text = async (file, args, options) => (await rawText(file, args, options)).trim();
function object(value) { return value !== null && typeof value === 'object' && !Array.isArray(value); }
function string(value) { return typeof value === 'string' && value.length > 0 && !/[\x00-\x1f\x7f]/.test(value); }
function pathname(raw) {
    // Unix path commands add one LF. Spaces are pathname bytes, not padding.
    need(raw.endsWith('\n'), 'missing pathname framing');
    const value = raw.slice(0, -1);
    need(string(value) && path.isAbsolute(value), 'invalid single pathname output');
    return value;
}
function json(raw) {
    let value;
    try { value = JSON.parse(raw); } catch (_) { throw new Error('invalid structured command output'); }
    need(object(value), 'structured command output is not an object');
    return value;
}
function absolute(value, cwd) { need(string(value), 'missing path evidence'); return path.resolve(cwd, value); }
// Filesystem lookup must precede lexical cleaning, especially for symlink/...
// Positional arguments carry selectors, no captured input is evaluated as shell.
const configIdentityScript = `
evener_path_value() {
  evener_value=$("$@" && printf '.') || return 1
  evener_value=\${evener_value%.}
  case "$evener_value" in
    *'
') evener_value=\${evener_value%?} ;;
    *) return 1 ;;
  esac
  case "$evener_value" in ''|*'
'*) return 1 ;; esac
  printf '%s\\n' "$evener_value"
}
evener_directory() { CDPATH='' cd -P -- "$1" 2>/dev/null && pwd -P; }
config_path() {
    p=$1
    n=0
    while [ -L "$p" ]; do
        n=$((n+1)); [ "$n" -le 40 ] || return 1
        link=$(evener_path_value readlink "$p") || return 1
        case "$link" in
            /*) p=$link ;;
            *) parent=$(evener_path_value dirname "$p") || return 1; p=$parent/$link ;;
        esac
    done
    [ -f "$p" ] || return 1
    parent=$(evener_path_value dirname "$p") || return 1
    parent=$(evener_path_value evener_directory "$parent") || return 1
    printf '%s/%s\\n' "$parent" "\${p##*/}"
}
config_path "$1"`;
async function configMatches(selector, canonical, options) {
    need(string(selector) && string(canonical), 'missing config path evidence');
    if (selector === canonical) return true;
    const resolved = pathname(await rawText('bash', ['-c', configIdentityScript, 'config-identity', selector], options));
    return resolved === canonical || sameFile(resolved, canonical);
}
function sameFile(a, b) {
    try {
        const left = fs.statSync(a, {bigint: true}), right = fs.statSync(b, {bigint: true});
        return left.dev === right.dev && left.ino === right.ino;
    } catch (_) { return false; }
}
function definition(raw) {
    const def = {pid: 0, cwd: '/', argv: [], env: {}};
    const seen = new Set();
    let depth = 0, block = '', inherited = {}, environment = {};
    for (const rawLine of raw.split('\n')) {
        const line = rawLine.replace(/^[ \t]+/, '');
        if (!line) continue;
        if (line === '}') { depth--; need(depth >= 0, 'ambiguous service definition'); block = ''; continue; }
        if (depth === 2 && block === 'arguments') { def.argv.push(line); continue; }
        if (depth === 2 && (block === 'environment' || block === 'inherited environment')) {
            const m = line.match(/^([A-Za-z_][A-Za-z_0-9]*) =>(?: (.*))?$/);
            need(!m || m[1] !== 'EVENER_HUB_ADDR', 'selected service contains retired listener environment');
            if (m && relevant.includes(m[1])) {
                const target = block === 'environment' ? environment : inherited;
                need(!Object.hasOwn(target, m[1]), 'duplicate relevant service environment');
                need(!/[\x00-\x1f\x7f]/.test(m[2] || ''), 'ambiguous relevant service environment');
                target[m[1]] = m[2] || '';
            }
            continue;
        }
        if (line.endsWith(' = {')) {
            const name = line.slice(0, -4);
            if (depth === 1 && ['arguments', 'environment', 'inherited environment'].includes(name)) {
                need(!seen.has(name), 'duplicate service block'); seen.add(name); block = name;
            }
            depth++; continue;
        }
        if (depth === 1) {
            const m = line.match(/^(program|working directory|pid) = (.*)$/);
            if (m) {
                need(!seen.has(m[1]), 'duplicate service field'); seen.add(m[1]);
                if (m[1] === 'pid') { need(/^[1-9][0-9]*$/.test(m[2]), 'invalid service PID'); def.pid = Number(m[2]); }
                else def[m[1] === 'program' ? 'program' : 'cwd'] = m[2];
            }
        }
    }
    need(depth === 0 && sameFile(def.program, binary) && path.isAbsolute(def.cwd) &&
        def.argv.length >= 2 && def.argv[0] === def.program && def.argv[1] === 'hub' &&
        (def.argv.length === 2 || def.argv[2].startsWith('-')) &&
        Number.isSafeInteger(def.pid), 'selected service executable or argv is not this worktree hub');
    def.env = {...inherited, ...environment};
    return def;
}
function selectors(def) {
    const result = [];
    const seen = new Set();
    // Only identify selector flags here. The executable owns config/listener
    // grammar and precedence. Preserve other launch flags in the service.
    for (let i = 2; i < def.argv.length; i++) {
        const m = def.argv[i].match(/^--?(config|addr)(?:=(.*))?$/);
        if (m) {
            need(!seen.has(m[1]), 'ambiguous repeated service selector'); seen.add(m[1]);
            const value = m[2] === undefined ? def.argv[++i] : m[2];
            need(typeof value === 'string' && value.length > 0, 'missing service selector value');
            result.push('--'+m[1], value);
        }
    }
    return result;
}
function intent(def) { return JSON.stringify([def.program, def.cwd, def.argv, relevant.map(k => [k, def.env[k]])]); }
async function service(ms = inspectMS) { return definition(await rawText('launchctl', ['print', target], {ms})); }
function processArgvMatches(actual, def, report) {
    if (actual === def.argv.join(' ')) return true;
    if (!report || report.state !== 'running' || !report.running || report.running.pid !== def.pid) return false;
    const r = report.running, bound = r.bound_addr;
    const address = typeof bound === 'string' && bound.match(/^(?:\[([^\]]+)\]|([^:]+)):(\d+)$/);
    if (!address || bound.includes('%') || !isIP(address[1] || address[2]) || Number(address[3]) < 1 || Number(address[3]) > 65535) return false;
    const ephemeral = value => typeof value === 'string' && /^(?:\[[^\]]+\]|[^:]*):0?$/.test(value);
    const expected = [...def.argv];
    let index = -1, inline = false;
    for (let i = 2; i < def.argv.length; i++) {
        const m = def.argv[i].match(/^--?addr(?:=(.*))?$/);
        if (!m) continue;
        if (index !== -1) return false;
        index = i; inline = m[1] !== undefined;
        const value = inline ? m[1] : def.argv[++i];
        if (!ephemeral(value)) return false;
    }
    if (index === -1) {
        const configured = report.listen_source === 'config' && ephemeral(report.configured_listen_addr);
        const published = r.listen_source === 'config' && ephemeral(r.requested_listen_addr);
        const env = def.env.EVENER_HUB_LISTEN_ADDR;
        const fromEnv = ephemeral(env) && ((report.listen_source === 'env' && report.configured_listen_addr === env) ||
            (r.listen_source === 'env' && r.requested_listen_addr === env));
        if (!configured && !published && !fromEnv) return false;
        expected.push('-addr', bound);
    } else if (inline) expected[index] = def.argv[index].split('=')[0]+'='+bound;
    else expected[index+1] = bound;
    return actual === expected.join(' ');
}
async function processIdentity(def, ms = inspectMS, evidence) {
    if (!def.pid) return '';
    const pid = String(def.pid);
    need(await text('ps', ['-p', pid, '-o', 'uid='], {ms}) === String(uid), 'selected service effective user differs');
    const birth = await text('ps', ['-p', pid, '-o', 'lstart='], {ms});
    need(string(birth), 'selected process birth unavailable');
    const actual = await text('ps', ['-p', pid, '-ww', '-o', 'command='], {ms});
    const cwd = (await rawText('lsof', ['-a', '-p', pid, '-d', 'cwd', '-Fn'], {ms})).split('\n').filter(s => s.startsWith('n/'));
    need(cwd.length === 1 && sameFile(cwd[0].slice(1), def.cwd), 'selected process working directory differs');
    // Fetch endpoint evidence only for a rewrite, after user/cwd checks. Keep
    // the original definition argv for selection and replacement, never ps argv.
    need(actual === def.argv.join(' ') || processArgvMatches(actual, def, evidence && await evidence()), 'selected process argv differs');
    need(await text('ps', ['-p', pid, '-o', 'lstart='], {ms}) === birth, 'selected process changed during inspection');
    return birth;
}
async function endpoint(def, ms = inspectMS) {
    const env = {PATH: process.env.PATH, ...def.env};
    const report = json(await text(binary, ['hub', 'endpoint', '--json', ...selectors(def)], {cwd: def.cwd, env, ms}));
    need(report.schema_version === 1 && ['running', 'starting', 'stopped', 'unavailable'].includes(report.state) &&
        typeof report.config_exists === 'boolean' && string(report.config_path) && string(report.hub_state_root) && path.isAbsolute(report.hub_state_root) &&
        string(report.configured_listen_addr) && ['flag', 'env', 'config', 'default'].includes(report.listen_source), 'invalid endpoint report schema or selection');
    report.config_path = absolute(report.config_path, def.cwd);
    const selected = selectors(def), configIndex = selected.indexOf('--config');
    if (configIndex >= 0) need(await configMatches(selected[configIndex+1], report.config_path, {cwd: def.cwd, env, ms}), 'endpoint config differs from service selector');
    if (explicitConfig) need(await configMatches(explicitConfig, report.config_path, {cwd: invocationCwd, ms}), 'explicit config differs from selected service');
    if (report.state === 'running') {
        const r = report.running;
        need(object(r) && r.schema_version === 1 && Number.isSafeInteger(r.pid) && r.pid > 0 && r.pid === def.pid &&
            string(r.boot_id) && string(r.started_at) && await configMatches(r.config_path, report.config_path, {cwd: def.cwd, env, ms}) &&
            r.hub_state_root === report.hub_state_root && string(r.bound_addr) && string(r.local_url) &&
            string(r.requested_listen_addr) && ['flag', 'env', 'config', 'default'].includes(r.listen_source) && string(r.version),
            'running endpoint identity differs from selected service');
    } else need(report.running === undefined || report.running === null, 'non-running report contains running identity');
    return report;
}
async function capability(def) {
    // A healthy, independently associated Running report already witnesses the
    // old generation. For damaged/unreachable metadata, require mapped vnode
    // evidence BEFORE probing disk capability, then recheck the file afterward.
    const before = fs.statSync(binary, {bigint: true});
    if (def.pid) {
        const mapped = await text('lsof', ['-a', '-p', String(def.pid), '-d', 'txt', '-F', 'Di']);
        let device = null, match = false;
        for (const line of mapped.split('\n')) {
            if (/^D(?:0x)?[0-9a-fA-F]+$/.test(line)) device = BigInt('0x'+line.slice(1).replace(/^0x/, ''));
            if (/^i[0-9]+$/.test(line) && device === before.dev && BigInt(line.slice(1)) === before.ino) match = true;
        }
        need(match, 'missing running-image capability evidence, disk pathname is not the old image');
    }
    const contract = json(await text(binary, ['launch-check', '--json'], {cwd: def.cwd, env: {PATH: process.env.PATH, ...def.env}}));
    const after = fs.statSync(binary, {bigint: true});
    need(before.dev === after.dev && before.ino === after.ino && before.size === after.size && before.mtimeNs === after.mtimeNs,
        'executable changed during capability inspection');
    need(string(contract.version) && string(contract.protocol) &&
        (contract.hub_endpoint_schema === undefined || Number.isSafeInteger(contract.hub_endpoint_schema)), 'running image capability is unknown');
    need(contract.hub_endpoint_schema === 1, 'selected old image requires manual endpoint migration');
}
let binary, target, invocationCwd = process.cwd();
(async () => {
    const repo = pathname(await rawText('git', ['rev-parse', '--show-toplevel']));
    binary = path.join(repo, 'evener');
    if (!label) {
        const listing = await text('launchctl', ['list']);
        const labels = listing.split('\n').map(s => s.trim().split(/\s+/)).filter(a => a.length === 3 && a[2].includes('evener')).map(a => a[2]);
        need(labels.length === 1, 'select exactly one launchd job with --label'); label = labels[0];
    }
    need(/^[A-Za-z0-9_.-]+$/.test(label), 'invalid launchd label');
    target = 'gui/'+uid+'/'+label;
    let def = await service(); oldPID = lastPID = def.pid;
    let predecessor;
    const launch = intent(def), birth = await processIdentity(def, inspectMS, async () => predecessor = await endpoint(def));
    try { predecessor = predecessor || await endpoint(def); }
    catch (e) {
        // Old binaries may reject the endpoint subcommand altogether. Diagnose
        // positive unsupported image capability, never infer it from stderr.
        await capability(def);
        throw e;
    }
    const selection = JSON.stringify([predecessor.config_path, predecessor.hub_state_root]);
    if (predecessor.state !== 'running') {
        need(def.pid !== 0 || predecessor.state === 'stopped', 'stopped service does not have a free selected root');
        if (def.pid) {
            need(predecessor.state !== 'stopped', 'selected process is not associated with the selected root');
            // Corroborate the root through the selected service's open lock file,
            // not a claimed metadata PID. This is NOT kernel flock attestation.
            const lock = path.join(predecessor.hub_state_root, 'hub.lock');
            const files = await rawText('lsof', ['-a', '-p', String(def.pid), '-Fn', lock]);
            need(files.split('\n').includes('n'+lock), 'missing selected process state-root evidence');
        }
        await capability(def);
    }
    const authorized = await service();
    need(intent(authorized) === launch && authorized.pid === oldPID && await processIdentity(authorized, inspectMS, async () => predecessor) === birth,
        'selected predecessor changed during authorization');
    const sha = await text('git', ['rev-parse', '--short', 'HEAD'], {cwd: repo});
    const dirty = await run('git', ['status', '--porcelain', '-uno'], {cwd: repo});
    const expected = sha + (dirty.text.trim() ? '-dirty' : '');
    console.log('deploy-hub: selected '+target+', predecessor PID '+oldPID);
    stage = 'build';
    await build(repo); built = true;
    // Atomic replacement is owned by build-hub. Do not reuse the new pathname
    // as evidence about the predecessor. The matching live witness is retained
    // only within this invocation, never cached by PID alone.
    stage = 'revalidate';
    def = await service(); lastPID = def.pid;
    need(intent(def) === launch && def.pid === oldPID && await processIdentity(def, inspectMS, async () => predecessor) === birth, 'service association changed during build, no restart attempted');
    const current = await endpoint(def);
    need(JSON.stringify([current.config_path, current.hub_state_root]) === selection, 'selected config or state root changed during build');
    if (predecessor.running && current.running) need(predecessor.running.boot_id === current.running.boot_id, 'predecessor boot changed during build');
    if (!def.pid) need(current.state === 'stopped', 'selected stopped root is no longer free');
    // The endpoint query itself takes time, so reread the service/PID once more.
    const final = await service();
    need(intent(final) === launch && final.pid === oldPID && await processIdentity(final, inspectMS, async () => predecessor) === birth, 'service changed before restart');
    stage = 'restart'; attempted = true;
    const kick = await run('launchctl', ['kickstart', '-k', target], {status: true}).catch(() => ({code: 'unknown'}));
    console.log('deploy-hub: selected kickstart status '+kick.code+', verifying successor');
    stage = 'verify';
    const deadline = Date.now()+Number(seconds)*1000;
    let lastFailure = 'no successor report';
    while (Date.now() < deadline) {
        try {
            const remaining = () => { const left = deadline-Date.now(); need(left > 0, 'verification deadline exceeded'); return Math.min(inspectMS, left); };
            const successor = await service(remaining); lastPID = successor.pid;
            need(intent(successor) === launch, 'selected service definition changed');
            let report;
            const nextBirth = await processIdentity(successor, remaining, async () => report = await endpoint(successor, remaining));
            report = report || await endpoint(successor, remaining);
            need(JSON.stringify([report.config_path, report.hub_state_root]) === selection && report.state === 'running', 'selected successor is not ready');
            need(successor.pid > 0 && (successor.pid !== oldPID || nextBirth !== birth), 'predecessor is still running');
            need(!predecessor.running || report.running.boot_id !== predecessor.running.boot_id, 'old boot is still reported');
            need(report.running.version === expected && report.running.backend_git_sha === sha, 'successor build/version mismatch');
            const after = await service(remaining);
            need(intent(after) === launch && after.pid === successor.pid && await processIdentity(after, remaining, async () => report) === nextBirth, 'successor changed during verification');
            need(Date.now() <= deadline, 'successor verification deadline exceeded');
            console.log('deploy-hub: OK, '+target+', PID '+oldPID+' -> '+successor.pid+', boot '+report.running.boot_id+', build '+expected+', selected root verified');
            return;
        } catch (e) { lastFailure = e.message; }
        if (Date.now() < deadline) await new Promise(resolve => setTimeout(resolve, Math.min(100, deadline-Date.now())));
    }
    throw new Error('successor not verified before timeout, '+lastFailure);
})().catch(e => {
    console.error('deploy-hub: '+stage+' failed, '+e.message);
    console.error('deploy-hub: predecessor PID '+oldPID+', last observed selected PID '+lastPID+
        ', build completed '+built+', restart attempted '+attempted+'. No rollback or evidence deletion performed.');
    process.exitCode = 1;
});
JS
