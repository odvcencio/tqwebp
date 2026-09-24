"""Integrate log rate on the shared SSIM-dB range. Never extrapolate."""
import math
import numpy as np


def curve(rows):
    rows = sorted(rows, key=lambda row: row['quality'])
    if len(rows) < 3:
        raise ValueError('fewer than three points')
    if any(not 0 < row['ssim_y'] < 1 or row['bytes'] <= 0 for row in rows):
        raise ValueError('invalid lossy point')
    if any(a['ssim_y'] >= b['ssim_y'] or a['bytes'] >= b['bytes'] for a, b in zip(rows, rows[1:])):
        raise ValueError('nonmonotone')
    return (np.array([-10 * math.log10(1 - row['ssim_y']) for row in rows]),
            np.log([row['bytes'] for row in rows]))


def compare(reference, candidate):
    try:
        ref, test = curve(reference), curve(candidate)
    except ValueError as error:
        return {'status': str(error)}
    low, high = max(ref[0][0], test[0][0]), min(ref[0][-1], test[0][-1])
    if high <= low:
        return {'status': 'no-overlap'}
    knots = np.unique(np.concatenate(([low, high], *[x[(x > low) & (x < high)] for x, _ in [ref, test]])))
    delta = np.interp(knots, *test) - np.interp(knots, *ref)
    bd = float(np.exp(np.trapezoid(delta, knots) / (high - low)) - 1)
    q75 = next((row for row in reference if row['quality'] == 75), None)
    ratio = None
    if q75:
        target = -10 * math.log10(1 - q75['ssim_y'])
        if test[0][0] <= target <= test[0][-1]:
            ratio = float(np.exp(np.interp(target, *test)) / q75['bytes'])
    return dict(status='ok', bd_percent=bd * 100, matched_q75_ratio=ratio,
                overlap_ssimdb=[low, high])
