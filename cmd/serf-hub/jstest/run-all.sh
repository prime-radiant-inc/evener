#!/bin/sh
set -eu

if [ -z "${NODE_PATH:-}" ]; then
  if [ -d "./node_modules" ]; then
    NODE_PATH="$PWD/node_modules"
  elif [ -d "/tmp/serf-jstest-jsdom/node_modules" ]; then
    NODE_PATH="/tmp/serf-jstest-jsdom/node_modules"
  fi
  export NODE_PATH
fi

for test in test-*.js; do
  node "$test"
done
