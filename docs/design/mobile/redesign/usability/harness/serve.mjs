#!/usr/bin/env node
// serve.mjs — static file server for a phone-app prototype fragment.
//
// Why this exists: the prototype under test is authored as an HTML page
// FRAGMENT (no <!doctype>/<html>/<head>/<body>) because it will later be
// published into a host page that supplies those. To view or drive the
// fragment as a real page during usability testing, something has to wrap
// it the same way the eventual host will. This server does that wrapping
// for '/' and '/index.html' and serves everything else in --dir as-is.
//
// Usage:
//   node serve.mjs --dir <prototypeDir> --port <port> [--verbose]

import http from 'node:http';
import fs from 'node:fs';
import path from 'node:path';

const HELP = `serve.mjs — static file server for a phone-app prototype fragment

Usage:
  node serve.mjs --dir <prototypeDir> --port <port> [--verbose]

Options:
  --dir <path>   Directory to serve (required). Its index.html is treated as
                 a body FRAGMENT and wrapped in a minimal HTML document when
                 requested as '/' or '/index.html'. Every other file is
                 served as-is with a content type based on its extension.
  --port <n>     Port to listen on, bound to 127.0.0.1 only (required).
  --verbose      Log one line per request to stderr.
  --help         Show this help.
`;

function parseArgs(argv) {
  const args = { dir: null, port: null, verbose: false, help: false };
  for (let i = 0; i < argv.length; i++) {
    const a = argv[i];
    if (a === '--dir') args.dir = argv[++i];
    else if (a === '--port') args.port = Number(argv[++i]);
    else if (a === '--verbose') args.verbose = true;
    else if (a === '--help' || a === '-h') args.help = true;
  }
  return args;
}

const args = parseArgs(process.argv.slice(2));
if (args.help || !args.dir || !args.port || Number.isNaN(args.port)) {
  process.stdout.write(HELP);
  process.exit(args.help ? 0 : 1);
}

const rootDir = path.resolve(args.dir);
if (!fs.existsSync(rootDir) || !fs.statSync(rootDir).isDirectory()) {
  console.error(`serve.mjs: --dir '${args.dir}' is not a directory`);
  process.exit(1);
}

// The prototype's index.html has no <head>/<body> of its own, so this reset
// keeps it from looking broken when loaded directly: safe-area padding for
// notch/home-indicator devices, a sane default font/background, and a couple
// of conventions ([hidden], responsive images) the fragment is likely to
// assume a host page provides.
const RESET_CSS =
  ':root{color-scheme:light;padding-top:env(safe-area-inset-top,0px);padding-bottom:env(safe-area-inset-bottom,0px)} body{margin:0;font:14px system-ui,-apple-system,sans-serif;background:#fafafa} img{max-width:100%} [hidden]{display:none!important}';

function wrapFragment(fragment) {
  return (
    '<!doctype html><html lang="en"><head><meta charset="utf-8">' +
    '<meta name="viewport" content="width=device-width, initial-scale=1, viewport-fit=cover">' +
    `<style>${RESET_CSS}</style></head><body>${fragment}</body></html>`
  );
}

const CONTENT_TYPES = {
  '.html': 'text/html; charset=utf-8',
  '.css': 'text/css; charset=utf-8',
  '.js': 'text/javascript; charset=utf-8',
  '.mjs': 'text/javascript; charset=utf-8',
  '.json': 'application/json; charset=utf-8',
  '.svg': 'image/svg+xml',
  '.png': 'image/png',
  '.woff2': 'font/woff2',
};

// Resolve a request path against rootDir without ever escaping it, even via
// '..' segments or URL-encoded tricks.
function resolveSafely(root, urlPath) {
  const decoded = decodeURIComponent(urlPath.split('?')[0].split('#')[0]);
  const resolved = path.resolve(root, `.${decoded}`);
  const resolvedRoot = path.resolve(root);
  if (resolved !== resolvedRoot && !resolved.startsWith(resolvedRoot + path.sep)) {
    return null;
  }
  return resolved;
}

function sendText(res, status, text) {
  res.writeHead(status, { 'content-type': 'text/plain; charset=utf-8' });
  res.end(text);
}

function handleRequest(req, res) {
  const urlPath = req.url === '/' ? '/index.html' : req.url;
  const filePath = resolveSafely(rootDir, urlPath);

  if (!filePath) {
    sendText(res, 403, 'forbidden: path escapes the served directory');
    return;
  }

  if (urlPath === '/index.html') {
    if (!fs.existsSync(filePath) || !fs.statSync(filePath).isFile()) {
      sendText(res, 404, "not found: index.html (the prototype hasn't been written yet?)");
      return;
    }
    const fragment = fs.readFileSync(filePath, 'utf8');
    res.writeHead(200, { 'content-type': 'text/html; charset=utf-8' });
    res.end(wrapFragment(fragment));
    return;
  }

  if (!fs.existsSync(filePath) || !fs.statSync(filePath).isFile()) {
    sendText(res, 404, `not found: ${urlPath}`);
    return;
  }

  const ext = path.extname(filePath).toLowerCase();
  const contentType = CONTENT_TYPES[ext] || 'application/octet-stream';
  res.writeHead(200, { 'content-type': contentType });
  fs.createReadStream(filePath).pipe(res);
}

const server = http.createServer((req, res) => {
  if (args.verbose) {
    const start = Date.now();
    res.on('finish', () => {
      console.error(`${req.method} ${req.url} -> ${res.statusCode} (${Date.now() - start}ms)`);
    });
  }
  try {
    handleRequest(req, res);
  } catch (err) {
    sendText(res, 500, `server error: ${err.message}`);
  }
});

server.listen(args.port, '127.0.0.1', () => {
  console.log(`serve.mjs: serving ${rootDir} at http://127.0.0.1:${args.port}/`);
});

server.on('error', (err) => {
  console.error(`serve.mjs: ${err.message}`);
  process.exit(1);
});

function shutdown() {
  server.close(() => process.exit(0));
  setTimeout(() => process.exit(0), 2000).unref();
}
process.on('SIGTERM', shutdown);
process.on('SIGINT', shutdown);
