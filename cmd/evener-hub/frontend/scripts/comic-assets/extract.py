#!/usr/bin/env python3
"""Offline extraction of selected original Comic Chat art for the personal client.
No downloads and no dependencies beyond Pillow. See README.md.
"""
from pathlib import Path
from io import BytesIO
import hashlib
import json
import struct
from PIL import Image
from rle4 import decode_rle4

ROOT = Path(__file__).resolve().parent
OUT = ROOT.parent.parent / 'public/webassets/comic'
ART = ROOT / 'source/v1.0-pre/client/comicart'
REVISION = '48a162249484ab8d116c243e8203b0956d350c09'
EMOTIONS = ['unspecified', 'happy', 'coy', 'bored', 'scared', 'sad', 'angry',
            'shout', 'laugh', 'neutral', 'wave', 'point-other', 'point-self',
            'double-point', 'shrug', '3qr-walk', 'side-walk', '3qf-walk']
# Author-selected combinations of original records, not claimed canonical pairs.
SELECTIONS = {
    'anna': [('neutral', 0, 0), ('happy-wave', 1, 7), ('angry', 5, 5),
             ('scared', 7, 10), ('point', 16, 15), ('celebrate', 6, 8)],
    'xeno': [('neutral', 0, 0), ('point', 1, 3)],
    # First original face AND torso records whose source emotion is neutral.
    'armando': [('neutral', 0, 0)],
    'bolo': [('neutral', 0, 0)],
    'denise': [('neutral', 3, 4)],
    'tiki': [('neutral', 2, 0)],
}


class Avatar:
    def __init__(self, name):
        self.name = name
        self.data = (ART / 'avatars' / f'{name}.avb').read_bytes()
        self.pos = 0
        magic, self.kind, self.version = self.read('<hhh')
        assert magic == 0x81 and self.kind == 2
        self.records = {}
        while True:
            key, = self.read('<h')
            if key == 1:
                end = self.data.index(b'\0', self.pos)
                self.internal_name = self.data[self.pos:end].decode('cp1252')
                self.pos = end + 1
            elif key in (2, 8):
                val, = self.read('<h')
                if key == 2:
                    self.flags = val
                else:
                    self.style = val
            elif key == 3:
                self.icon_offset, = self.read('<I')
            elif key in (4, 5):
                count, = self.read('<h')
                group = 'faces' if key == 4 else 'torsos'
                records = []
                for index in range(count):
                    foreground, mask, aura, emotion, intensity = self.read('<IIIhB')
                    cx, cy = self.read('<hh')
                    rec = dict(index=index, foreground=foreground, mask=mask,
                               aura=aura, emotion=emotion,
                               emotion_name=EMOTIONS[emotion], intensity=intensity,
                               cx=cx, cy=cy)
                    if key == 4:
                        dx, dy, face_x, face_y = self.read('<hhhh')
                        rec.update(dx=dx, dy=dy, face_x=face_x, face_y=face_y)
                    self.pos += 16
                    records.append(rec)
                self.records[group] = records
            elif key == 6:
                break
            else:
                raise ValueError(f'Unknown AVB key {key} at {self.pos - 2}')

    def read(self, fmt):
        values = struct.unpack_from(fmt, self.data, self.pos)
        self.pos += struct.calcsize(fmt)
        return values

    def bitmap(self, offset):
        assert self.data[offset:offset + 2] == b'BM'
        size, = struct.unpack_from('<I', self.data, offset + 2)
        image = Image.open(BytesIO(self.data[offset:offset + size]))
        image.load()
        return image.convert('RGB')

    def composite(self, face_index, torso_index):
        face = self.records['faces'][face_index]
        torso = self.records['torsos'][torso_index]
        head = self.bitmap(face['foreground'])
        body = self.bitmap(torso['foreground'])
        dx = torso['cx'] + face['dx'] - face['cx']
        dy = torso['cy'] + face['dy'] - face['cy']
        left, top = min(0, dx), min(0, dy)
        right = max(body.width, dx + head.width)
        bottom = max(body.height, dy + head.height)
        output = Image.new('RGBA', (right - left, bottom - top))
        head_pos, body_pos = (dx - left, dy - top), (-left, -top)

        if self.name in ('bolo', 'denise'):
            output = self.composite_shaded(face, torso, head_pos, body_pos, output.size)
            return output, dict(face=face, torso=torso, head_offset=[dx, dy],
                                canvas_origin=[left, top], size=list(output.size),
                                flags=self.flags, nimbus=True)

        def draw(offset, pos, size, white):
            bitmap = self.bitmap(offset)
            assert bitmap.size == size
            # The body/face masks are binary; Bolo and Denise's foreground
            # drawings include original gray shading which must not be erased.
            if white:
                assert set(bitmap.getdata()) <= {(0, 0, 0), (255, 255, 255)}
            alpha = bitmap.convert('L').point(lambda v: 255 if v < 255 else 0)
            layer = Image.new('RGBA', size, 'white') if white else bitmap.convert('RGBA')
            layer.putalpha(alpha)
            output.alpha_composite(layer, pos)

        # Faithful binary equivalent of bodycam.cpp DrawBody(drawNimbus=TRUE):
        # MERGEPAINT(S,D) = (~S)|D -> white where S is black.
        # SRCAND(S,D) = S&D -> black where S is black.
        for rec, pos, size in ((torso, body_pos, body.size), (face, head_pos, head.size)):
            if rec['aura']:
                draw(rec['aura'], pos, size, white=True)
        layers = [(torso, body_pos, body.size, self.flags & 2),
                  (face, head_pos, head.size, self.flags & 1)]
        if not self.flags & 4:
            layers.reverse()
        for rec, pos, size, use_mask in layers:
            if use_mask and rec['mask']:
                draw(rec['mask'], pos, size, white=True)
            draw(rec['foreground'], pos, size, white=False)
        return output, dict(face=face, torso=torso, head_offset=[dx, dy],
                            canvas_origin=[left, top], size=list(output.size),
                            flags=self.flags, nimbus=True)

    def composite_shaded(self, face, torso, head_pos, body_pos, size):
        """Carry the source's grayscale ROP bits, rather than painting gray over ink.

        Most pixels become opaque or transparent. Three Bolo boundary pixels
        retain a partial background bit mask; RGBA approximates those three on
        non-white grounds, while reproducing original white-paper pixels exactly.
        """
        width, height = size
        masks = bytearray([255]) * (width * height * 3)
        inks = bytearray(width * height * 3)

        def raster(offset, position, mergepaint):
            bitmap = self.bitmap(offset)
            raw = bitmap.tobytes()
            for y in range(bitmap.height):
                for x in range(bitmap.width):
                    src = (y * bitmap.width + x) * 3
                    dst = ((y + position[1]) * width + x + position[0]) * 3
                    for channel in range(3):
                        value = raw[src + channel]
                        at = dst + channel
                        if mergepaint:
                            inks[at] |= value ^ 255
                        else:
                            inks[at] &= value
                        masks[at] &= value

        for rec, pos in ((torso, body_pos), (face, head_pos)):
            if rec['aura']:
                raster(rec['aura'], pos, True)
        layers = [(torso, body_pos, self.flags & 2), (face, head_pos, self.flags & 1)]
        if not self.flags & 4:
            layers.reverse()
        for rec, pos, use_mask in layers:
            if use_mask and rec['mask']:
                raster(rec['mask'], pos, True)
            raster(rec['foreground'], pos, False)

        pixels = []
        for at in range(0, len(masks), 3):
            mask, ink = masks[at], inks[at]
            assert masks[at:at + 3] == bytes((mask, mask, mask))
            pixels.append((ink, ink, ink, 255 - mask))
        output = Image.new('RGBA', size)
        output.putdata(pixels)
        return output


def main():
    manifest = dict(revision=REVISION, sprites=[], backgrounds=[], emotions=[])
    for folder in ('sprites', 'backgrounds', 'emotions'):
        (OUT / folder).mkdir(parents=True, exist_ok=True)
    for name, selections in SELECTIONS.items():
        av = Avatar(name)
        for pose, face_index, torso_index in selections:
            image, metadata = av.composite(face_index, torso_index)
            path = f'sprites/{name}-{pose}.png'
            image.save(OUT / path)
            metadata.update(path=path, avatar=name, pose=pose,
                            source=f'v1.0-pre/client/comicart/avatars/{name}.avb')
            manifest['sprites'].append(metadata)
            print(path, *image.size)
    for name in ('field', 'room8bs'):
        source = ART / 'backdrop' / f'{name}.bmp'
        image = decode_rle4(source)
        path = f'backgrounds/{name}.png'
        image.save(OUT / path)
        manifest['backgrounds'].append(dict(path=path, size=list(image.size),
            source=str(source.relative_to(ROOT / 'source'))))
        print(path, *image.size)
    for code in ('neu', 'hap', 'bor', 'sca', 'ang', 'laf'):
        source = ART / 'avatars' / f'fc_{code}_s.bmp'
        image = Image.open(source).convert('RGB')
        path = f'emotions/fc_{code}_s.png'
        image.save(OUT / path)
        manifest['emotions'].append(dict(path=path, size=list(image.size),
            source=str(source.relative_to(ROOT / 'source'))))
        print(path, *image.size)
    for group in ('sprites', 'backgrounds', 'emotions'):
        for item in manifest[group]:
            item['sha256'] = hashlib.sha256((OUT / item['path']).read_bytes()).hexdigest()
    (OUT / 'manifest.json').write_text(json.dumps(manifest, indent=2) + '\n')


if __name__ == '__main__':
    main()
