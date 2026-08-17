#!/usr/bin/env python3
"""Measure libwebp on the tqwebp corpus and write the gate fixture.

The fixture arms the informative half of gate G3: how many bytes libwebp
spends for the picture quality tqwebp reaches. libwebp reaches this
script through Pillow, which links the same encoder and decoder the cwebp
and dwebp command line tools use.

The script also measures tqwebp's own files when a directory of them is
given, which is the differential check of specification section 11.3: the
numbers Python computes here must match the numbers the Go oracle
computes, because both follow the same two definitions.

Luma follows the conversion image/color applies in Go, so the numbers are
directly comparable with oracle.MeasurePSNR:

    Y = (19595*R + 38470*G + 7471*B + 32768) >> 16

Usage:

    python3 tools/libwebp_baseline.py \
        --corpus testdata/corpus \
        --out testdata/golden/libwebp_baseline.json \
        [--tqwebp-dir DIR --tqwebp-out FILE]
"""

import argparse
import hashlib
import io
import json
import math
import os
import sys

from PIL import Image, features


QUALITIES = [50, 75, 85, 90, 95]
CHECK_QUALITIES = [10, 25, 50, 75, 85, 90, 95]
CALIBRATION_QUALITIES = [1, 5, 10, 15, 20, 25, 30, 35, 40, 45, 50, 55, 60, 65, 70, 75, 80, 85, 90, 95, 100]


def luma(rgb_bytes):
    """Return the Go image/color luma of a flat RGB byte string."""
    out = bytearray(len(rgb_bytes) // 3)
    mv = memoryview(rgb_bytes)
    for i in range(len(out)):
        r = mv[3 * i]
        g = mv[3 * i + 1]
        b = mv[3 * i + 2]
        out[i] = (19595 * r + 38470 * g + 7471 * b + 32768) >> 16
    return out


def psnr(a, b):
    if len(a) != len(b):
        raise ValueError("plane size mismatch")
    total = 0
    for x, y in zip(a, b):
        d = x - y
        total += d * d
    if total == 0:
        return float("inf")
    mse = total / len(a)
    return 10 * math.log10(255 * 255 / mse)


def measure(source_rgb, encoded_bytes):
    decoded = Image.open(io.BytesIO(encoded_bytes)).convert("RGB")
    return psnr(luma(source_rgb.tobytes()), luma(decoded.tobytes()))


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--corpus", default="testdata/corpus")
    parser.add_argument("--out", default="testdata/golden/libwebp_baseline.json")
    parser.add_argument("--tqwebp-dir", default="")
    parser.add_argument("--tqwebp-out", default="")
    parser.add_argument("--calibration-out", default="")
    args = parser.parse_args()

    if not features.check("webp"):
        print("this Pillow build has no WebP support", file=sys.stderr)
        return 1

    names = sorted(
        os.path.splitext(name)[0]
        for name in os.listdir(args.corpus)
        if name.endswith(".png")
    )

    points = {}
    for name in names:
        source = Image.open(os.path.join(args.corpus, name + ".png")).convert("RGB")
        rows = []
        for quality in QUALITIES:
            buf = io.BytesIO()
            source.save(buf, "WEBP", quality=quality, method=4, lossless=False)
            data = buf.getvalue()
            rows.append(
                {
                    "quality": quality,
                    "bytes": len(data),
                    "display_y_psnr_db": round(measure(source, data), 4),
                }
            )
        points[name] = rows
        print("measured", name, flush=True)

    if args.calibration_out:
        calibration = {}
        for name in names:
            if not name.startswith("photo_"):
                continue
            source = Image.open(os.path.join(args.corpus, name + ".png")).convert("RGB")
            rows = []
            for quality in CALIBRATION_QUALITIES:
                buf = io.BytesIO()
                source.save(buf, "WEBP", quality=quality, method=4, lossless=False)
                data = buf.getvalue()
                rows.append({
                    "quality": quality,
                    "bytes": len(data),
                    "display_y_psnr_db": round(measure(source, data), 4),
                })
            calibration[name] = rows
            print("calibrated", name, flush=True)
        write_json(args.calibration_out, {"points": calibration})
        print("wrote", args.calibration_out)

    fixture = {
        "producer": {
            "library": "libwebp",
            "version": features.version("webp"),
            "reached_through": "Pillow " + Image.__version__,
            "settings": "quality sweep at method 4, lossless off",
        },
        "measure": {
            "display_y_psnr_db": "luma PSNR after libwebp's own decode, with the luma of Go's image/color",
            "formula": "Y = (19595*R + 38470*G + 7471*B + 32768) >> 16",
        },
        "corpus_sha256": corpus_hash(args.corpus, names),
        "points": points,
    }
    write_json(args.out, fixture)
    print("wrote", args.out)

    if args.tqwebp_dir:
        checks = []
        for name in names:
            source = Image.open(os.path.join(args.corpus, name + ".png")).convert("RGB")
            for quality in CHECK_QUALITIES:
                path = os.path.join(args.tqwebp_dir, "%s_q%d.webp" % (name, quality))
                if not os.path.exists(path):
                    continue
                with open(path, "rb") as handle:
                    data = handle.read()
                checks.append(
                    {
                        "image": name,
                        "quality": quality,
                        "bytes": len(data),
                        "display_y_psnr_db": round(measure(source, data), 4),
                    }
                )
        out = args.tqwebp_out or "libwebp_check.json"
        write_json(out, {"checks": checks})
        print("wrote", out)
    return 0


def corpus_hash(directory, names):
    digest = hashlib.sha256()
    for name in names:
        with open(os.path.join(directory, name + ".png"), "rb") as handle:
            digest.update(handle.read())
    return digest.hexdigest()


def write_json(path, payload):
    with open(path, "w") as handle:
        json.dump(payload, handle, indent=2, sort_keys=True)
        handle.write("\n")


if __name__ == "__main__":
    raise SystemExit(main())
