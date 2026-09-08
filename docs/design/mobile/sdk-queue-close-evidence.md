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

The current-source run `/tmp/evener-sdk-idle-close-current-OwRkNp` is the
first verified `thread/closed` qualification. It used a completed setup turn
and canonical `awaiting` state before the observer subscribed; the actor then
used `subscribe:false` reads while the observer received notifications. Both
active and queued turns completed before shutdown, and the exact target
`thread/closed` arrived once among 28 raw events.

Earlier retained attempts remain historical limitations. The fixture
`/tmp/evener-sdk-producers-final-SqGtdW` did not observe a matching event. The
later `/tmp/evener-sdk-close-run-YFL38i` also failed, but its `go version -m`
metadata identifies pre-fix source `d2d5eedf9`, despite its intended use for the
`4f3e824ac` fix. Those records are preserved in the receipt and do not support
an SDK or backend defect claim.

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
