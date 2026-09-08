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

Before the current-source rerun, the backend fix at `4f3e824ac` was rebuilt and exercised in a second fresh
real-process fixture, `/tmp/evener-sdk-close-run-YFL38i`. The same actor and
observer remained subscribed while an active scripted-provider turn was held;
`thread/queueChanged`, the queued readback, release, and both turn completions
were observed. After `thread/shutdown` returned its empty acknowledgment, the
observer waited 30 seconds for the exact target ref and thread ID. No matching
`thread/closed` arrived before the daemon exited. Every notification was
written immediately to mode-0600 JSONL and retained in the final event file,
so that pre-fix-binary run remains a concrete failed qualification rather than
a teardown-only observation. The fixture and process cleanup hashes are
recorded in the receipt.

The earlier close failure was traced to binary provenance rather than an SDK
lifecycle defect. Its `go version -m` metadata identified source revision
`d2d5eedf9`, before the shutdown change. A fresh run using current source
`3284d6ac5` rebuilt `evener` (`81603ee6330ba64b4646e3d24d06b1a705688266c6c003d8babb377dd763fa46`)
and `evener-dev`
(`28267467f9758126d49e737795d685f7f2e15dc2e86d861cd3aa26c2ad57ad50`).
The run first established a completed setup turn and `awaiting` thread, then
used an actor `subscribe:false` read, subscribed the observer, completed both
the active and queued turns, and confirmed canonical `awaiting` state before
shutdown. The exact target `thread/closed` arrived once, among 28 raw events.
Raw JSONL was written immediately on receipt and the final event list was
retained. The run is scoped qualification for `thread/closed`; it adds one
new qualified notification name. The 139-file packaged SDK tarball comparison
remains byte-equal, with tarball SHA-256
`2a22e65e84da4fa2466a5406800f7b141ee710d4f4e4f73588fbf4873e73e731`.
