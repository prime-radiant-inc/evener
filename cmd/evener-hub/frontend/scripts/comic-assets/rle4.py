"""Minimal Windows BI_RLE4 decoder for the pinned original backdrops.
Pillow 10.2.0 misdecodes these odd-width images. Output was cross-checked pixel
for pixel against the independently installed ImageMagick decoder.
"""
import struct
from PIL import Image


def decode_rle4(path):
    data = path.read_bytes()
    assert data[:2] == b'BM'
    offset, = struct.unpack_from('<I', data, 10)
    header, width, height, planes, bits, compression = struct.unpack_from('<IiiHHI', data, 14)
    assert header == 40 and width > 0 and height > 0
    assert planes == 1 and bits == 4 and compression == 2
    colors, = struct.unpack_from('<I', data, 46)
    palette = []
    for i in range(colors or 16):
        blue, green, red, _ = struct.unpack_from('<BBBB', data, 54 + i * 4)
        palette.extend((red, green, blue))
    pixels = bytearray(width * height)
    x, y, pos = 0, height - 1, offset

    def put(value):
        nonlocal x
        # The original odd-width BMPs encode one padding nibble per row.
        assert 0 <= x <= width and 0 <= y < height
        if x < width:
            pixels[y * width + x] = value
        x += 1

    while pos < len(data):
        count, value = data[pos:pos + 2]
        pos += 2
        if count:
            for i in range(count):
                put(value >> 4 if i % 2 == 0 else value & 15)
        elif value == 0:
            x, y = 0, y - 1
        elif value == 1:
            break
        elif value == 2:
            dx, dy = data[pos:pos + 2]
            pos += 2
            x, y = x + dx, y - dy
        else:
            for i in range(value):
                packed = data[pos + i // 2]
                put(packed >> 4 if i % 2 == 0 else packed & 15)
            byte_count = (value + 1) // 2
            pos += (byte_count + 1) & ~1
    image = Image.frombytes('P', (width, height), bytes(pixels))
    image.putpalette(palette)
    return image.convert('RGB')
