"""Print class means and per-image rate comparisons from measurement files."""
import argparse
import json
from pathlib import Path
import statistics
from rate import compare


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('files', type=Path, nargs='+')
    args = parser.parse_args()
    rows = [json.loads(line) for path in args.files for line in path.read_text().splitlines()]
    keys = [(r['name'], r['codec'], r['quality']) for r in rows]
    if len(set(keys)) != len(keys):
        raise ValueError('duplicate measurements')
    means, comparisons = [], []
    for category in sorted({r['category'] for r in rows}):
        subset = [r for r in rows if r['category'] == category]
        for codec in sorted({r['codec'] for r in subset}):
            for quality in sorted({r['quality'] for r in subset if r['codec'] == codec}):
                samples = [r for r in subset if r['codec'] == codec and r['quality'] == quality]
                mean = dict(category=category, codec=codec, quality=quality, n=len(samples))
                for field in ['bytes', 'ms', 'psnr_rgb', 'psnr_y', 'ssim_y']:
                    values = [r[field] for r in samples if r.get(field) is not None]
                    mean[field] = statistics.mean(values) if values else None
                means.append(mean)
            if codec in ['native', 'cwebp']:
                continue
            for name in sorted({r['name'] for r in subset}):
                ref = [r for r in subset if r['name'] == name and r['codec'] == 'cwebp']
                test = [r for r in subset if r['name'] == name and r['codec'] == codec]
                comparisons.append(dict(name=name, category=category, codec=codec, **compare(ref, test)))
    print(json.dumps(dict(means=means, comparisons=comparisons), indent=2))


if __name__ == '__main__':
    main()
