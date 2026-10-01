package cli

import (
	"bytes"
	"context"
	"errors"
	webp "m31labs.dev/tqwebp"
	"math"
	"strconv"
	"testing"
)

func TestPublicCLIMetadataMaxInt64NoOverflow(t *testing.T) {
	o := testOptions(t)
	input, e := encodePNG(context.Background(), tinyImage(), webp.Metadata{ICC: rgbProfile()}, o)
	if e != nil {
		t.Fatal(e)
	}
	code, result, out := runJSON(t, []string{"-", "-o", "-", "--method", "1", "--max-metadata-bytes", strconv.FormatInt(math.MaxInt64, 10)}, input)
	if code != 0 || !result.OK || !webpData(out) {
		t.Fatal(code, result)
	}
}
func TestBoundedReadZeroExactAndMaximumLimits(t *testing.T) {
	for _, tc := range []struct {
		data  string
		limit int64
		pass  bool
	}{{"", 0, true}, {"x", 0, false}, {"abc", 3, true}, {"abc", 2, false}, {"abc", math.MaxInt64, true}} {
		out, e := readBounded(context.Background(), bytes.NewBufferString(tc.data), tc.limit)
		if tc.pass {
			if e != nil || string(out) != tc.data {
				t.Fatal(tc, e, string(out))
			}
		} else if !errors.Is(e, webp.ErrLimitExceeded) || out != nil {
			t.Fatal(tc, e)
		}
	}
}
