package oracle

import (
	"strings"
	"testing"
)

func TestTable_StringIsStableRegardlessOfInsertOrder(t *testing.T) {
	t1 := &Table{}
	t1.Add(Row{Image: "b", Class: "photo", Codec: "jpeg", Quality: "75", Bytes: 100, YPSNR: 30, SSIM: 0.9})
	t1.Add(Row{Image: "a", Class: "photo", Codec: "jpeg", Quality: "75", Bytes: 200, YPSNR: 32, SSIM: 0.95})

	t2 := &Table{}
	t2.Add(Row{Image: "a", Class: "photo", Codec: "jpeg", Quality: "75", Bytes: 200, YPSNR: 32, SSIM: 0.95})
	t2.Add(Row{Image: "b", Class: "photo", Codec: "jpeg", Quality: "75", Bytes: 100, YPSNR: 30, SSIM: 0.9})

	s1, s2 := t1.String(), t2.String()
	if s1 != s2 {
		t.Fatalf("Table.String() depends on insertion order:\n--- t1 ---\n%s\n--- t2 ---\n%s", s1, s2)
	}
	lines := strings.Split(strings.TrimRight(s1, "\n"), "\n")
	if len(lines) != 3 { // header + 2 rows
		t.Fatalf("Table.String() line count = %d, want 3:\n%s", len(lines), s1)
	}
	if !strings.Contains(lines[1], "a") || !strings.Contains(lines[2], "b") {
		t.Fatalf("Table.String() not sorted by image name:\n%s", s1)
	}
}

func TestTable_StringFormatsInf(t *testing.T) {
	tab := &Table{}
	tab.Add(Row{Image: "x", Class: "flat", Codec: "jpeg", Quality: "100", Bytes: 1, YPSNR: PSNRInf, SSIM: 1})
	out := tab.String()
	if !strings.Contains(out, "inf") {
		t.Fatalf("Table.String() did not render PSNRInf as \"inf\":\n%s", out)
	}
}
