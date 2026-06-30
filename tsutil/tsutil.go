/*
Copyright © 2020 Anton Kramarev
Copyright © 2024 Perfana Software B.V.

Permission is hereby granted, free of charge, to any person obtaining a copy
of this software and associated documentation files (the "Software"), to deal
in the Software without restriction, including without limitation the rights
to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
copies of the Software, and to permit persons to whom the Software is
furnished to do so, subject to the following conditions:

The above copyright notice and this permission notice shall be included in
all copies or substantial portions of the Software.

THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN
THE SOFTWARE.
*/

// Package tsutil builds deterministic, re-loadable sub-resolution timestamps for
// the log parsers. Test tools write timestamps with low resolution (milliseconds
// for Gatling/JMeter, seconds for k6), so many log lines can share the same base
// time. To stop InfluxDB points from overwriting each other, a per-base-time line
// counter and a file index are combined into a unique nanosecond offset.
package tsutil

import (
	"fmt"
	"path/filepath"
	"sort"
	"time"
)

const (
	// oneMillisecond is the size of the offset range in nanoseconds.
	oneMillisecond = 1_000_000
	// MaxGenerators is the assumed maximum number of load generators (files).
	MaxGenerators = 100
	// OffsetWindow is the per-file slice of the one-millisecond range, in
	// nanoseconds. Each file index owns a disjoint window so timestamps of
	// different files never collide.
	OffsetWindow = oneMillisecond / MaxGenerators

	// ModeRandom keeps the legacy behaviour (random offset). It is the default.
	ModeRandom = "random"
	// ModeLine enables the deterministic, re-loadable behaviour.
	ModeLine = "line"
)

// OffsetCounter assigns a deterministic sub-resolution offset to every log line.
//
// It is owned by the parsing goroutine only and must not be shared across
// goroutines. The line counter resets whenever the base time changes, so the
// offset stays small while remaining unique for all lines that share one base
// time, regardless of their measurement or tags.
type OffsetCounter struct {
	fileIndex  int64
	prevBaseNs int64
	lineIndex  int64
	hasPrev    bool
}

// NewOffsetCounter returns a counter for the given file (generator) index.
func NewOffsetCounter(fileIndex uint) *OffsetCounter {
	return &OffsetCounter{fileIndex: int64(fileIndex)}
}

// Timestamp returns the deterministic timestamp for the next line that carries
// the given base time (in nanoseconds).
//
// The returned overflow flag is true exactly once per base time, at the moment
// the per-base-time line counter first exceeds the file window. When this
// happens the offset is clamped to stay inside the file window (accepting a rare
// collision rather than crossing into another file's window).
func (c *OffsetCounter) Timestamp(baseTimeNs int64) (time.Time, bool) {
	if !c.hasPrev || baseTimeNs != c.prevBaseNs {
		c.prevBaseNs = baseTimeNs
		c.lineIndex = 0
		c.hasPrev = true
	} else {
		c.lineIndex++
	}

	overflow := c.lineIndex == OffsetWindow

	li := c.lineIndex
	if li >= OffsetWindow {
		li = OffsetWindow - 1
	}

	offset := c.fileIndex*OffsetWindow + li

	return time.Unix(0, baseTimeNs+offset), overflow
}

// ValidateMode checks that the configured timestamp mode is supported.
func ValidateMode(mode string) error {
	if mode != ModeRandom && mode != ModeLine {
		return fmt.Errorf("unknown timestamp-mode %q, expected %q or %q", mode, ModeRandom, ModeLine)
	}
	return nil
}

// ComputeFileIndex derives the file index automatically. It lists the entries in
// dir whose names match the glob mask (e.g. "*.csv" for result files or
// "*-[0-9]*" for result directories), sorts the matching full paths and returns
// the position of target (a full path) within that sorted list, taken modulo
// MaxGenerators. This keeps timestamps of different files/runs apart without a
// manual --file-index value. It returns 0 when nothing matches or target is not
// found, so callers always get a safe default.
func ComputeFileIndex(dir, mask, target string) uint {
	matches, err := filepath.Glob(filepath.Join(dir, mask))
	if err != nil || len(matches) == 0 {
		return 0
	}
	sort.Strings(matches)
	for i, m := range matches {
		if m == target {
			return uint(i) % MaxGenerators
		}
	}
	return 0
}
