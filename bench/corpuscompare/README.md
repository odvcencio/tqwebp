# Corpus comparison

This tool compares tqwebp with libwebp on local PNG files. It is a separate
Go module. Its CGo and Python dependencies do not enter the encoder module.

## Run

Install `cwebp` and `dwebp` as test tools. Build the two Go programs separately.
The two CGo packages export the same C symbols and cannot share one binary.

```sh
GOWORK=off go build -o bin/encode .
GOWORK=off go build -tags bep -o bin/encode-bep .
python3 -m venv .venv
.venv/bin/pip install -r requirements.txt
.venv/bin/python -m unittest test_rate.py
nice -n 10 .venv/bin/python measure.py \
  --manifest results/corpus-2026-09-23.json \
  --corpus /path/to/preserved/pngs --out out/run1
.venv/bin/python summarize.py out/run1/metrics.jsonl > out/run1/summary.json
```

The manifest records source URLs, dimensions, and SHA-256 hashes. Supply those
PNG files or create a new manifest for another corpus. The tool checks each
hash. It does not download source images. The source sites can change. A fresh
screenshot of a live page will need a new manifest. The measurement files in
`results` preserve the captured values without redistributing the source art.
Use a new output directory for each run. The tool refuses an existing result file.

## Method

- Encode opaque RGB content at qualities 50, 75, and 90. Flatten alpha over white
  before this test. This test does not measure alpha support.
- Use method 4 for cwebp. Use the public defaults for both CGo wrappers.
- Test nativewebp once at its default lossless effort. Its quality label of 100
  identifies lossless output. It is not a lossy quality setting.
- Decode every output with the same dwebp binary. Compute RGB PSNR and luma
  PSNR in decoded RGB space. Luma is `0.299 R + 0.587 G + 0.114 B`.
- Compute luma SSIM with an 11 by 11 Gaussian window, sigma 1.5, population
  covariance, and range 255. This differs from the root oracle's 8 by 8 SSIM.
- Warm each Go encoder once. Report the median of five timed calls, or three
  for methods 5 and 6. Exclude source decoding and output file writes. Use one
  Go execution thread. Reuse the output buffer and check deterministic bytes.
- Report cwebp wall time over five calls after one warmup. It includes process
  startup, PPM input, and output writes. It is not an isolated C encode time.
- Measure Go allocations in a separate call with the warmed output buffer.
  These values exclude C allocations. Do not compare them as total memory.

The rate calculation integrates piecewise linear log bytes over the common
SSIM-dB interval, where `SSIM-dB = -10 log10(1 - SSIM)`. It uses three points
per encoder and never extrapolates. It refuses curves that do not increase in
both size and SSIM. The q75 ratio interpolates the candidate at cwebp q75 SSIM,
only when that point lies inside the candidate range. This is a BD-rate-style
estimate. It is not a standard four-point polynomial BD-rate result.

## Capture from 2026-09-23

See [the results](results/2026-09-23.md). Raw files include all image-level
measurements. Regenerate their summaries with:

```sh
.venv/bin/python summarize.py results/metrics-*.jsonl
```

The corpus has six Kodak photos, three graphics or cutouts, and three Chrome
screenshots. It is a small diagnostic corpus. It cannot establish universal
quality or speed claims. The rose cutout has photographic texture. Broader
release gates need more portraits, line art, small text, and alpha masks.
