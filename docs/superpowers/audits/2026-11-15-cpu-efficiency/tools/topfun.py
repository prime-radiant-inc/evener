#!/usr/bin/env python3
"""Print flat/cumulative CPU by function from a Go pprof protobuf without
`go tool pprof`. Use when go tool pprof cannot finish (cold tool build under
heavy host load) or when the binary that wrote the profile is unavailable.

Usage: topfun.py <profile.pprof> [flat_limit]
"""
import gzip, sys
from collections import defaultdict

def varint(b, i):
    v = s = 0
    while True:
        c = b[i]; i += 1
        v |= (c & 0x7f) << s
        if c < 0x80:
            return v, i
        s += 7

def fields(b):
    i = 0
    while i < len(b):
        key, i = varint(b, i)
        f, wt = key >> 3, key & 7
        if wt == 0:
            v, i = varint(b, i); yield f, 'v', v
        elif wt == 2:
            n, i = varint(b, i); yield f, 'b', b[i:i+n]; i += n
        elif wt == 1:
            i += 8; yield f, 'v', 0
        elif wt == 5:
            i += 4; yield f, 'v', 0
        else:
            return

def packed(b):
    out, i = [], 0
    while i < len(b):
        v, i = varint(b, i); out.append(v)
    return out

def read(path):
    data = open(path, 'rb').read()
    try:
        return gzip.decompress(data)
    except OSError:
        return data

raw = read(sys.argv[1])
strtab, funcs, locs_by_id, samples = [], {}, {}, []
for f, kind, v in fields(raw):
    if f == 6:                       # string_table entry (index = position)
        strtab.append(v.decode('utf-8', 'replace'))
    elif f == 5 and kind == 'b':     # Function
        fid, name_idx = 0, 0
        for g, gk, gv in fields(v):
            if g == 1 and gk == 'v': fid = gv
            elif g == 2 and gk == 'v': name_idx = gv
        funcs[fid] = name_idx
    elif f == 4 and kind == 'b':     # Location
        lid, fn = 0, 0
        for g, gk, gv in fields(v):
            if g == 1 and gk == 'v': lid = gv
            elif g == 4 and gk == 'b':   # Line
                for h, hk, hv in fields(gv):
                    if h == 1 and hk == 'v': fn = hv
        locs_by_id[lid] = fn
    elif f == 2 and kind == 'b':     # Sample
        loc_ids, vals = [], []
        for g, gk, gv in fields(v):
            if g == 1:
                loc_ids.extend(packed(gv) if gk == 'b' else [gv])
            elif g == 2:
                # Sample.value is repeated: [samples/count, cpu/nanoseconds].
                # The last entry is the CPU time, which is what we report.
                vals.extend(packed(gv) if gk == 'b' else [gv])
        samples.append((loc_ids, vals))

def name_of(loc_id):
    fid = locs_by_id.get(loc_id, 0)
    return strtab[funcs[fid]] if funcs.get(fid, 0) < len(strtab) else '?'

flat, cum, total = defaultdict(int), defaultdict(int), 0
for loc_ids, vals in samples:
    if not loc_ids or not vals: continue
    v = vals[-1]; total += v
    flat[name_of(loc_ids[0])] += v
    seen = set()
    for l in loc_ids:
        n = name_of(l)
        if n in seen: continue
        seen.add(n); cum[n] += v

limit = int(sys.argv[2]) if len(sys.argv) > 2 else 30
print(f"samples={len(samples)} total={total/1e6:.0f}ms")
print("\n--- by flat ---")
for n, v in sorted(flat.items(), key=lambda kv: -kv[1])[:limit]:
    print(f"{v/1e6:8.1f}ms {cum[n]/1e6:8.1f}ms  {n}")
print("\n--- by cum ---")
for n, v in sorted(cum.items(), key=lambda kv: -kv[1])[:25]:
    print(f"{flat[n]/1e6:8.1f}ms {v/1e6:8.1f}ms  {n}")

# --- callers of a named function, and its callees, when a third arg is given ---
if len(sys.argv) > 3:
    target = sys.argv[3]
    from collections import Counter
    callers, callees = Counter(), Counter()
    for l, (loc_ids, vals) in enumerate([]):
        pass
    for loc_ids, vals in samples:
        if not loc_ids or not vals: continue
        v = vals[-1]
        names = [name_of(l) for l in loc_ids]
        if target in names:
            i = names.index(target)
            # pprof stacks are leaf-first: the next entry is the caller,
            # the previous one is a callee.
            if i + 1 < len(names): callers[names[i+1]] += v
            if i > 0: callees[names[i-1]] += v
    print(f"\n--- callers of {target} ---")
    for n, v in callers.most_common(15): print(f"{v/1e6:8.1f}ms  {n}")
    print(f"\n--- callees of {target} ---")
    for n, v in callees.most_common(15): print(f"{v/1e6:8.1f}ms  {n}")
