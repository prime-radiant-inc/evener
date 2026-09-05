# Roster latency investigation

5 September 2026. Read-only production requests and a one-second process sample; no hub restart, production mutation, timeout increase or application code change.

## Reproduction

The shared native connection client initialized against `http://127.0.0.1:9180` in 6 ms. Two sequential `thread/list` requests returned:

| Requested limit | Returned rows | Elapsed |
| --- | --- | --- |
| 5 | 5 | 12,310 ms |
| 51 | 51 | 14,169 ms |

Credentials were loaded from the existing token file and not logged. No transcript or session content was printed. These observations reproduce substantial latency, though less than the earlier 27.6-second observation. They do not establish a stable average or percentile.

## Located mechanism

A macOS `sample` of the hub listener process (PID 38695) during listing captured this stack:

```
hubThreadList
  mergePastMetadataForList
    pastEntryThread
      pastEntryDelegateStatus
        LoadSessionDelegateStatus
          loadHistoricalStableActivityWithAttention
            readExistingDelegateAttentionFold
              readDelegateAttentionFold
                transcript.ReadLine
```

The live roster path is loading historical delegate information while enriching session rows. Limiting returned rows does not eliminate that work because live metadata merging precedes the final limit. A one-second sample establishes that this path executes during the slow request; it does not apportion the entire elapsed time among all possible causes.

The binary at the listener's reported executable path, `/Users/jesse/git/prime-radiant/evener/evener`, reports build revision `f4a3ff2bf889ea4964583b03229cafcde3f7c1c6`. Source at that revision calls `pastEntryThread` from the list path. Build information from a path alone cannot prove the mapped process image is identical, but the sampled function chain independently corroborates that behavior in the running process.

## Existing worktree implementation

The mobile worktree already uses `pastEntryThreadForList`, a metadata-only projection, for both live enrichment and historical roster rows. Its introduction is attributed to `b0e826e625`. It also bounds source fanout. Do not credit this investigation with implementing those changes.

Fresh `go test ./cmd/evener-hub -run 'TestHub(ThreadList|RPCThreadList)' -count=1` passed on source `00efc56e0`. This exercises existing roster behavior and source-bound tests; it is not a production-scale benchmark or a direct test that every historical read is excluded.

The isolated hub on 56491, with three sessions, returned each limit request in less than 0.5 ms (rounded output 0 ms). This verifies the lightweight fixture path, not a valid before/after production comparison: the datasets and binaries differ.

## Remaining work

Keep production latency marked unresolved. Before claiming a fix, verify the metadata-only projection's intended list contract, integrate/deploy through the normal hub workflow, and measure the same production dataset again. The native app should not disguise this delay with a larger timeout. Client-side pagination and search remain separate required work and cannot by themselves remove the observed server-side historical reads.

Local diagnostic artifacts: `/tmp/evener-roster-timing.mts` and `/tmp/evener-hub-roster-sample.txt`. The timing helper only initializes and lists; it does not open conversations or mutate sessions. Neither temporary artifact is a permanent test harness.
