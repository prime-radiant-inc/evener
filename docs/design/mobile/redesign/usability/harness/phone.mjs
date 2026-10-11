#!/usr/bin/env node
// phone.mjs — the CLI a usability-test "participant" uses to drive the
// prototype the way a person would: by looking at screenshots and tapping,
// swiping, long-pressing, scrolling and typing. Every invocation is a single
// short-lived process that POSTs one JSON command to a running driver.mjs
// and prints a short plain-text result. All the actual browser automation
// lives in driver.mjs; this file only parses argv and relays.
//
// Usage: node phone.mjs --port <port> <command> [args...]
// Run `node phone.mjs --help` for the full command list.

const HELP = `phone.mjs — drive the prototype like a phone user

Usage: node phone.mjs --port <port> <command> [args...]

Commands:
  shot [label]                          take a screenshot now
  tap "<text>" [--nth N]                tap the on-screen element labeled <text>
  tapxy X Y                             touch-tap at CSS coordinates
  longpress "<text>" | --xy X Y         touch down, hold 650ms, release
  swipe X1 Y1 X2 Y2 [--ms 300]          touch-drag from one point to another
  swiperow "<text>" left|right          swipe a row left or right
  scroll down|up [pixels] [--at X Y]    scroll like a finger
  type "<text>"                         type into the focused field
  key <Name>                            press a key (Enter, Backspace, ...)
  back                                  iOS edge-swipe back gesture
  see                                   list what's on screen
  task <k>                              start task k (1-based index or id)
  done "<what you did / concluded>"     end the current task with a note
  finish                                end the session, shut the driver down
  health                                check the driver is alive

Every action command (tap, tapxy, longpress, swipe, swiperow, scroll, type,
key, back) waits ~450ms for animations, saves a screenshot automatically,
and prints its path after the result — look at it to see what happened.
`;

function fail(message) {
  console.error(message);
  process.exit(1);
}

function parseTopLevel(argv) {
  const a = [...argv];
  let port = null;
  const portIdx = a.indexOf('--port');
  if (portIdx !== -1) {
    port = Number(a[portIdx + 1]);
    a.splice(portIdx, 2);
  }
  if (a.length === 0 || a[0] === '--help' || a[0] === '-h') {
    process.stdout.write(HELP);
    process.exit(0);
  }
  return { port, command: a[0], rest: a.slice(1) };
}

// Pulls a flag with a fixed number of numeric values out of an args array,
// e.g. parseFlagValues(['--at','1','2'], 'at', 2) -> [1, 2]. Returns null if
// the flag isn't present at all.
function parseFlagValues(list, flagName, count) {
  const idx = list.indexOf(`--${flagName}`);
  if (idx === -1) return null;
  const values = list.slice(idx + 1, idx + 1 + count).map(Number);
  if (values.length < count || values.some(Number.isNaN)) {
    fail(`--${flagName} needs ${count} numeric value(s)`);
  }
  return values;
}

function withoutFlag(list, flagName, count) {
  const idx = list.indexOf(`--${flagName}`);
  if (idx === -1) return list;
  return [...list.slice(0, idx), ...list.slice(idx + 1 + count)];
}

function buildArgs(command, rest) {
  switch (command) {
    case 'shot':
      return { label: rest[0] };

    case 'tap': {
      const nthVals = parseFlagValues(rest, 'nth', 1);
      const positional = withoutFlag(rest, 'nth', 1);
      if (positional.length < 1) fail('usage: tap "<text>" [--nth N]');
      return { text: positional[0], nth: nthVals ? nthVals[0] : null };
    }

    case 'tapxy': {
      if (rest.length < 2) fail('usage: tapxy X Y');
      const x = Number(rest[0]);
      const y = Number(rest[1]);
      if (Number.isNaN(x) || Number.isNaN(y)) fail('tapxy needs two numbers: X Y');
      return { x, y };
    }

    case 'longpress': {
      const xy = parseFlagValues(rest, 'xy', 2);
      if (xy) return { x: xy[0], y: xy[1] };
      if (rest.length >= 1) return { text: rest[0] };
      fail('usage: longpress "<text>"  OR  longpress --xy X Y');
      return undefined;
    }

    case 'swipe': {
      const msVals = parseFlagValues(rest, 'ms', 1);
      const positional = withoutFlag(rest, 'ms', 1);
      if (positional.length < 4) fail('usage: swipe X1 Y1 X2 Y2 [--ms 300]');
      const nums = positional.slice(0, 4).map(Number);
      if (nums.some(Number.isNaN)) fail('swipe needs 4 numbers: X1 Y1 X2 Y2');
      return { x1: nums[0], y1: nums[1], x2: nums[2], y2: nums[3], ms: msVals ? msVals[0] : undefined };
    }

    case 'swiperow': {
      if (rest.length < 2) fail('usage: swiperow "<text>" left|right');
      const direction = rest[rest.length - 1];
      const text = rest.slice(0, -1).join(' ');
      if (!['left', 'right'].includes(direction)) fail('swiperow direction must be left or right');
      return { text, direction };
    }

    case 'scroll': {
      const atVals = parseFlagValues(rest, 'at', 2);
      const positional = withoutFlag(rest, 'at', 2);
      if (positional.length < 1) fail('usage: scroll down|up [pixels] [--at X Y]');
      const direction = positional[0];
      if (!['up', 'down'].includes(direction)) fail('scroll direction must be up or down');
      const pixels = positional[1] != null ? Number(positional[1]) : undefined;
      if (pixels != null && Number.isNaN(pixels)) fail('scroll pixels must be a number');
      return { direction, pixels, atX: atVals ? atVals[0] : undefined, atY: atVals ? atVals[1] : undefined };
    }

    case 'type': {
      if (rest.length < 1) fail('usage: type "<text>"');
      return { text: rest.join(' ') };
    }

    case 'key': {
      if (rest.length < 1) fail('usage: key <Name>');
      return { name: rest[0] };
    }

    case 'back':
    case 'see':
    case 'finish':
    case 'health':
      return {};

    case 'task': {
      if (rest.length < 1) fail('usage: task <k>');
      return { k: rest[0] };
    }

    case 'done': {
      if (rest.length < 1) fail('usage: done "<what you did / concluded>"');
      return { note: rest.join(' ') };
    }

    default:
      fail(`unknown command '${command}'\n\n${HELP}`);
      return undefined;
  }
}

async function sendCommand(port, command, cmdArgs) {
  const url = `http://127.0.0.1:${port}/`;
  const controller = new AbortController();
  const timeout = setTimeout(() => controller.abort(), 20000);
  let resp;
  try {
    resp = await fetch(url, {
      method: 'POST',
      headers: { 'content-type': 'application/json' },
      body: JSON.stringify({ command, args: cmdArgs }),
      signal: controller.signal,
    });
  } catch (err) {
    const reason = err.name === 'AbortError' ? 'timed out after 20s' : err.message;
    fail(`driver not reachable at 127.0.0.1:${port} (${reason}). Is driver.mjs running on that port?`);
    return undefined;
  } finally {
    clearTimeout(timeout);
  }
  try {
    return await resp.json();
  } catch (err) {
    fail(`driver returned a non-JSON response: ${err.message}`);
    return undefined;
  }
}

const { port, command, rest } = parseTopLevel(process.argv.slice(2));
if (!port || Number.isNaN(port)) fail('--port <n> is required, e.g. node phone.mjs --port 8761 health');

const cmdArgs = buildArgs(command, rest);
const data = await sendCommand(port, command, cmdArgs);
console.log(data.text);
process.exit(data.ok ? 0 : 1);
