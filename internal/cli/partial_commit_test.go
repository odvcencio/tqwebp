package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"image/png"
	webp "m31labs.dev/tqwebp"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExtractionCleanupFailureDisclosesCommittedOutput(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "frames")
	ops := systemOutputOps()
	sentinel := errors.New("injected cleanup failure")
	ops.remove = func(name string) error {
		if strings.HasPrefix(filepath.Base(name), ".tqwebp-") {
			return sentinel
		}
		return os.Remove(name)
	}
	var out, stderr bytes.Buffer
	code := run(context.Background(), []string{"decode", "-", "--out-dir", dir, "--json"}, bytes.NewReader(makeAnimation(t, webp.Metadata{})), &out, &stderr, false, ops)
	var result Result
	if e := json.Unmarshal(stderr.Bytes(), &result); e != nil {
		t.Fatal(e)
	}
	if code != 5 || result.OK || result.OutputBytes != nil || !strings.Contains(result.Error, "output may already be committed") {
		t.Fatal(code, result)
	}
	// A failed unlink after successful no-clobber link can leave complete output.
	// The result is explicitly uncertain rather than a false all-or-none promise.
	data, e := os.ReadFile(filepath.Join(dir, "frame-000000.png"))
	if e != nil {
		t.Fatal(e)
	}
	if _, e = png.Decode(bytes.NewReader(data)); e != nil {
		t.Fatal(e)
	}
	if _, e = os.Stat(filepath.Join(dir, "manifest.json")); !os.IsNotExist(e) {
		t.Fatal("incomplete extraction published manifest")
	}
}
func TestCancellationCleanupWarningKeepsPrimaryCategory(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	temp := &fakeTemp{name: "owned"}
	ops := outputOps{
		create: func(string, string) (tempOutput, error) { cancel(); return temp, nil },
		remove: func(string) error { return errors.New("cleanup failed") },
		link:   func(string, string) error { t.Fatal("committed after cancellation"); return nil },
	}
	e := atomicFile(ctx, "target", []byte("data"), false, ops)
	code, category := classify(e)
	if code != 130 || category != "interrupt" || !errors.Is(e, context.Canceled) || !strings.Contains(e.Error(), "cleanup") {
		t.Fatal(code, category, e)
	}
}
