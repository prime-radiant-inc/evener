# SDK queue and close producer evidence

The retained fixture `/tmp/evener-sdk-producers-retry2-5qFnjm` provides scoped
real AppWire evidence for `thread/queueChanged`. An observer subscribed to the
target ref before an active scripted-provider turn was held. A real
`turn/queue` mutation returned an acknowledgment, canonical `thread/read`
showed the queued entry, and the raw observer stream contained
`thread/queueChanged`. The provider was released and the active turn completed.

The evidence is recorded in
[`sdk-queue-close-receipt.json`](assets/sdk-queue-close-receipt.json), with
hashes for the raw event stream, queue acknowledgment/readback, final read,
and provider evidence. The packaged SDK comparison covered all 139 tarball
regular files byte-for-byte against the installed consumer.

The separate fresh fixture `/tmp/evener-sdk-producers-final-SqGtdW` tested
`thread/closed` with the observer subscribed through a bounded 30-second wait
after an empty `thread/shutdown` acknowledgment. No matching event arrived,
so that producer remains unqualified. Its retained pre-shutdown records are
listed in the receipt. This is an evidence gap; it does not establish that the
producer is unsupported or identify a backend defect. No `thread/closed` count
has been added.

The backend fix at `4f3e824ac` was rebuilt and exercised in a second fresh
real-process fixture, `/tmp/evener-sdk-close-run-YFL38i`. The same actor and
observer remained subscribed while an active scripted-provider turn was held;
`thread/queueChanged`, the queued readback, release, and both turn completions
were observed. After `thread/shutdown` returned its empty acknowledgment, the
observer waited 30 seconds for the exact target ref and thread ID. No matching
`thread/closed` arrived before the daemon exited. Every notification was
written immediately to mode-0600 JSONL and retained in the final event file,
so this remains a concrete failed qualification rather than a teardown-only
observation. The new fixture and process cleanup hashes are recorded in the
receipt; `thread/closed` remains unqualified.
