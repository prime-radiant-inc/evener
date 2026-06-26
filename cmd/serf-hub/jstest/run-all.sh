#!/bin/sh
set -u

cd "$(dirname "$0")"

# Per-test wall-clock limit (seconds). A hung test must not wedge the whole
# suite, so each test runs under `timeout` and a TIMEOUT is reported as a
# failure while the loop continues. Override with JSTEST_TIMEOUT.
TIMEOUT="${JSTEST_TIMEOUT:-90}"

if [ -z "${NODE_PATH:-}" ]; then
  if [ -d "./node_modules" ]; then
    NODE_PATH="$PWD/node_modules"
  elif [ -d "/tmp/serf-jstest-jsdom/node_modules" ]; then
    NODE_PATH="/tmp/serf-jstest-jsdom/node_modules"
  fi
  export NODE_PATH
fi

fail=0
for test in test-*.js; do
  out=$(timeout "$TIMEOUT" node "$test" 2>&1)
  rc=$?
  if [ "$rc" -eq 0 ]; then
    echo "OK      $test"
  elif [ "$rc" -eq 124 ]; then
    echo "TIMEOUT $test (exceeded ${TIMEOUT}s)"
    fail=1
  else
    echo "FAIL($rc) $test"
    echo "$out" | tail -20
    fail=1
  fi
done

if [ "$fail" -ne 0 ]; then
  echo "jstest: one or more tests failed"
  exit 1
fi
echo "jstest: all tests passed"
