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

package k6parser

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/perfana/x2i/influx"
	l "github.com/perfana/x2i/logger"
	"github.com/perfana/x2i/tsutil"
	"github.com/spf13/cobra"
)

const (
	oneMillisecond = 1_000_000
)

var (
	nodeName            string
	errStoppedByUser    = errors.New("process stopped by user")
	errFatal            = errors.New("fatal error")
	logDir              string
	systemUnderTest     string
	testEnvironment     string
	waitTime            uint
	timestampMode       string
	fileIndex           uint
	offsetCounter       *tsutil.OffsetCounter
	uploadExistingFiles bool
	resultsLogFileName  string
	http_req_duration   = regexp.MustCompile(`^http_req_duration.*`)
	grpc_req_duration   = regexp.MustCompile(`^grpc_req_duration.*`)
	group_duration      = regexp.MustCompile(`^group_duration.*`)
	parserStopped       = make(chan struct{})
)

func lookupTargetDir(ctx context.Context, dir string) error {
	const loopTimeout = 5 * time.Second

	l.Infoln("Looking for target directory...")
	for {
		// This block checks if stop signal is received from user
		// and stops further lookup
		select {
		case <-ctx.Done():
			return errStoppedByUser
		default:
		}

		fInfo, err := os.Stat(dir)
		if err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("target path %s exists but there is an error: %w", dir, err)
		}
		if os.IsNotExist(err) {
			time.Sleep(loopTimeout)
			continue
		}

		if !fInfo.IsDir() {
			return fmt.Errorf("was expecting directory at %s, but found a file", dir)
		}

		abs, _ := filepath.Abs(dir)
		l.Infof("Target directory found at %s", abs)
		logDir = abs
		break
	}

	return nil
}

func waitForLog(ctx context.Context) error {

	const loopTimeout = 5 * time.Second
	const resultFilePattern = "*.csv"

	l.Infoln("Searching for " + logDir + "/" + resultFilePattern + " files...")
	for {
		// This block checks if stop signal is received from user
		// and stops further lookup
		select {
		case <-ctx.Done():
			return errStoppedByUser
		default:
		}

		files, err := filepath.Glob(logDir + "/" + resultFilePattern)
		if err != nil {
			fmt.Println("Error:", err)
			return err
		}
		if len(files) == 0 {
			fmt.Printf("No results file found in dir %s matching pattern %s\n", logDir, resultFilePattern)
			time.Sleep(loopTimeout)
			continue
		}
		resultsLogFileName = filepath.Base(files[0])

		fInfo, err := os.Stat(logDir + "/" + resultsLogFileName)
		if err != nil && !os.IsNotExist(err) {
			return err
		}
		if os.IsNotExist(err) {
			time.Sleep(loopTimeout)
			continue
		}

		// WARNING: second part of this check may fail on Windows. Not tested
		if fInfo.Mode().IsRegular() && (uploadExistingFiles || runtime.GOOS == "windows" || fInfo.Mode().Perm() == 420) {
			abs, _ := filepath.Abs(logDir + "/" + resultsLogFileName)
			l.Infof("Found %s\n", abs)
			break
		}

		return errors.New("something wrong happened when attempting to open " + resultsLogFileName)
	}

	return nil
}

func timeFromUnixBytes(ub []byte) (time.Time, error) {
	timeStamp, err := strconv.ParseInt(string(ub), 10, 64)
	if err != nil {
		return time.Time{}, fmt.Errorf("failed to parse timestamp as integer: %w", err)
	}
	baseNs := (timeStamp * 1000) * oneMillisecond
	if timestampMode == tsutil.ModeLine {
		ts, overflow := offsetCounter.Timestamp(baseNs)
		if overflow {
			l.Errorf("More than %d lines share base time %d for file index %d; sub-millisecond offsets exhausted, points may be overwritten", tsutil.OffsetWindow, timeStamp, fileIndex)
		}
		return ts, nil
	}
	// A workaround that adds a random amount of nanoseconds to the timestamp
	// so db entries will (should) not be overwritten
	return time.Unix(0, baseNs+rand.Int63n(oneMillisecond)), nil
}

func http_req_duration_LineProcess(lb []byte) error {

	split := bytes.Split(lb, []byte(","))
	if len(split) != 19 {
		return errors.New("line contains unexpected amount of values")
	}

	timestamp, err := timeFromUnixBytes(split[1])
	if err != nil {
		return err
	}

	duration, err := strconv.ParseFloat(string(split[2]), 64)
	if err != nil {
		fmt.Println("Error:", err)
		return err
	}

	requestPoint, err := influx.NewPoint(
		"http_req_duration",
		map[string]string{
			"name":              strings.TrimSpace(strings.ReplaceAll(string(split[9]), " ", "_")),
			"group":             strings.TrimSpace(strings.ReplaceAll(string(split[7]), " ", "_")),
			"method":            string(split[8]),
			"expected_response": string(split[6]),
			"systemUnderTest":   systemUnderTest,
			"testEnvironment":   testEnvironment,
			"nodeName":          nodeName,
			"status":            string(split[13]),
			"service":           string(split[12]),
			"url":               string(split[16]),
			"scenario":          string(split[11]),
			"error_code":        string(split[5]),
			"error":             string(bytes.TrimSpace(split[4])),
		},
		map[string]interface{}{
			"duration": duration,
		},
		timestamp,
	)
	if err != nil {
		return fmt.Errorf("error creating new point with request data: %w", err)
	}

	influx.SendPoint(requestPoint)

	return nil
}
func grpc_req_duration_LineProcess(lb []byte) error {

	split := bytes.Split(lb, []byte(","))
	if len(split) != 19 {
		return errors.New("line contains unexpected amount of values")
	}

	timestamp, err := timeFromUnixBytes(split[1])
	if err != nil {
		return err
	}

	duration, err := strconv.ParseFloat(string(split[2]), 64)
	if err != nil {
		fmt.Println("Error:", err)
		return err
	}

	requestPoint, err := influx.NewPoint(
		"grpc_req_duration",
		map[string]string{
			"name":              strings.TrimSpace(strings.ReplaceAll(string(split[9]), " ", "_")),
			"group":             strings.TrimSpace(strings.ReplaceAll(string(split[7]), " ", "_")),
			"method":            string(split[8]),
			"expected_response": string(split[6]),
			"systemUnderTest":   systemUnderTest,
			"testEnvironment":   testEnvironment,
			"nodeName":          nodeName,
			"status":            string(split[13]),
			"service":           string(split[12]),
			"url":               string(split[16]),
			"scenario":          string(split[11]),
			"error_code":        string(split[5]),
			"error":             string(bytes.TrimSpace(split[4])),
		},
		map[string]interface{}{
			"duration": duration,
		},
		timestamp,
	)
	if err != nil {
		return fmt.Errorf("error creating new point with request data: %w", err)
	}

	influx.SendPoint(requestPoint)

	return nil
}

func group_duration_LineProcess(lb []byte) error {

	split := bytes.Split(lb, []byte(","))
	if len(split) != 19 {
		return errors.New("line contains unexpected amount of values")
	}

	timestamp, err := timeFromUnixBytes(split[1])
	if err != nil {
		return err
	}

	duration, err := strconv.ParseFloat(string(split[2]), 64)
	if err != nil {
		fmt.Println("Error:", err)
		return err
	}

	requestPoint, err := influx.NewPoint(
		"group_duration",
		map[string]string{
			"group":    strings.TrimSpace(strings.ReplaceAll(string(split[7]), " ", "_")),
			"scenario": string(split[11]),
		},
		map[string]interface{}{
			"duration": duration,
		},
		timestamp,
	)
	if err != nil {
		return fmt.Errorf("error creating new point with request data: %w", err)
	}

	influx.SendPoint(requestPoint)

	return nil
}

func stringProcessor(lineBuffer []byte) error {

	switch {
	case http_req_duration.Match(lineBuffer):
		return http_req_duration_LineProcess(lineBuffer)
	case grpc_req_duration.Match(lineBuffer):
		return grpc_req_duration_LineProcess(lineBuffer)
	case group_duration.Match(lineBuffer):
		return group_duration_LineProcess(lineBuffer)
	default:
		return nil
	}
}

func fileProcessor(ctx context.Context, file *os.File) {
	r := bufio.NewReader(file)
	buf := new(bytes.Buffer)
	startWait := time.Now()

ParseLoop:
	for {
		// This block checks if stop signal is received from user
		// and stops further processing
		select {
		case <-ctx.Done():
			l.Infoln("Parser received closing signal. Processing stopped")
			break ParseLoop
		default:
		}

		b, err := r.ReadBytes('\n')
		if err == io.EOF {
			// If no new lines read for more than value provided by 'stop-timeout' key then processing is stopped
			if time.Now().After(startWait.Add(time.Duration(waitTime) * time.Second)) {
				l.Infof("No new lines found for %d seconds. Stopping application...", waitTime)
				break ParseLoop
			}
			// All new data is stored in buffer until next loop
			buf.Write(b)
			time.Sleep(time.Second)
			continue
		}
		if err != nil {
			l.Errorf("Unexpected error encountered while parsing file: %v", err)
		}

		buf.Write(b)
		err = stringProcessor(buf.Bytes())
		if err != nil {
			l.Errorf("String processing failed: %v", err)
			if errors.Is(err, errFatal) {
				l.Errorln("Log parser caught an error that can't be handled. Stopping application...")
				break ParseLoop
			}
		}
		// Clean buffer after processing preparing for a new loop
		buf.Reset()
		// Reset a timeout timer
		startWait = time.Now()
	}
	parserStopped <- struct{}{}
}

func parseStart(ctx context.Context, wg *sync.WaitGroup) {
	defer wg.Done()

	l.Infoln("Starting log file parser...")
	file, err := os.Open(logDir + "/" + resultsLogFileName)
	if err != nil {
		l.Errorf("Failed to read %s file: %v\n", resultsLogFileName, err)
	}
	defer file.Close()

	fileProcessor(ctx, file)
}

// setupTimestampMode validates the timestamp related flags and, for the
// deterministic 'line' mode, initializes the per-file line offset counter.
func setupTimestampMode() error {
	switch timestampMode {
	case tsutil.ModeRandom:
		return nil
	case tsutil.ModeLine:
		if fileIndex >= tsutil.MaxGenerators {
			return fmt.Errorf("file-index %d is out of range, must be < %d", fileIndex, tsutil.MaxGenerators)
		}
		offsetCounter = tsutil.NewOffsetCounter(fileIndex)
		l.Infof("Using deterministic 'line' timestamp mode with file index %d", fileIndex)
		return nil
	default:
		return fmt.Errorf("unknown timestamp-mode %q, expected %q or %q", timestampMode, tsutil.ModeRandom, tsutil.ModeLine)
	}
}

// RunMain performs main application logic
func RunMain(cmd *cobra.Command, dir string) {
	systemUnderTest, _ = cmd.Flags().GetString("system-under-test")
	testEnvironment, _ = cmd.Flags().GetString("test-environment")
	waitTime, _ = cmd.Flags().GetUint("stop-timeout")
	timestampMode, _ = cmd.Flags().GetString("timestamp-mode")
	uploadExistingFiles, _ = cmd.Flags().GetBool("upload-existing-files")
	if err := tsutil.ValidateMode(timestampMode); err != nil {
		l.Errorln(err)
		os.Exit(1)
	}
	rand.Seed(time.Now().UnixNano())
	nodeName, _ = os.Hostname()

	l.Infof("Searching for directory at %s", dir)
	abs, err := filepath.Abs(dir)
	if err != nil {
		l.Errorf("Failed to construct an absolute path for %s: %v", dir, err)
	}

	if err := lookupTargetDir(cmd.Context(), abs); err != nil {
		if err == errStoppedByUser {
			return
		}
		l.Errorf("Target directory lookup failed with error: %v\n", err)
		os.Exit(1)
	}

	if err := waitForLog(cmd.Context()); err != nil {
		if err == errStoppedByUser {
			return
		}
		l.Errorf("Failed waiting for %s with error: %v\n", resultsLogFileName, err)
		os.Exit(1)
	}

	// Derive the file index automatically from the directory contents so that
	// timestamps of different files stay apart in the deterministic 'line' mode.
	fileIndex = tsutil.ComputeFileIndex(logDir, "*.csv", filepath.Join(logDir, resultsLogFileName))
	if err := setupTimestampMode(); err != nil {
		l.Errorln(err)
		os.Exit(1)
	}

	wg := &sync.WaitGroup{}
	pCtx, pCancel := context.WithCancel(context.Background())
	iCtx, iCancel := context.WithCancel(context.Background())

	wg.Add(2)
	go parseStart(pCtx, wg)
	go influx.StartProcessing(iCtx, wg)

FinisherLoop:
	for {
		select {
		// If top level context is cancelled we first stop the parser
		case <-cmd.Context().Done():
			pCancel()
		// Then wait for parser to stop and stop client processing
		case <-parserStopped:
			iCancel()
			// In case parser finished processing on its own, we cancel its context
			pCancel()
			break FinisherLoop
		}
	}
	wg.Wait()
}
