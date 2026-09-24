import unittest
from rate import compare


class RateTest(unittest.TestCase):
    def setUp(self):
        self.reference = [dict(quality=q, bytes=size, ssim_y=ssim)
                          for q, size, ssim in [(50, 100, .9), (75, 200, .95), (90, 400, .98)]]

    def test_constant_rate_factor(self):
        for factor in [1, 1.25, 2]:
            candidate = [dict(row, bytes=row['bytes'] * factor) for row in self.reference]
            result = compare(self.reference, candidate)
            self.assertAlmostEqual(result['bd_percent'], (factor - 1) * 100)
            self.assertAlmostEqual(result['matched_q75_ratio'], factor)

    def test_no_extrapolation(self):
        candidate = [dict(row, ssim_y=row['ssim_y'] - .3) for row in self.reference]
        self.assertEqual(compare(self.reference, candidate)['status'], 'no-overlap')

    def test_nonmonotone(self):
        candidate = [dict(row) for row in self.reference]
        candidate[1]['ssim_y'] = .99
        self.assertEqual(compare(self.reference, candidate)['status'], 'nonmonotone')


if __name__ == '__main__':
    unittest.main()
