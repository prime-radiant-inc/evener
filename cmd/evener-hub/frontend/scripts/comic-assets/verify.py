#!/usr/bin/env python3
"""Verify only selected Comic Chat art against pinned Git blobs and Win32 raster operations.

No network or font files. Run from any directory; --asset-root/--source-root support
independent corruption controls without touching retained inputs.
"""
from io import BytesIO
from pathlib import Path
import argparse
import hashlib
import json
import struct
import subprocess
from PIL import Image

ROOT = Path(__file__).resolve().parent
DEFAULT_ART = ROOT.parent.parent / 'public/webassets/comic'
REVISION = '48a162249484ab8d116c243e8203b0956d350c09'
# Frozen independently of extract.py and its generated manifest. Original eight
# figures/backgrounds/icons came from the approved preview; four added figures
# came from pinned official AVB records with independent ROP verification.
PNG_SHA256 = {
    'sprites/anna-neutral.png': '8adca28494e27012833e32d591a31875e23b97a8b73b4ee70dc749ccd6c001d6',
    'sprites/anna-happy-wave.png': '4f77bbb5676c4ac1d41bde614cfa2579cc186f244d59bbe4cf1b2173bbab1cc4',
    'sprites/anna-angry.png': '153ec5197c2fc7278062cf43e011c827ae4a6e4f8830cbf6315a48475de7698f',
    'sprites/anna-scared.png': 'd4cccd0aab153a24b89532fe1183cd4f45dcfc5dcc09fd9d98435c30b345f32d',
    'sprites/anna-point.png': '5700d2724ff7192fad6d6d709af8ef2cc6a6c93cfa4067783bacb6d3bcac87c0',
    'sprites/anna-celebrate.png': '353456cdcf1b0e6bd0c22b70309d7843a8c75336ed697a0854dff72f90d58731',
    'sprites/xeno-neutral.png': '6e4ee70923aa1af389c9ea97d1d39480775f22b3b629f2db497bd1616f1a4bd2',
    'sprites/xeno-point.png': 'ec1b0685547f146a61a22c6dd08d3ab52228d4eabc562577d7e75b3a78897ec3',
    'sprites/armando-neutral.png': '3249b8ae2c801a0df0d3cfae3951b26c306a6aab65420e4a9106843c7a7b4ee3',
    'sprites/bolo-neutral.png': '451f1fae459f152dbb4a6b14942adb645e45aec81d6cb5ca9e8ed649f8acc934',
    'sprites/denise-neutral.png': 'b1cf0f6a6c2e820088754a818480255f4a7e3cbbe60cc77a0089d01da6f2a823',
    'sprites/tiki-neutral.png': 'ad78f86420d4fc38ba9d87c478acbacf9c0f16e9c0626b73764dc5e2a7d293b5',
    'backgrounds/field.png': 'ed21ae3dca236a064b79591ca6fd277bf06fb30157b7bdde744c0cf8b43f17d3',
    'backgrounds/room8bs.png': '0aba3cf2ecedb053c1a4fc711564d890122a3bca1cccaadabe6713abcc7f9977',
    'emotions/fc_neu_s.png': '37a190e014603319ffe5840bd3b367c6edadffac6f5fa7b1f38b8371fa23754e',
    'emotions/fc_hap_s.png': '51e034abfe2ae73fe7a8743df3af004ddf00000b4b9799dea60df43c1b29a212',
    'emotions/fc_bor_s.png': '20f93984757d8bd99c4a5da0f36b6bfafb91b62f4432891302c48dfa059a0cb8',
    'emotions/fc_sca_s.png': '72ae61cf6cf23970e8f1e11decb843ea3fb6a08de999bd033020d117dd573d05',
    'emotions/fc_ang_s.png': 'ee4be773fe3dcc5674cf42ca794b7c8b50c789688b3906abae3ee1fccb8a767a',
    'emotions/fc_laf_s.png': '7cb313d11270aa3070c7216b9d82b3e61859695c33bea0513c4b023166f76bb1',
}
EVIDENCE_SHA256 = '8f944d30a5b26b4043272e017a614a68e5ec579fba171fbe29b3f4a3fe8a7b73'
MIT_SHA256 = '722262cd04696639f0f65df38d792fb4e08a28309ff5cee15f10bdd9b66411a3'
CAST_RECORDS = {'armando': (0, 0), 'bolo': (0, 0), 'denise': (3, 4), 'tiki': (2, 0)}


def sha(data):
    return hashlib.sha256(data).hexdigest()


def check(condition, detail):
    if not condition:
        raise ValueError(detail)


def verify(art, original):
    evidence_bytes = (original / 'source-evidence.json').read_bytes()
    check(sha(evidence_bytes) == EVIDENCE_SHA256, 'pinned source-evidence.json changed')
    evidence = json.loads(evidence_bytes)
    check(evidence['revision'] == REVISION and evidence['repository'] == 'https://github.com/microsoft/comic-chat', 'source revision changed')
    check(len(evidence['files']) == 24, 'pinned original source count changed')
    original_paths = set()
    for entry in evidence['files']:
        path = entry['local_path']
        check(path not in original_paths and path == ('source/' + entry['upstream_path'] if path != 'LICENSE' else 'LICENSE'), 'source path mismatch')
        original_paths.add(path)
        check(entry['url'] == f"https://raw.githubusercontent.com/microsoft/comic-chat/{REVISION}/{entry['upstream_path']}", f'upstream URL mismatch: {path}')
        data = (art / 'LICENSE').read_bytes() if path == 'LICENSE' else (original / path).read_bytes()
        check(len(data) == entry['bytes'] and sha(data) == entry['sha256'], f'original SHA-256 mismatch: {path}')
        check(hashlib.sha1(f'blob {len(data)}\0'.encode() + data).hexdigest() == entry['git_blob_sha1'], f'Git blob mismatch: {path}')
    license_bytes = (art / 'LICENSE').read_bytes()
    check(sha(license_bytes) == MIT_SHA256 and b'Permission is hereby granted' in license_bytes and b'THE SOFTWARE IS PROVIDED' in license_bytes, 'missing MIT notice')
    print('Verified 24 pinned Microsoft Git blobs and the unchanged MIT notice.')

    manifest = json.loads((art / 'manifest.json').read_text())
    check(manifest['revision'] == REVISION, 'asset revision changed')
    entries = [item for group in ('sprites', 'backgrounds', 'emotions') for item in manifest[group]]
    check(len(entries) == 20 and {item['path'] for item in entries} == set(PNG_SHA256), 'selected asset set mismatch')
    check({str(path.relative_to(art)) for path in art.rglob('*.png')} == set(PNG_SHA256), 'unexpected or missing PNG')
    for item in entries:
        path = item['path']
        data = (art / path).read_bytes()
        check(sha(data) == PNG_SHA256[path] == item['sha256'], f'selected art SHA-256 mismatch: {path}')
        with Image.open(BytesIO(data)) as image:
            image.load()
            check(list(image.size) == item['size'], f'wrong dimensions: {path}')
    print('Opened 20 selected PNGs and matched independently pinned SHA-256 values.')

    for item in manifest['backgrounds'] + manifest['emotions']:
        source = original / 'source' / item['source']
        expected = subprocess.check_output(['convert', str(source), '-depth', '8', 'rgb:-'])
        actual = Image.open(art / item['path']).convert('RGB').tobytes()
        check(actual == expected, f'original BMP pixels mismatch: {item["path"]}')
    print('Background and emotion pixels match independent ImageMagick decoding.')

    for item in manifest['sprites']:
        path = item['path']
        sprite = Image.open(art / path)
        alpha = list(sprite.getchannel('A').getdata())
        partial = [i for i, value in enumerate(alpha) if value not in (0, 255)]
        check(sprite.mode == 'RGBA' and (len(partial) == 3 and set(alpha[i] for i in partial) == {17, 68} if item['avatar'] == 'bolo' else not partial), f'unexpected sprite transparency: {path}')
        if item['avatar'] in CAST_RECORDS:
            face_index, torso_index = CAST_RECORDS[item['avatar']]
            check(item['face']['index'] == face_index and item['torso']['index'] == torso_index and item['face']['emotion_name'] == 'neutral' and item['torso']['emotion_name'] == 'neutral', f'non-neutral source records: {path}')
        data = (original / 'source' / item['source']).read_bytes()
        face, torso = item['face'], item['torso']

        def bitmap(offset):
            check(data[offset:offset + 2] == b'BM', f'embedded BMP offset mismatch: {path}')
            size, = struct.unpack_from('<I', data, offset + 2)
            bmp = data[offset:offset + size]
            image = Image.open(BytesIO(bmp)).convert('RGB')
            if struct.unpack_from('<I', data, offset + 30)[0] != 0:
                independent = subprocess.check_output(['convert', 'bmp:-', '-depth', '8', 'rgb:-'], input=bmp)
                check(image.tobytes() == independent, f'compressed original BMP mismatch: {path}')
            return image

        head, body = bitmap(face['foreground']), bitmap(torso['foreground'])
        dx, dy = torso['cx'] + face['dx'] - face['cx'], torso['cy'] + face['dy'] - face['cy']
        left, top = min(0, dx), min(0, dy)
        width, height = sprite.size
        check(width == max(body.width, dx + head.width) - left and height == max(body.height, dy + head.height) - top, f'wrong composite bounds: {path}')
        head_pos, body_pos = (dx - left, dy - top), (-left, -top)
        for ground in ((255, 255, 255), (0, 0, 0), (37, 123, 211)):
            destination = bytearray(ground * (width * height))

            def raster(offset, position, mergepaint):
                source = bitmap(offset)
                raw = source.tobytes()
                for y in range(source.height):
                    for x in range(source.width):
                        src = (y * source.width + x) * 3
                        dst = ((y + position[1]) * width + x + position[0]) * 3
                        for channel in range(3):
                            value = raw[src + channel]
                            if mergepaint:
                                destination[dst + channel] |= value ^ 255
                            else:
                                destination[dst + channel] &= value

            for record, pos in ((torso, body_pos), (face, head_pos)):
                if record['aura']:
                    raster(record['aura'], pos, True)
            ordered = [(torso, body_pos, item['flags'] & 2), (face, head_pos, item['flags'] & 1)]
            if not item['flags'] & 4:
                ordered.reverse()
            for record, pos, has_mask in ordered:
                if has_mask and record['mask']:
                    raster(record['mask'], pos, True)
                raster(record['foreground'], pos, False)
            rendered = Image.new('RGBA', sprite.size, ground + (255,))
            rendered.alpha_composite(sprite)
            actual = rendered.convert('RGB').tobytes()
            differing = [i for i in range(width * height) if actual[i * 3:i * 3 + 3] != destination[i * 3:i * 3 + 3]]
            # Bitwise AND with 238/187 at three Bolo boundary pixels depends on
            # individual background bits; standard PNG alpha cannot express it
            # for arbitrary saturated backgrounds. White/black must be exact.
            check(not differing or (path == 'sprites/bolo-neutral.png' and ground == (37, 123, 211) and differing == partial), f'Win32 ROP mismatch: {path}, {ground}')
    print('All 12 figures match independent Win32 ROP on white/black; three Bolo fringe pixels use nearest PNG alpha over blue.')
    print('PASS: selected original-art provenance and extraction checks complete.')


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--asset-root', type=Path, default=DEFAULT_ART)
    parser.add_argument('--source-root', type=Path, default=ROOT)
    args = parser.parse_args()
    verify(args.asset_root, args.source_root)
