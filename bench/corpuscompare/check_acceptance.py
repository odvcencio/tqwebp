"""Refuse missing measurements or a Method 6 quality collapse.

Run after measure.py with tq5 and tq6 at qualities 50, 75, and 90. This
guard compares decoded RGB-luma scores; it is not a libwebp-parity claim.
"""
import argparse
import json
import math
from pathlib import Path


def check(rows, manifest):
    if not manifest:
        raise ValueError('manifest is empty')
    expected = {}
    for item in manifest:
        name = item['name']
        if not isinstance(name, str) or not name.strip() or name in expected:
            raise ValueError('manifest names must be unique nonempty strings')
        if any(type(item[field]) is not int or item[field] <= 0
               for field in ('width', 'height')):
            raise ValueError('manifest dimensions must be positive integers')
        expected[name] = item
    selected = [r for r in rows if r['codec'] in ('tq5', 'tq6')]
    keyed = {(r['name'], r['codec'], r['quality']): r for r in selected}
    if len(keyed) != len(selected):
        raise ValueError('duplicate measurements')
    required = {(name, codec, q) for name in expected
                for codec in ('tq5', 'tq6') for q in (50, 75, 90)}
    if set(keyed) != required:
        raise ValueError('incomplete or unexpected tq5/tq6 sweep')
    failures = []
    for name, item in expected.items():
        for q in (50, 75, 90):
            baseline, candidate = (keyed[name, codec, q] for codec in ('tq5', 'tq6'))
            for row in (baseline, candidate):
                if (any(type(row[field]) is not int for field in ('width', 'height')) or
                        (row['width'], row['height']) != (item['width'], item['height'])):
                    raise ValueError('dimensions differ: ' + name)
                if (type(row['bytes']) is not int or row['bytes'] <= 0 or
                        type(row['ssim_y']) not in (int, float) or
                        not math.isfinite(row['ssim_y']) or
                        not 0 <= row['ssim_y'] <= 1 or
                        (row['psnr_y'] is not None and
                         (type(row['psnr_y']) not in (int, float) or
                          not math.isfinite(row['psnr_y']))) or
                        (row['psnr_y'] is None and row['ssim_y'] != 1)):
                    raise ValueError('invalid measurement: ' + name)
            if (candidate['ssim_y'] < baseline['ssim_y'] - .005 or
                    (float('inf') if candidate['psnr_y'] is None else candidate['psnr_y']) <
                    (float('inf') if baseline['psnr_y'] is None else baseline['psnr_y']) - .5):
                failures.append(f'{name} q{q}: Method 6 loses more than .005 SSIM or .5 dB luma PSNR')
    if failures:
        raise ValueError('\n'.join(failures))
    return len(required) // 2


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('metrics', type=Path)
    parser.add_argument('--manifest', type=Path, required=True)
    args = parser.parse_args()
    rows = [json.loads(line) for line in args.metrics.read_text().splitlines()]
    print(f'PASS: {check(rows, json.loads(args.manifest.read_text()))} Method 6 quality comparisons')


if __name__ == '__main__':
    main()
