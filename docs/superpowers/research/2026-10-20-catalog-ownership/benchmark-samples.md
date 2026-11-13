# Catalog benchmark samples

Raw implementation-lane measurements, preserved from the reviewed task report. These precede test-helper simplification; the production implementation is unchanged by that cleanup.


Exact subbenchmark names for the coordinator's CPU driver:

- `BenchmarkCatalogLookupAndList/resolve`
- `BenchmarkCatalogLookupAndList/model-ids`
- `BenchmarkLoadEmbeddedWarm` (no subbenchmark)

Command for both sides:

```
GOMAXPROCS=2 go test -p=1 ./llm/registry -run '^$' -bench '^(BenchmarkLoadEmbeddedWarm|BenchmarkCatalogLookupAndList)$' -benchmem -benchtime=100x -count=5
```

Before source identity: base commit `f18fe2290a5c1037aab21bd3201ce20bf09c9862`, `load.go` blob `991e4b6c15e54472657208474ec03949150424db`, test blob `bb97c7ebea619528c512c959ef3ca7fee6f8cd04`.

Raw before:

```
BenchmarkLoadEmbeddedWarm-2       	     100	  10650987 ns/op	11577173 B/op	   73019 allocs/op
BenchmarkLoadEmbeddedWarm-2       	     100	  10996156 ns/op	11574920 B/op	   73019 allocs/op
BenchmarkLoadEmbeddedWarm-2       	     100	  10917672 ns/op	11578687 B/op	   73019 allocs/op
BenchmarkLoadEmbeddedWarm-2       	     100	  10717136 ns/op	11578304 B/op	   73020 allocs/op
BenchmarkLoadEmbeddedWarm-2       	     100	  11705176 ns/op	11577892 B/op	   73019 allocs/op
BenchmarkCatalogLookupAndList/resolve-2         	     100	     28718 ns/op	   19670 B/op	     231 allocs/op
BenchmarkCatalogLookupAndList/resolve-2         	     100	     23832 ns/op	   19670 B/op	     231 allocs/op
BenchmarkCatalogLookupAndList/resolve-2         	     100	     24688 ns/op	   19298 B/op	     231 allocs/op
BenchmarkCatalogLookupAndList/resolve-2         	     100	     26035 ns/op	   19670 B/op	     231 allocs/op
BenchmarkCatalogLookupAndList/resolve-2         	     100	     24644 ns/op	   19298 B/op	     231 allocs/op
BenchmarkCatalogLookupAndList/model-ids-2       	     100	      9923 ns/op	    6136 B/op	      15 allocs/op
BenchmarkCatalogLookupAndList/model-ids-2       	     100	     10730 ns/op	    6136 B/op	      15 allocs/op
BenchmarkCatalogLookupAndList/model-ids-2       	     100	      6872 ns/op	    6136 B/op	      15 allocs/op
BenchmarkCatalogLookupAndList/model-ids-2       	     100	      6893 ns/op	    6136 B/op	      15 allocs/op
BenchmarkCatalogLookupAndList/model-ids-2       	     100	      7233 ns/op	    6136 B/op	      15 allocs/op
PASS
ok  	primeradiant.com/evener/llm/registry	5.625s
```

After source identity: commit `1f1b3dccdac5f4311b0f877902e4834f28d56208` (worktree at measurement time), `load.go` blob `f08dd7813bf89cfc27fa2a1855219cfed37edb3d`, test blob `bb97c7ebea619528c512c959ef3ca7fee6f8cd04`.

Raw after:

```
BenchmarkLoadEmbeddedWarm-2       	     100	   8521671 ns/op	 7175321 B/op	   64389 allocs/op
BenchmarkLoadEmbeddedWarm-2       	     100	   8320215 ns/op	 7172733 B/op	   64389 allocs/op
BenchmarkLoadEmbeddedWarm-2       	     100	   9307707 ns/op	 7173781 B/op	   64389 allocs/op
BenchmarkLoadEmbeddedWarm-2       	     100	   8397920 ns/op	 7175295 B/op	   64389 allocs/op
BenchmarkLoadEmbeddedWarm-2       	     100	  10205738 ns/op	 7174635 B/op	   64389 allocs/op
BenchmarkCatalogLookupAndList/resolve-2         	     100	     24389 ns/op	   19298 B/op	     231 allocs/op
BenchmarkCatalogLookupAndList/resolve-2         	     100	     29261 ns/op	   19670 B/op	     231 allocs/op
BenchmarkCatalogLookupAndList/resolve-2         	     100	     23947 ns/op	   19670 B/op	     231 allocs/op
BenchmarkCatalogLookupAndList/resolve-2         	     100	     23429 ns/op	   19670 B/op	     231 allocs/op
BenchmarkCatalogLookupAndList/resolve-2         	     100	     22919 ns/op	   19298 B/op	     231 allocs/op
BenchmarkCatalogLookupAndList/model-ids-2       	     100	      6756 ns/op	    6136 B/op	      15 allocs/op
BenchmarkCatalogLookupAndList/model-ids-2       	     100	      6547 ns/op	    6136 B/op	      15 allocs/op
BenchmarkCatalogLookupAndList/model-ids-2       	     100	      9695 ns/op	    6136 B/op	      15 allocs/op
BenchmarkCatalogLookupAndList/model-ids-2       	     100	      6722 ns/op	    6136 B/op	      15 allocs/op
BenchmarkCatalogLookupAndList/model-ids-2       	     100	      6669 ns/op	    6136 B/op	      15 allocs/op
PASS
ok  	primeradiant.com/evener/llm/registry	4.585s
```

Summary (means of the five raw samples):

- `BenchmarkLoadEmbeddedWarm` B/op: 11,577,395.2 → 7,174,353.0, −4,403,042.2 B/op (−38.03%).
- `BenchmarkLoadEmbeddedWarm` allocs/op: 73,019.2 → 64,389.0, −8,630.2 (−11.82%).
- `BenchmarkCatalogLookupAndList/resolve`: 231 allocs/op and same B/op on both sides; wall ns/op 23,832–28,718 before, 22,919–29,261 after (noise; no repeatable regression).
- `BenchmarkCatalogLookupAndList/model-ids`: 15 allocs/op and 6,136 B/op on both sides; wall ns/op 6,872–10,730 before, 6,547–9,695 after.

These are wall-time `ns/op` values, not CPU measurements. Coordinator owns paired subprocess CPU and startup-peak measurement.
