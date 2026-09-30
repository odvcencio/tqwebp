import copy
import json
from pathlib import Path
import unittest
from check_acceptance import check


class AcceptanceTests(unittest.TestCase):
    def setUp(self):
        self.manifest = [dict(name='fixture', width=32, height=24)]
        self.rows = [dict(name='fixture', codec=codec, quality=q, width=32,
                         height=24, bytes=100, ssim_y=.95, psnr_y=36.)
                     for codec in ('tq5', 'tq6') for q in (50, 75, 90)]

    def test_equal_quality_passes(self):
        self.assertEqual(check(self.rows, self.manifest), 3)

    def test_missing_or_duplicate_refused(self):
        for rows in (self.rows[:-1], self.rows + [self.rows[0]]):
            with self.assertRaises(ValueError):
                check(rows, self.manifest)

    def test_collapsed_quality_refused(self):
        rows = copy.deepcopy(self.rows)
        rows[-1]['ssim_y'] = .7
        with self.assertRaises(ValueError):
            check(rows, self.manifest)

    def test_invalid_metrics_refused(self):
        for field, value in [('ssim_y', float('nan')), ('psnr_y', float('inf')),
                             ('width', 33), ('bytes', 0)]:
            rows = copy.deepcopy(self.rows)
            rows[-1][field] = value
            with self.assertRaises(ValueError):
                check(rows, self.manifest)

    def test_boolean_or_nonnumeric_quality_scores_refused(self):
        for field in ('ssim_y', 'psnr_y'):
            for value in (True, False, '1', [], {}):
                rows = copy.deepcopy(self.rows)
                for row in rows:
                    row[field] = value
                with self.assertRaises(ValueError):
                    check(rows, self.manifest)

    def test_empty_duplicate_or_invalid_manifest_refused(self):
        for manifest in ([], self.manifest * 2,
                         [dict(name=' ', width=32, height=24)],
                         [dict(name='fixture', width=32., height=24)],
                         [dict(name='fixture', width=True, height=24)]):
            with self.assertRaises(ValueError):
                check([], manifest)

    def test_noninteger_or_nonfinite_bytes_refused(self):
        for value in (float('nan'), float('inf'), 1.5, True):
            rows = copy.deepcopy(self.rows)
            rows[-1]['bytes'] = value
            with self.assertRaises(ValueError):
                check(rows, self.manifest)

    def test_exact_images_have_infinite_psnr(self):
        rows = copy.deepcopy(self.rows)
        for row in rows:
            row['ssim_y'] = 1.
            row['psnr_y'] = None  # measurement JSON's infinite-PSNR sentinel
        self.assertEqual(check(rows, self.manifest), 3)

    def test_psnr_only_loss_refused(self):
        rows = copy.deepcopy(self.rows)
        rows[-1]['psnr_y'] = 35.49
        with self.assertRaisesRegex(ValueError, 'Method 6 loses'):
            check(rows, self.manifest)

    def test_loss_budget_boundaries(self):
        rows = copy.deepcopy(self.rows)
        rows[-1]['ssim_y'] = .95 - .005
        rows[-1]['psnr_y'] = 36. - .5
        self.assertEqual(check(rows, self.manifest), 3)
        for field in ('ssim_y', 'psnr_y'):
            outside = copy.deepcopy(rows)
            outside[-1][field] -= 1e-8
            with self.assertRaises(ValueError):
                check(outside, self.manifest)

    def test_historical_method6_regression_refused(self):
        root = Path(__file__).parent / 'results'
        rows = [json.loads(line) for line in
                (root / 'metrics-tq5-tq6.jsonl').read_text().splitlines()]
        manifest = json.loads((root / 'corpus-2026-09-23.json').read_text())
        with self.assertRaisesRegex(ValueError, 'Method 6 loses'):
            check(rows, manifest)


if __name__ == '__main__':
    unittest.main()
