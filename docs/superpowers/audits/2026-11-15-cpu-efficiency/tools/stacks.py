#!/usr/bin/env python3
"""Collapse a Go pprof protobuf into weighted stacks (leaf first) so you can see
which call site owns a cost when `go tool pprof` is unavailable.

Usage: stacks.py <profile.pprof> [substring]   (substring filters stacks)
"""
import gzip, sys
from collections import Counter

def varint(b, i):
    v = s = 0
    while True:
        c = b[i]; i += 1; v |= (c & 0x7f) << s
        if c < 0x80: return v, i
        s += 7

def fields(b):
    i = 0
    while i < len(b):
        key, i = varint(b, i); f, wt = key >> 3, key & 7
        if wt == 0:
            v, i = varint(b, i); yield f, 'v', v
        elif wt == 2:
            n, i = varint(b, i); yield f, 'b', b[i:i+n]; i += n
        elif wt == 1: i += 8; yield f, 'v', 0
        elif wt == 5: i += 4; yield f, 'v', 0
        else: return

def packed(b):
    out, i = [], 0
    while i < len(b):
        v, i = varint(b, i); out.append(v)
    return out

raw = open(sys.argv[1], 'rb').read()
try: raw = gzip.decompress(raw)
except OSError: pass
strtab, funcs, locs, samples = [], {}, {}, []
for f, k, v in fields(raw):
    if f == 6: strtab.append(v.decode('utf-8', 'replace'))
    elif f == 5 and k == 'b':
        fid = ni = 0
        for g, gk, gv in fields(v):
            if g == 1 and gk == 'v': fid = gv
            elif g == 2 and gk == 'v': ni = gv
        funcs[fid] = ni
    elif f == 4 and k == 'b':
        lid = fn = 0
        for g, gk, gv in fields(v):
            if g == 1 and gk == 'v': lid = gv
            elif g == 4 and gk == 'b':
                for h, hk, hv in fields(gv):
                    if h == 1 and hk == 'v': fn = hv
        locs[lid] = fn
    elif f == 2 and k == 'b':
        ids, vals = [], []
        for g, gk, gv in fields(v):
            if g == 1: ids.extend(packed(gv) if gk == 'b' else [gv])
            elif g == 2: vals.extend(packed(gv) if gk == 'b' else [gv])
        samples.append((ids, vals))

def name(lid):
    fid = locs.get(lid, 0)
    return strtab[funcs[fid]] if funcs.get(fid, 0) < len(strtab) else '?'

filt = sys.argv[2] if len(sys.argv) > 2 else None
agg = Counter()
for ids, vals in samples:
    if not ids or not vals: continue
    names = [name(l) for l in ids]
    if filt and not any(filt in n for n in names): continue
    agg[tuple(names)] += vals[-1]

for stack, v in agg.most_common(12):
    print(f"### {v/1e6:8.1f}ms")
    for n in stack: print("   ", n)
