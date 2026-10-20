#!/usr/bin/env python3
"""Development-only synthetic fixtures, no production code is imported.
Run from repository root: python3 llm/internal/imagerepair/testdata/generate.py
Requires Pillow and liblcms2. No network, credentials, or photographs.
"""
import ctypes as C
import ctypes.util
import io
import json
import math
import struct
from pathlib import Path
from PIL import Image, ImageCms

OUT = Path(__file__).resolve().parent
lcms = C.CDLL(ctypes.util.find_library("lcms2"))

class xyY(C.Structure):
    _fields_ = [("x", C.c_double), ("y", C.c_double), ("Y", C.c_double)]

class Primaries(C.Structure):
    _fields_ = [("Red", xyY), ("Green", xyY), ("Blue", xyY)]

lcms.cmsBuildParametricToneCurve.argtypes = [C.c_void_p, C.c_int, C.POINTER(C.c_double)]
lcms.cmsBuildParametricToneCurve.restype = C.c_void_p
lcms.cmsCreateRGBProfile.argtypes = [C.POINTER(xyY), C.POINTER(Primaries), C.POINTER(C.c_void_p)]
lcms.cmsCreateRGBProfile.restype = C.c_void_p
lcms.cmsSaveProfileToMem.argtypes = [C.c_void_p, C.c_void_p, C.POINTER(C.c_uint32)]
lcms.cmsCloseProfile.argtypes = [C.c_void_p]
lcms.cmsFreeToneCurve.argtypes = [C.c_void_p]

# Display P3 chromaticities and the IEC 61966-2-1 sRGB transfer function.
white = xyY(.3127, .3290, 1)
primaries = Primaries(xyY(.680, .320, 1), xyY(.265, .690, 1), xyY(.150, .060, 1))
params = (C.c_double * 5)(2.4, 1/1.055, .055/1.055, 1/12.92, .04045)
curve = lcms.cmsBuildParametricToneCurve(None, 4, params)
p3 = lcms.cmsCreateRGBProfile(C.byref(white), C.byref(primaries), (C.c_void_p * 3)(curve, curve, curve))
size = C.c_uint32()
assert lcms.cmsSaveProfileToMem(p3, None, C.byref(size))
buf = C.create_string_buffer(size.value)
assert lcms.cmsSaveProfileToMem(p3, buf, C.byref(size))
profiles = {"p3": buf.raw, "srgb": ImageCms.ImageCmsProfile(ImageCms.createProfile("sRGB")).tobytes()}
lcms.cmsCloseProfile(p3)
lcms.cmsFreeToneCurve(curve)

# Replace only the TRC payloads using the ICC wire format, then ask LittleCMS
# (not the Go implementation) to interpret them. All other tags remain intact.
def with_curve(profile, payload):
    count = struct.unpack_from(">I", profile, 128)[0]
    entries = [struct.unpack_from(">4sII", profile, 132+12*i) for i in range(count)]
    result = bytearray(profile[:132+12*count])
    for i, (name, off, length) in enumerate(entries):
        data = payload if name in (b"rTRC", b"gTRC", b"bTRC") else profile[off:off+length]
        result.extend(b"\x00" * (-len(result) % 4))
        struct.pack_into(">4sII", result, 132+12*i, name, len(result), len(data))
        result.extend(data)
    result.extend(b"\x00" * (-len(result) % 4))
    struct.pack_into(">I", result, 0, len(result))
    return bytes(result)

def parametric(kind, values):
    return b"para\x00\x00\x00\x00" + struct.pack(">HH", kind, 0) + b"".join(
        struct.pack(">i", round(v*65536)) for v in values)

for kind, values in enumerate([
    [2.2], [2, 1.1, -.1], [2, math.sqrt(.98)+.1, -.1, .02],
    [2.4, 1/1.055, .055/1.055, 1/12.92, .04045],
    [2, math.sqrt(.98), 0, .196, .2, .02, .02],
]):
    profiles["para"+str(kind)] = with_curve(profiles["p3"], parametric(kind, values))
for name, table in [("identity", []), ("gamma", [round(2.2*256)]),
                    ("sampled", [round((i/16)**1.8 * 65535) for i in range(17)])]:
    payload = b"curv\x00\x00\x00\x00" + struct.pack(">I",len(table))
    payload += b"".join(struct.pack(">H",v) for v in table)
    profiles[name] = with_curve(profiles["p3"], payload)

samples = [(0,0,0),(255,255,255),(128,128,128),(16,16,16),(64,64,64),(240,240,240),
           (255,0,0),(0,255,0),(0,0,255),(255,255,0),(0,255,255),(255,0,255),
           (180,70,40),(40,180,70),(70,40,180),(120,140,160),(5,10,15),(250,180,120)]
image = Image.new("RGB", (48, 48))
for i, rgb in enumerate(samples):
    x, y = (i % 6)*8, (i // 6)*16
    image.paste(rgb, (x, y, x+8, y+16))
image.save(OUT / "patches.jpg", quality=100, subsampling=0)
image.save(OUT / "progressive.jpg", quality=100, subsampling=0, progressive=True)
image.convert("CMYK").save(OUT / "cmyk.jpg", quality=100)
reference = {"generator": {"Pillow": Image.__version__, "LittleCMS": ImageCms.core.littlecms_version,
                         "intent": "relative colorimetric", "black_point_compensation": False}, "profiles": {}}
dst = ImageCms.createProfile("sRGB")
for name, data in profiles.items():
    # Normalize only creation timestamp, keeping regeneration deterministic.
    data = data[:24] + bytes.fromhex("07e900010001000000000000") + data[36:]
    (OUT / (name + ".icc")).write_bytes(data)
    src = ImageCms.ImageCmsProfile(io.BytesIO(data))
    transform = ImageCms.buildTransform(src, dst, "RGB", "RGB", renderingIntent=1, flags=0x100) # NOOPTIMIZE
    row = Image.new("RGB", (len(samples),1))
    row.putdata(samples)
    result = ImageCms.applyTransform(row, transform)
    reference["profiles"][name] = [{"input": list(a), "output": list(b)} for a,b in zip(samples,result.getdata())]
    if name in ("p3", "srgb"):
        with Image.open(OUT / "patches.jpg") as jpeg:
            # Store pixels only, not the newly timestamped destination profile.
            ImageCms.applyTransform(jpeg, transform).save(OUT / (name + "-reference.png"), icc_profile=None)
(OUT / "samples.json").write_text(json.dumps(reference, indent=2) + "\n")
print(json.dumps(reference["generator"]))
