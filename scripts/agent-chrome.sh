#!/usr/bin/env bash
# agent-chrome.sh — launch a Chrome that belongs to YOU and nobody else.
#
# Why this exists (kata 8ecz): the superpowers-chrome MCP server drives ONE
# shared Chrome, and `set_profile` is a single sticky value on that server
# process rather than a per-agent setting. The first agent to call it silently
# redirects every other agent — and the controller — into its profile for the
# rest of the session. Measured: 38 tabs piled into one agent's "isolated"
# profile while the real default held 2, and a controller that had never called
# set_profile read back another agent's worktree name.
#
# That is worse than no isolation, because it looks like isolation. Agents
# believed they were alone in a private browser while a dozen others were in it.
# The damage is not theoretical: one usability participant made eight attempts
# at a single task and never got an uninterrupted run, its tab hijacked twice
# inside one second. Another abandoned its screenshots entirely.
#
# So: DO NOT call set_profile. Launch your own browser with this instead, and
# talk to it over CDP on the port it prints.
#
# Usage:
#   eval "$(scripts/agent-chrome.sh <name>)"   # exports AGENT_CHROME_PORT
#   scripts/agent-chrome.sh <name> --port-only # just print the port
#   scripts/agent-chrome.sh --kill <name>      # shut yours down
#
# <name> should be your worktree or branch name. git guarantees it is unique,
# which is the whole point — a name derived from a shared wave prefix is how
# two agents ended up overwriting each other's scratch files (kata k2rx).
set -uo pipefail

CHROME_CANDIDATES=(
	"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome"
	"/Applications/Chromium.app/Contents/MacOS/Chromium"
	"/usr/bin/google-chrome"
	"/usr/bin/chromium"
	"/usr/bin/chromium-browser"
)

find_chrome() {
	local c
	for c in "${CHROME_CANDIDATES[@]}"; do
		[ -x "$c" ] && { printf '%s' "$c"; return 0; }
	done
	echo "agent-chrome: no Chrome/Chromium found (looked at: ${CHROME_CANDIDATES[*]})" >&2
	return 1
}

name=""
port_only=0
kill_mode=0
while [ $# -gt 0 ]; do
	case "$1" in
	--port-only) port_only=1 ;;
	--kill) kill_mode=1 ;;
	-h | --help)
		sed -n '2,30p' "$0" | sed 's/^# \{0,1\}//'
		exit 0
		;;
	*) name="$1" ;;
	esac
	shift
done

[ -n "$name" ] || {
	echo "agent-chrome: need a name (use your worktree/branch name)" >&2
	exit 2
}

# Derived from the name, so it cannot collide with another agent's by accident
# and is the same every time YOU ask for it.
root="${TMPDIR:-/tmp}/agent-chrome-$name"
profile="$root/profile"
portfile="$root/port"

if [ "$kill_mode" = 1 ]; then
	if [ -f "$root/pid" ] && kill -0 "$(cat "$root/pid")" 2>/dev/null; then
		kill "$(cat "$root/pid")" && echo "agent-chrome: stopped $name" >&2
	else
		echo "agent-chrome: nothing running for $name" >&2
	fi
	exit 0
fi

# Already up and healthy? Reuse it rather than starting a second one.
if [ -f "$portfile" ] && [ -f "$root/pid" ] && kill -0 "$(cat "$root/pid")" 2>/dev/null; then
	port=$(cat "$portfile")
	if curl -s --max-time 2 "http://127.0.0.1:$port/json/version" >/dev/null 2>&1; then
		[ "$port_only" = 1 ] && { printf '%s\n' "$port"; exit 0; }
		printf 'export AGENT_CHROME_PORT=%s\n' "$port"
		exit 0
	fi
fi

chrome=$(find_chrome) || exit 1
mkdir -p "$profile"

# Port 0 lets the kernel choose, so two agents starting at the same instant
# cannot pick the same number - the mistake that put two hubs on 8959.
"$chrome" \
	--remote-debugging-port=0 \
	--user-data-dir="$profile" \
	--no-first-run \
	--no-default-browser-check \
	--disable-search-engine-choice-screen \
	--headless=new \
	about:blank >"$root/chrome.log" 2>&1 &
echo $! >"$root/pid"

# Chrome writes the port it actually got into the profile. Poll for it rather
# than sleeping a guessed interval.
port=""
for _ in $(seq 1 60); do
	if [ -s "$profile/DevToolsActivePort" ]; then
		port=$(head -1 "$profile/DevToolsActivePort")
		[ -n "$port" ] && break
	fi
	sleep 0.25
done

[ -n "$port" ] || {
	echo "agent-chrome: chrome never reported a debugging port; see $root/chrome.log" >&2
	exit 1
}
printf '%s' "$port" >"$portfile"

[ "$port_only" = 1 ] && { printf '%s\n' "$port"; exit 0; }
printf 'export AGENT_CHROME_PORT=%s\n' "$port"
