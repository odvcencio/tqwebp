"""Measure local PNGs. Keep CGo tools outside the encoder module."""
import argparse
import hashlib
import json
from pathlib import Path
import statistics
import subprocess
import time

import numpy as np
from PIL import Image
from skimage.metrics import structural_similarity


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--manifest', type=Path, required=True)
    parser.add_argument('--corpus', type=Path, required=True)
    parser.add_argument('--out', type=Path, required=True)
    parser.add_argument('--bin', type=Path, default=Path('bin'))
    parser.add_argument('--cwebp', default='cwebp')
    parser.add_argument('--dwebp', default='dwebp')
    parser.add_argument('--codecs', nargs='+', default=['tq4', 'tq5', 'tq6', 'chai', 'bep', 'native', 'cwebp'])
    args = parser.parse_args()
    args.out.mkdir(parents=True, exist_ok=True)
    manifest = json.loads(args.manifest.read_text())
    # Check all sources before the first encode. Refuse a stale corpus.
    for item in manifest:
        source = args.corpus / (item['name'] + '.png')
        if hashlib.sha256(source.read_bytes()).hexdigest() != item['sha256']:
            raise ValueError('source hash differs: ' + str(source))
    with (args.out / 'metrics.jsonl').open('x') as metrics:
        for item in manifest:
            name = item['name']
            source = args.corpus / (name + '.png')
            original = np.asarray(Image.open(source).convert('RGB'), dtype=np.float64)
            if (original.shape[1], original.shape[0]) != (item['width'], item['height']):
                raise ValueError('source dimensions differ: ' + name)
            luma = original @ np.array([.299, .587, .114])
            ppm = args.out / (name + '.ppm')
            Image.open(source).convert('RGB').save(ppm)
            for codec in args.codecs:
                for quality in ([100] if codec == 'native' else [50, 75, 90]):
                    output = args.out / f'{name}.{codec}.q{quality}.webp'
                    if codec == 'cwebp':
                        command = [args.cwebp, '-quiet', '-q', str(quality), '-m', '4', str(ppm), '-o', str(output)]
                        subprocess.run(command, check=True)
                        times = []
                        for _ in range(5):
                            start = time.perf_counter()
                            subprocess.run(command, check=True)
                            times.append((time.perf_counter() - start) * 1000)
                        row = dict(codec=codec, quality=quality, bytes=output.stat().st_size,
                                   ms=statistics.median(times), times_ms=times)
                    else:
                        binary = args.bin / ('encode-bep' if codec == 'bep' else 'encode')
                        command = [str(binary.resolve()), '-codec', codec, '-input', str(source),
                                   '-output', str(output), '-quality', str(quality), '-repeats',
                                   str(3 if codec in ['tq5', 'tq6'] else 5)]
                        row = json.loads(subprocess.check_output(command))
                    decoded = output.with_suffix('.png')
                    subprocess.run([args.dwebp, '-quiet', str(output), '-o', str(decoded)], check=True)
                    actual = np.asarray(Image.open(decoded).convert('RGB'), dtype=np.float64)
                    if actual.shape != original.shape:
                        raise ValueError('decoded dimensions differ: ' + str(output))
                    mse = float(np.mean((actual - original) ** 2))
                    actual_luma = actual @ np.array([.299, .587, .114])
                    y_mse = float(np.mean((actual_luma - luma) ** 2))
                    row.update(name=name, category=item['category'], width=item['width'], height=item['height'],
                               psnr_rgb=psnr(mse), psnr_y=psnr(y_mse), exact=mse == 0,
                               ssim_y=float(structural_similarity(luma, actual_luma, data_range=255,
                                           gaussian_weights=True, sigma=1.5, use_sample_covariance=False)))
                    metrics.write(json.dumps(row) + '\n')
                    metrics.flush()
                    print(name, codec, quality, row['bytes'], round(row['ms'], 2), flush=True)


def psnr(mse):
    # JSON null means zero error, which has infinite PSNR.
    return float(10 * np.log10(255 ** 2 / mse)) if mse else None


if __name__ == '__main__':
    main()
