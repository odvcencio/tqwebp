// Command go generate, run from bench/deepteams, regenerates
// testdata/golden/deepteams_baseline.txt from the tqwebp corpus and the
// current dtbaseline measurement code.
//
//go:generate go run ./cmd/dtbench -update

// Package deepteams holds the WP-0 black-box external baseline: measuring
// github.com/deepteams/webp, a third-party pure-Go WebP encoder, across
// the tqwebp corpus. It is a separate Go module so github.com/deepteams/webp
// never appears in the root tqwebp module's dependency graph. This
// package itself is a measurement rig: it is not, and must never become,
// a dependency of the tqwebp encoder.
package deepteams
