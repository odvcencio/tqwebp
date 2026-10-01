# Opaque encoder foundation contract

This contract covers the existing encoder plus the P1/P2 foundation. The
reference source is `efac282eaa5a1248847a89381033398bca1a2929`. It does not claim
compressed-alpha or lossless-color encoding. [Transparent still and animation encoding](encoding-alpha-animation.md) is now available. The public decoder supports
VP8/VP8L and raw/compressed ALPH stills plus [composed animation](animation.md).
The experimental [container API](container.md) transports still/animation metadata.

## Existing callers remain compatible

- Module `m31labs.dev/tqwebp`, package `webp`, minimum Go 1.26
- `Encode(io.Writer, image.Image, *Options) error`
- `EncodeWithLimits(io.Writer, image.Image, *Options, Limits) error`
- `Options` has exactly `Quality int`, then `Method int`
- `Limits` has exactly `MaxWidth int`, `MaxHeight int`, `MaxPixels int64`,
  then `MaxOutputBytes int64`
- Nil/zero options mean quality 75 and method 4; zero limits add no caller cap
- Methods 1–4 retain the same path; methods 5/6 remain experimental
- Opaque lossy stills only; quality 100 remains lossy 4:2:0
- Transparent inputs now preserve raw 8-bit alpha; this is an explicit supported-input expansion, with no implicit flattening
- Nonzero bounds and valid standard/custom `image.Image` values are supported
- Writers receive identical bytes for the same supported input/options; output
  caps include framing/padding and refusals write nothing
- Writer errors retain identity; short writes return `io.ErrShortWrite`

The external consumer tests deliberately compile both keyed and positional
literals. The golden manifest records 27 outputs generated from the untouched
reference tree before implementation: nine deterministic corpus images at
quality 75, across methods 4/5/6. It is not a new benchmark or an acceptance
substitute for independent interoperability tests.

## New cancellable path

`EncodeContext(context.Context, io.Writer, image.Image, *Options, Limits) error`
uses the same validated codec operation. The old entry points pass
`context.Background()` and impose no new default limits.

A nil or typed-nil context returns `ErrInvalidContext`. A pre-cancelled context
returns its `context.Canceled` or `context.DeadlineExceeded` unchanged, before
image/writer methods. Otherwise retain the original validation order:

1. Options
2. Negative limit fields
3. Nil/typed-nil writer
4. Nil/typed-nil image and empty/inverted bounds
5. Caller width, height, then visible pixel count
6. VP8's 16383-pixel axis limit
7. Opacity
8. Encoding and complete-output budget

Cancellation checkpoints cover opacity/conversion traversals, macroblocks,
bounded mode/coefficient search phases, probability analysis and its second
method 6 pass, serialization, and external write boundaries. No goroutine is
created to race a callback. Arbitrary `Bounds`, `Opaque`, `At`, or `Write`
methods may block; cancellation is observed only once they return. A writer
failure takes precedence over cancellation during that same write. Partial
external output is possible after the first write starts.

## Structured refusals

`LimitError` has `Resource string`, `Limit int64`, and `Actual int64`. Resources
in this tranche are `width`, `height`, `pixels` and `output_bytes`. `Actual` is
the computed amount or `-1` when safely unavailable. A huge dimension/product
that does not fit int64 and an early output-serialization refusal use `-1`.
Callers use `errors.As` for fields and `errors.Is` for categories. Every
`LimitError` matches `ErrLimitExceeded`; `output_bytes` also matches
`ErrOutputTooLarge`. Existing errors remain declared. Diagnostic text is not a
parsing contract, and a format dimension error remains distinct from policy.

## Bounds do not promise process-memory or hard deadlines

Positive output caps give the first/token partitions a shared complete-file
budget, including the VP8 prefix, RIFF framing and padding. Their backing
capacities cannot grow beyond that budget; oversized serialization stops before
a second complete-file buffer is assembled. Accepted capped files use an
exact-size commit buffer. This is buffered encoding, not streaming.

Input/reconstruction planes, macroblocks, method 6 analysis scratch, runtime
overhead and caller storage are not capped by `MaxOutputBytes`. It is neither
a peak RSS limit nor a CPU deadline. Pixel admission and bounded concurrency
remain deployment responsibilities. Cooperative context cancellation is not a
hard timeout for blocked callbacks.

## Required remaining toolkit work

The completed usability product still requires CLI workflows, broad holdout/browser qualification
and full release qualification. Root codecs remain pinned native audited
adaptations; runtime foreign decoders and the oracle stay outside its import graph.

No tagged release, publication or browser/runtime qualification is implied by
these foundation tests.
