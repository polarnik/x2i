package gatlingparser

import (
	"bufio"
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"

	l "github.com/perfana/x2i/logger"
)

// The fixtures under test/data/<version>/<combo>/simulation.log are produced by the
// gatling-matrix project (see gatling-logs/gatling-matrix). The simulation source
// (BasicSimulation.kt) uses fixed, non-random scenario/group/request names, so we can
// assert on exact names regardless of the Gatling version:
//
//	scenario                     : "Scenario"
//	simulation class             : example.BasicSimulation
//	groups=0 (grp0)              : request "Request", no group
//	groups=1 (grp1)              : group ["Group 1"] -> request "Request"
//	groups=3 (grp3)              : groups ["GET","/mainPage/{ID}","withParams"] -> request "GET/mainPage/{ID}?params"
//
// The combo directory name encodes the parameters: vu<N>_err<bool>_assert<bool>_grp<N>.
// With errors=true every request targets /404 and therefore fails (KO); otherwise it is OK.
//
// Format families:
//	3.12.x  -> legacy text/TSV simulation.log      (parsed by the text code path)
//	3.13.0+ -> binary simulation.log               (parsed by the binary decoders)

const testDataRoot = "../test/data"

const expectedScenario = "Scenario"
const expectedSimulationClass = "example.BasicSimulation"

var comboNamePattern = regexp.MustCompile(`^vu(\d+)_err(true|false)_assert(true|false)_grp(\d+)$`)

type comboParams struct {
	vu         int
	withErrors bool
	assertions bool
	groups     int
}

// parseCombo extracts the scenario parameters from a combo directory name such as
// "vu10_errtrue_asserttrue_grp3".
func parseCombo(t *testing.T, name string) (comboParams, bool) {
	t.Helper()
	m := comboNamePattern.FindStringSubmatch(name)
	if m == nil {
		return comboParams{}, false
	}
	vu, _ := strconv.Atoi(m[1])
	groups, _ := strconv.Atoi(m[4])
	return comboParams{
		vu:         vu,
		withErrors: m[2] == "true",
		assertions: m[3] == "true",
		groups:     groups,
	}, true
}

// expectedShape returns the request name, the hierarchy of the request's enclosing groups
// (top -> leaf) and the set of group-record hierarchies expected for a given groups value.
func expectedShape(groups int) (reqName string, reqGroups []string, groupRecords [][]string) {
	switch groups {
	case 0:
		return "Request", nil, nil
	case 1:
		return "Request", []string{"Group 1"}, [][]string{{"Group 1"}}
	case 3:
		return "GET/mainPage/{ID}?params",
			[]string{"GET", "/mainPage/{ID}", "withParams"},
			[][]string{
				{"GET", "/mainPage/{ID}", "withParams"},
				{"GET", "/mainPage/{ID}"},
				{"GET"},
			}
	default:
		return "", nil, nil
	}
}

func hierarchyEqual(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

func hierarchyKey(h []string) string {
	return strings.Join(h, ",")
}

// discoverVersions lists the version directories available under test/data and classifies
// each one as text (3.12.x) or binary (3.13.0+).
func discoverVersions(t *testing.T) (text []string, binary []string) {
	t.Helper()
	entries, err := os.ReadDir(testDataRoot)
	if err != nil {
		t.Fatalf("failed to read %s: %v", testDataRoot, err)
	}
	versionLike := regexp.MustCompile(`^3\.\d+\.\d+$`)
	for _, e := range entries {
		if !e.IsDir() || !versionLike.MatchString(e.Name()) {
			continue
		}
		if strings.HasPrefix(e.Name(), "3.12.") {
			text = append(text, e.Name())
		} else {
			binary = append(binary, e.Name())
		}
	}
	return text, binary
}

func comboDirs(t *testing.T, versionDir string) []string {
	t.Helper()
	entries, err := os.ReadDir(versionDir)
	if err != nil {
		t.Fatalf("failed to read %s: %v", versionDir, err)
	}
	var combos []string
	for _, e := range entries {
		if e.IsDir() && comboNamePattern.MatchString(e.Name()) {
			combos = append(combos, e.Name())
		}
	}
	return combos
}

// --- binary parsing --------------------------------------------------------

type parsedLog struct {
	run      RunMessage
	requests []RequestRecord
	groups   []GroupRecord
	users    []UserRecord
	errs     []ErrorRecord
}

// parseBinaryLog drives the real binary decoders (ReadHeader + ReadNotHeaderRecord) over a
// fixture and collects every decoded record. It intentionally avoids fileProcessorBinary's
// polling/goroutine machinery so the matrix stays fast and deterministic.
func parseBinaryLog(t *testing.T, path string) parsedLog {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatalf("open(%s): %v", path, err)
	}
	defer file.Close()

	reader := bufio.NewReader(file)
	run, scenarios, err := processLogHeader(reader)
	if err != nil {
		t.Fatalf("processLogHeader(%s): %v", path, err)
	}

	result := parsedLog{run: *run}
	for {
		record, err := ReadNotHeaderRecord(reader, run.Start, scenarios)
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("ReadNotHeaderRecord(%s): %v", path, err)
		}
		switch r := record.(type) {
		case RequestRecord:
			result.requests = append(result.requests, r)
		case GroupRecord:
			result.groups = append(result.groups, r)
		case UserRecord:
			result.users = append(result.users, r)
		case ErrorRecord:
			result.errs = append(result.errs, r)
		}
	}
	return result
}

func assertShape(t *testing.T, label string, p comboParams, log parsedLog) {
	t.Helper()

	if !strings.Contains(log.run.SimulationClassName, "BasicSimulation") {
		t.Errorf("%s: unexpected simulation class %q", label, log.run.SimulationClassName)
	}

	reqName, reqGroups, groupRecords := expectedShape(p.groups)

	// requests: one per virtual user, all with the expected name / group / status
	if len(log.requests) != p.vu {
		t.Errorf("%s: expected %d requests, got %d", label, p.vu, len(log.requests))
	}
	for _, r := range log.requests {
		if r.Name != reqName {
			t.Errorf("%s: request name %q, expected %q", label, r.Name, reqName)
		}
		var got []string
		if r.Group != nil {
			got = r.Group.Hierarchy
		}
		if !hierarchyEqual(got, reqGroups) {
			t.Errorf("%s: request group %v, expected %v", label, got, reqGroups)
		}
		// errors=true -> the /404 request fails (KO, Status=false)
		if r.Status == p.withErrors {
			t.Errorf("%s: request status=%v, but withErrors=%v", label, r.Status, p.withErrors)
		}
	}

	// scenario name on every user event
	for _, u := range log.users {
		if u.Scenario != expectedScenario {
			t.Errorf("%s: user scenario %q, expected %q", label, u.Scenario, expectedScenario)
		}
	}

	// group records: expected set repeated once per virtual user
	wantCount := len(groupRecords) * p.vu
	if len(log.groups) != wantCount {
		t.Errorf("%s: expected %d group records, got %d", label, wantCount, len(log.groups))
	}
	allowed := make(map[string]bool)
	for _, g := range groupRecords {
		allowed[hierarchyKey(g)] = true
	}
	for _, g := range log.groups {
		if !allowed[hierarchyKey(g.Group.Hierarchy)] {
			t.Errorf("%s: unexpected group hierarchy %v", label, g.Group.Hierarchy)
		}
	}
}

// TestBinaryMatrix parses every binary fixture (Gatling 3.13.0+) across all matrix combos
// and asserts the fixed scenario / group / request names and OK/KO status.
func TestBinaryMatrix(t *testing.T) {
	l.InitLogger("testlog.log")

	_, binaryVersions := discoverVersions(t)
	if len(binaryVersions) == 0 {
		t.Fatal("no binary version fixtures found under " + testDataRoot)
	}

	for _, version := range binaryVersions {
		version := version
		t.Run(version, func(t *testing.T) {
			versionDir := filepath.Join(testDataRoot, version)
			combos := comboDirs(t, versionDir)
			if len(combos) == 0 {
				t.Fatalf("no combo fixtures found under %s", versionDir)
			}
			for _, combo := range combos {
				combo := combo
				t.Run(combo, func(t *testing.T) {
					params, ok := parseCombo(t, combo)
					if !ok {
						t.Fatalf("unexpected combo name %q", combo)
					}
					path := filepath.Join(versionDir, combo, simulationLogFileName)
					log := parseBinaryLog(t, path)

					if log.run.GatlingVersion != version {
						t.Errorf("log reports version %q, expected %q", log.run.GatlingVersion, version)
					}
					assertShape(t, version+"/"+combo, params, log)
				})
			}
		})
	}
}

// --- text parsing ----------------------------------------------------------

// textNames mirrors the exact field splitting used by the text code path (see
// requestLineProcess / groupLineProcess / userLineProcess) so that a change in the 3.12.x
// text layout (field counts) or in the fixed names is caught.
func collectTextNames(t *testing.T, path string, p comboParams) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read(%s): %v", path, err)
	}

	reqName, reqGroups, groupRecords := expectedShape(p.groups)
	reqGroupKey := hierarchyKey(reqGroups)
	allowedGroups := make(map[string]bool)
	for _, g := range groupRecords {
		allowedGroups[hierarchyKey(g)] = true
	}

	requests, groups, users, assertions := 0, 0, 0, 0
	sawRun := false

	for _, line := range bytes.Split(data, []byte{'\n'}) {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		fields := bytes.Split(line, tabSep)
		head := string(fields[0])
		switch head {
		case "RUN":
			sawRun = true
			if len(fields) != runLineLen {
				t.Errorf("%s: RUN line has %d fields, parser expects %d", path, len(fields), runLineLen)
			}
			if got := string(fields[1]); got != expectedSimulationClass {
				t.Errorf("%s: RUN class %q, expected %q", path, got, expectedSimulationClass)
			}
		case "USER":
			users++
			if len(fields) != 4 {
				t.Errorf("%s: USER line has %d fields, parser expects 4", path, len(fields))
			}
			if got := string(fields[1]); got != expectedScenario {
				t.Errorf("%s: USER scenario %q, expected %q", path, got, expectedScenario)
			}
		case "REQUEST":
			requests++
			if len(fields) != requestLineLen-1 {
				t.Errorf("%s: REQUEST line has %d fields, parser expects %d", path, len(fields), requestLineLen-1)
			}
			if got := string(fields[2]); got != reqName {
				t.Errorf("%s: REQUEST name %q, expected %q", path, got, reqName)
			}
			if got := string(fields[1]); got != reqGroupKey {
				t.Errorf("%s: REQUEST groups %q, expected %q", path, got, reqGroupKey)
			}
			status := string(fields[5])
			if p.withErrors && status != "KO" {
				t.Errorf("%s: REQUEST status %q, expected KO", path, status)
			}
			if !p.withErrors && status != "OK" {
				t.Errorf("%s: REQUEST status %q, expected OK", path, status)
			}
		case "GROUP":
			groups++
			if len(fields) != groupLineLen-1 {
				t.Errorf("%s: GROUP line has %d fields, parser expects %d", path, len(fields), groupLineLen-1)
			}
			if key := string(fields[1]); !allowedGroups[key] {
				t.Errorf("%s: unexpected GROUP hierarchy %q", path, key)
			}
		case "ASSERTION":
			assertions++
		default:
			t.Errorf("%s: unexpected line head %q", path, head)
		}
	}

	if !sawRun {
		t.Errorf("%s: no RUN line found", path)
	}
	if requests != p.vu {
		t.Errorf("%s: expected %d REQUEST lines, got %d", path, p.vu, requests)
	}
	if got := len(groupRecords) * p.vu; groups != got {
		t.Errorf("%s: expected %d GROUP lines, got %d", path, got, groups)
	}
	if p.assertions && assertions == 0 {
		t.Errorf("%s: expected an ASSERTION line for an assert=true fixture", path)
	}
}

// TestTextMatrix validates every legacy text/TSV fixture (Gatling 3.12.x) across all matrix
// combos: it checks the fixed names and that the field layout still matches what the text
// code path expects.
func TestTextMatrix(t *testing.T) {
	l.InitLogger("testlog.log")

	textVersions, _ := discoverVersions(t)
	if len(textVersions) == 0 {
		t.Fatal("no text version fixtures found under " + testDataRoot)
	}

	for _, version := range textVersions {
		version := version
		t.Run(version, func(t *testing.T) {
			versionDir := filepath.Join(testDataRoot, version)
			combos := comboDirs(t, versionDir)
			if len(combos) == 0 {
				t.Fatalf("no combo fixtures found under %s", versionDir)
			}
			for _, combo := range combos {
				combo := combo
				t.Run(combo, func(t *testing.T) {
					params, ok := parseCombo(t, combo)
					if !ok {
						t.Fatalf("unexpected combo name %q", combo)
					}
					path := filepath.Join(versionDir, combo, simulationLogFileName)
					collectTextNames(t, path, params)
				})
			}
		})
	}
}

// TestFileProcessorBinary exercises the full binary code path end-to-end (header + record
// goroutines + a RecordsWriter) on a single representative fixture.
func TestFileProcessorBinary(t *testing.T) {
	l.InitLogger("testlog.log")
	waitTime = 1
	ctx := context.Background()

	path := filepath.Join(testDataRoot, "3.15.0", "vu10_errtrue_asserttrue_grp3", simulationLogFileName)
	file, err := os.Open(path)
	if err != nil {
		t.Fatalf("os.Open(%s) returned error: %v", path, err)
	}
	defer file.Close()

	writer := &collectingWriter{}
	go fileProcessorBinary(ctx, file, writer)
	<-parserStopped

	if len(writer.requests) != 10 {
		t.Errorf("expected 10 requests, got %d", len(writer.requests))
	}
	for _, r := range writer.requests {
		if r.Name != "GET/mainPage/{ID}?params" {
			t.Errorf("unexpected request name %q", r.Name)
		}
		if r.Status {
			t.Errorf("expected KO request for an errors=true fixture")
		}
	}
	if len(writer.groups) != 30 {
		t.Errorf("expected 30 group records, got %d", len(writer.groups))
	}
}

type collectingWriter struct {
	mu       sync.Mutex
	requests []RequestRecord
	groups   []GroupRecord
	users    []UserRecord
	errs     []ErrorRecord
}

func (w *collectingWriter) writeAll(wg *sync.WaitGroup, records <-chan interface{}) {
	defer wg.Done()
	for record := range records {
		w.mu.Lock()
		switch r := record.(type) {
		case RequestRecord:
			w.requests = append(w.requests, r)
		case GroupRecord:
			w.groups = append(w.groups, r)
		case UserRecord:
			w.users = append(w.users, r)
		case ErrorRecord:
			w.errs = append(w.errs, r)
		}
		w.mu.Unlock()
	}
}
