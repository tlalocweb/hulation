package handler

import (
	"strings"
	"testing"
)

func TestApplyUnifiedDiff_Basic(t *testing.T) {
	original := "line1\nline2\nline3\nline4\nline5\n"
	// git diff style: replace line3 with two new lines
	diff := `diff --git a/file.txt b/file.txt
index abc..def 100644
--- a/file.txt
+++ b/file.txt
@@ -1,5 +1,6 @@
 line1
 line2
-line3
+line3-modified
+line3-extra
 line4
 line5
`
	got, err := applyUnifiedDiff([]byte(original), []byte(diff))
	if err != nil {
		t.Fatalf("apply failed: %s", err)
	}
	want := "line1\nline2\nline3-modified\nline3-extra\nline4\nline5\n"
	if string(got) != want {
		t.Errorf("got:\n%q\nwant:\n%q", string(got), want)
	}
}

func TestApplyUnifiedDiff_Insert(t *testing.T) {
	original := "a\nb\nc\n"
	diff := `--- a/f
+++ b/f
@@ -2,1 +2,3 @@
 b
+inserted1
+inserted2
`
	got, err := applyUnifiedDiff([]byte(original), []byte(diff))
	if err != nil {
		t.Fatalf("apply failed: %s", err)
	}
	want := "a\nb\ninserted1\ninserted2\nc\n"
	if string(got) != want {
		t.Errorf("got:\n%q\nwant:\n%q", string(got), want)
	}
}

func TestApplyUnifiedDiff_Delete(t *testing.T) {
	original := "keep1\nremove\nkeep2\n"
	diff := `--- a/f
+++ b/f
@@ -1,3 +1,2 @@
 keep1
-remove
 keep2
`
	got, err := applyUnifiedDiff([]byte(original), []byte(diff))
	if err != nil {
		t.Fatalf("apply failed: %s", err)
	}
	want := "keep1\nkeep2\n"
	if string(got) != want {
		t.Errorf("got:\n%q\nwant:\n%q", string(got), want)
	}
}

func TestApplyUnifiedDiff_MultipleHunks(t *testing.T) {
	original := "a\nb\nc\nd\ne\nf\ng\nh\ni\nj\n"
	diff := `--- a/f
+++ b/f
@@ -1,3 +1,3 @@
 a
-b
+B
 c
@@ -7,3 +7,3 @@
 g
-h
+H
 i
`
	got, err := applyUnifiedDiff([]byte(original), []byte(diff))
	if err != nil {
		t.Fatalf("apply failed: %s", err)
	}
	want := "a\nB\nc\nd\ne\nf\ng\nH\ni\nj\n"
	if string(got) != want {
		t.Errorf("got:\n%q\nwant:\n%q", string(got), want)
	}
}

func TestApplyUnifiedDiff_ContextMismatch(t *testing.T) {
	original := "line1\nline2\nline3\n"
	diff := `--- a/f
+++ b/f
@@ -1,3 +1,3 @@
 line1
-WRONG
+replaced
 line3
`
	_, err := applyUnifiedDiff([]byte(original), []byte(diff))
	if err == nil {
		t.Fatalf("expected error for context mismatch, got none")
	}
	if !strings.Contains(err.Error(), "mismatch") {
		t.Errorf("expected 'mismatch' in error, got: %s", err)
	}
}

func TestApplyUnifiedDiff_NoTrailingNewline(t *testing.T) {
	original := "a\nb\nc" // no trailing newline
	diff := `--- a/f
+++ b/f
@@ -1,3 +1,3 @@
 a
-b
+B
 c
`
	got, err := applyUnifiedDiff([]byte(original), []byte(diff))
	if err != nil {
		t.Fatalf("apply failed: %s", err)
	}
	want := "a\nB\nc"
	if string(got) != want {
		t.Errorf("got:\n%q\nwant:\n%q", string(got), want)
	}
}

func TestParseHunkHeader(t *testing.T) {
	cases := []struct {
		in           string
		oldS, newS   int
	}{
		{"@@ -1,3 +1,4 @@", 1, 1},
		{"@@ -10,5 +12,3 @@ func foo()", 10, 12},
		{"@@ -1 +1 @@", 1, 1},
		{"@@ -0,0 +1,5 @@", 1, 1}, // 0 normalized to 1
	}
	for _, c := range cases {
		oldS, newS, err := parseHunkHeader(c.in)
		if err != nil {
			t.Errorf("%q: %s", c.in, err)
			continue
		}
		if oldS != c.oldS || newS != c.newS {
			t.Errorf("%q: got (%d, %d) want (%d, %d)", c.in, oldS, newS, c.oldS, c.newS)
		}
	}
}
