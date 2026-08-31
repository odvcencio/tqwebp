#!/usr/bin/env python3
"""Ask libwebp whether tqwebp's alpha planes survived.

Gate G5 already checks every alpha plane through golang.org/x/image/webp,
a second, independent Go decoder. This script adds the reference decoder:
libwebp itself, through Pillow. Compression method 0 stores the alpha
plane sample for sample, so libwebp must return every sample unchanged.

Produce the fixtures first, then run this script over them:

    go run ./cmd/tqbench -gates -alpha-dir /tmp/tqwebp-alpha
    python3 tools/libwebp_alpha_check.py /tmp/tqwebp-alpha

Each fixture is a pair: NAME.webp, which tqwebp wrote, and NAME.png, the
source picture in a lossless format. The script exits non-zero when one
alpha sample moved.

The script needs Pillow with WebP support. No Go tool can run it, which
is the point: it reads the bytes with a decoder this repository does not
build.
"""

import glob
import math
import os
import sys

try:
    from PIL import Image, features
except ImportError:
    sys.stderr.write("libwebp_alpha_check: Pillow is not installed\n")
    raise SystemExit(2)


def main(argv):
    if len(argv) != 2:
        sys.stderr.write("usage: libwebp_alpha_check.py DIR\n")
        return 2
    if not features.check("webp"):
        sys.stderr.write("libwebp_alpha_check: this Pillow has no WebP support\n")
        return 2

    directory = argv[1]
    paths = sorted(glob.glob(os.path.join(directory, "*.webp")))
    if not paths:
        sys.stderr.write("libwebp_alpha_check: %s holds no .webp file\n" % directory)
        return 2

    print("%-34s %-12s %-6s %-11s %s" % (
        "case", "size", "mode", "alpha", "colour rmse where alpha is 255"))
    failures = 0
    for path in paths:
        name = os.path.basename(path)[: -len(".webp")]
        source_path = os.path.join(directory, name + ".png")
        if not os.path.exists(source_path):
            print("%-34s no .png source next to the .webp" % name)
            failures += 1
            continue

        source = Image.open(source_path).convert("RGBA")
        decoded = Image.open(path)
        mode = decoded.mode
        decoded = decoded.convert("RGBA")

        if source.size != decoded.size:
            print("%-34s size %s decoded as %s" % (name, source.size, decoded.size))
            failures += 1
            continue

        width = source.size[0]
        mismatch = None
        squared = 0.0
        samples = 0
        for i, (s, d) in enumerate(zip(source.getdata(), decoded.getdata())):
            if s[3] != d[3] and mismatch is None:
                mismatch = (i % width, i // width, d[3], s[3])
            if s[3] == 255:
                for channel in range(3):
                    squared += (s[channel] - d[channel]) ** 2
                    samples += 1

        if mismatch is None:
            verdict = "exact"
        else:
            verdict = "MOVED at (%d,%d): %d, want %d" % mismatch
            failures += 1
        rmse = math.sqrt(squared / samples) if samples else float("nan")
        print("%-34s %-12s %-6s %-11s %.2f" % (
            name, "%dx%d" % source.size, mode, verdict, rmse))

    print()
    if failures:
        print("libwebp_alpha_check: %d of %d fixtures FAILED" % (failures, len(paths)))
        return 1
    print("libwebp_alpha_check: all %d alpha planes are byte exact through libwebp" % len(paths))
    return 0


if __name__ == "__main__":
    raise SystemExit(main(sys.argv))
