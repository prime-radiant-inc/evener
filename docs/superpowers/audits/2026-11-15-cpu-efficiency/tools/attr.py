#!/usr/bin/env python3
"""Attribute CPU to the nearest non-runtime caller frame, for a subtree filter.
Answers "how much CPU does syscall.Fsync cost, and which evener function owns it".

Usage: attr.py <profile.pprof> <substring in stack> [depth_from_leaf]
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

filt = sys.argv[2]
agg, total, grand = Counter(), 0, 0
for ids, vals in samples:
    if not ids or not vals: continue
    grand += vals[-1]
    names = [name(l) for l in ids]
    if not any(filt in n for n in names): continue
    total += vals[-1]
    owner = next((n for n in names if n.startswith('primeradiant.com/evener/')), names[-1])
    agg[owner] += vals[-1]

print(f"total profile {grand/1e6:.0f}ms · matching '{filt}' {total/1e6:.0f}ms ({100*total/grand:.1f}%)")
for n, v in agg.most_common(14):
    print(f"{v/1e6:8.1f}ms  {n}")
