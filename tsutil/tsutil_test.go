package tsutil

import (
	"os"
	"path/filepath"
	"testing"
)

const baseMs = int64(1_700_000_000_000) // arbitrary base time in ms
func baseNs(ms int64) int64             { return ms * oneMillisecond }

// First line of a base time gets offset = fileIndex*window, and the offset stays
// inside the single millisecond range.
func TestOffsetWithinRangeAndWindowStart(t *testing.T) {
	for _, fi := range []uint{0, 1, 50, 99} {
		c := NewOffsetCounter(fi)
		ts, overflow := c.Timestamp(baseNs(baseMs))
		if overflow {
			t.Fatalf("fileIndex %d: unexpected overflow on first line", fi)
		}
		got := ts.UnixNano() - baseNs(baseMs)
		want := int64(fi) * OffsetWindow
		if got != want {
			t.Fatalf("fileIndex %d: offset = %d, want %d", fi, got, want)
		}
		if got < 0 || got >= oneMillisecond {
			t.Fatalf("fileIndex %d: offset %d is outside [0, %d)", fi, got, oneMillisecond)
		}
	}
}

// Different file indexes use disjoint windows, so the same base time never
// produces the same timestamp for two different files.
func TestDisjointWindows(t *testing.T) {
	c0 := NewOffsetCounter(0)
	c1 := NewOffsetCounter(1)

	seen := make(map[int64]bool)
	for i := 0; i < OffsetWindow; i++ {
		ts0, _ := c0.Timestamp(baseNs(baseMs))
		seen[ts0.UnixNano()] = true
	}
	for i := 0; i < OffsetWindow; i++ {
		ts1, _ := c1.Timestamp(baseNs(baseMs))
		if seen[ts1.UnixNano()] {
			t.Fatalf("file 1 produced timestamp %d already used by file 0", ts1.UnixNano())
		}
	}
}

// Lines sharing one base time receive strictly increasing offsets even if their
// (simulated) tags/measurements interleave - the counter ignores them.
func TestInterleavingSameBaseTime(t *testing.T) {
	c := NewOffsetCounter(0)
	var prev int64 = -1
	for i := 0; i < 5; i++ {
		ts, _ := c.Timestamp(baseNs(baseMs))
		got := ts.UnixNano() - baseNs(baseMs)
		if got != int64(i) {
			t.Fatalf("line %d: offset = %d, want %d", i, got, i)
		}
		if got <= prev {
			t.Fatalf("line %d: offset %d not strictly increasing (prev %d)", i, got, prev)
		}
		prev = got
	}
}

// The counter resets to 0 when the base time changes.
func TestResetOnBaseTimeChange(t *testing.T) {
	c := NewOffsetCounter(0)

	c.Timestamp(baseNs(baseMs))
	ts2, _ := c.Timestamp(baseNs(baseMs))
	if off := ts2.UnixNano() - baseNs(baseMs); off != 1 {
		t.Fatalf("second line of same base time: offset = %d, want 1", off)
	}

	ts3, _ := c.Timestamp(baseNs(baseMs + 1))
	if off := ts3.UnixNano() - baseNs(baseMs+1); off != 0 {
		t.Fatalf("first line of new base time: offset = %d, want 0", off)
	}
}

// Idempotency: two independent counters fed the same sequence produce identical
// timestamps, so re-loading the same file yields the same data.
func TestDeterministicAcrossRuns(t *testing.T) {
	run := func() []int64 {
		c := NewOffsetCounter(3)
		out := make([]int64, 0, 6)
		seq := []int64{baseMs, baseMs, baseMs, baseMs + 2, baseMs + 2, baseMs + 5}
		for _, ms := range seq {
			ts, _ := c.Timestamp(baseNs(ms))
			out = append(out, ts.UnixNano())
		}
		return out
	}

	first := run()
	second := run()
	for i := range first {
		if first[i] != second[i] {
			t.Fatalf("non-deterministic at %d: %d != %d", i, first[i], second[i])
		}
	}
}

// Overflow is reported exactly once per base time, and the offset is clamped to
// stay inside the file window.
func TestOverflowReportedOnceAndClamped(t *testing.T) {
	c := NewOffsetCounter(0)

	overflowCount := 0
	var lastOffset int64
	for i := 0; i <= OffsetWindow+5; i++ {
		ts, overflow := c.Timestamp(baseNs(baseMs))
		if overflow {
			overflowCount++
		}
		lastOffset = ts.UnixNano() - baseNs(baseMs)
		if lastOffset < 0 || lastOffset >= OffsetWindow {
			t.Fatalf("line %d: clamped offset %d outside file window [0, %d)", i, lastOffset, OffsetWindow)
		}
	}
	if overflowCount != 1 {
		t.Fatalf("overflow reported %d times, want exactly 1", overflowCount)
	}

	// A new base time clears the overflow state.
	_, overflow := c.Timestamp(baseNs(baseMs + 1))
	if overflow {
		t.Fatalf("unexpected overflow on first line of a new base time")
	}
}

// ValidateMode accepts the supported modes and rejects anything else.
func TestValidateMode(t *testing.T) {
	for _, mode := range []string{ModeRandom, ModeLine} {
		if err := ValidateMode(mode); err != nil {
			t.Fatalf("mode %q: unexpected error %v", mode, err)
		}
	}
	if err := ValidateMode("nonsense"); err == nil {
		t.Fatalf("expected an error for an unknown mode")
	}
}

// ComputeFileIndex returns the position of target within the sorted glob matches.
func TestComputeFileIndex(t *testing.T) {
	dir := t.TempDir()
	names := []string{"c.csv", "a.csv", "b.csv", "ignore.txt"}
	for _, n := range names {
		if err := os.WriteFile(filepath.Join(dir, n), []byte("x"), 0o644); err != nil {
			t.Fatalf("failed to create %s: %v", n, err)
		}
	}

	// Sorted *.csv matches are a.csv(0), b.csv(1), c.csv(2); the .txt is excluded.
	if got := ComputeFileIndex(dir, "*.csv", filepath.Join(dir, "b.csv")); got != 1 {
		t.Fatalf("index for b.csv = %d, want 1", got)
	}
	if got := ComputeFileIndex(dir, "*.csv", filepath.Join(dir, "c.csv")); got != 2 {
		t.Fatalf("index for c.csv = %d, want 2", got)
	}
}

// ComputeFileIndex returns the safe default 0 when nothing matches or the target
// is absent.
func TestComputeFileIndexDefaults(t *testing.T) {
	dir := t.TempDir()
	if got := ComputeFileIndex(dir, "*.csv", filepath.Join(dir, "missing.csv")); got != 0 {
		t.Fatalf("empty dir index = %d, want 0", got)
	}
	if err := os.WriteFile(filepath.Join(dir, "only.csv"), []byte("x"), 0o644); err != nil {
		t.Fatalf("failed to create file: %v", err)
	}
	if got := ComputeFileIndex(dir, "*.csv", filepath.Join(dir, "absent.csv")); got != 0 {
		t.Fatalf("missing target index = %d, want 0", got)
	}
}
