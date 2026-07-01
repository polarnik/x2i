package gatlingparser

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"unicode/utf8"

	l "github.com/perfana/x2i/logger"
)

// These tests parse the real, non-synthetic Gatling 3.15.0 binary logs captured from the
// YouTrack performance suite and stored under test/data/custom/3.15.0/{warm-up,topapi}.
//
// The expected request names, group names and the error message are taken from the matching
// Gatling HTML reports:
//
//	warm-up : build/gatling-report-warmUp/warmuptopapisimulation-20260602083027581/index.html
//	topapi  : build/gatling-report/latest/index.html
//
// Because those reports live outside this repository, their names are pinned here as the
// authoritative expectation. Every string decoded from the log (scenario names, group
// hierarchy elements, request names and error messages) must be a well-formed value: valid
// UTF-8, without embedded NUL bytes / trailing zeros and without the Unicode replacement
// character. Any decoded string that is malformed, or that does not belong to the expected
// set from the report, is treated as a parsing error.

const (
	warmUpLogPath = "custom/3.15.0/warm-up"
	topApiLogPath = "custom/3.15.0/topapi"

	customSimulationClass = "jetbrains.youtrack.performance.apitests.testTopAPI.simulation"
	customGatlingVersion  = "3.15.0"
)

// customExpectation captures everything we assert for a single real-world fixture.
type customExpectation struct {
	relPath         string
	simulationClass string
	scenarios       []string
	requestNames    []string
	groupElements   []string
	errorMessages   []string // distinct error messages expected (nil => no errors at all)

	requestCount int
	groupCount   int
	userCount    int
	errorCount   int
}

// validateDecodedString fails the test if s is not a clean, human-readable value. This is the
// core guard the issue asks for: strings padded with trailing zeros or containing non-Unicode
// bytes indicate a broken decode and must be reported as an error.
func validateDecodedString(t *testing.T, label, s string) {
	t.Helper()
	if s == "" {
		t.Errorf("%s: decoded an empty string", label)
		return
	}
	if !utf8.ValidString(s) {
		t.Errorf("%s: decoded a non-UTF-8 string %q (bytes: %v)", label, s, []byte(s))
	}
	if strings.ContainsRune(s, 0) {
		t.Errorf("%s: decoded string %q contains a NUL byte (bytes: %v)", label, s, []byte(s))
	}
	if strings.ContainsRune(s, '\uFFFD') {
		t.Errorf("%s: decoded string %q contains the Unicode replacement character", label, s)
	}
}

func sortedKeys(m map[string]int) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// assertSetEqual checks that the distinct values in got exactly match want.
func assertSetEqual(t *testing.T, label string, got map[string]int, want []string) {
	t.Helper()
	wantSet := make(map[string]bool, len(want))
	for _, w := range want {
		wantSet[w] = true
	}
	for _, g := range sortedKeys(got) {
		if !wantSet[g] {
			t.Errorf("%s: decoded unexpected value %q (not present in the report)", label, g)
		}
	}
	gotSet := make(map[string]bool, len(got))
	for g := range got {
		gotSet[g] = true
	}
	for _, w := range want {
		if !gotSet[w] {
			t.Errorf("%s: expected value %q from the report was not decoded from the log", label, w)
		}
	}
}

func runCustomLogTest(t *testing.T, exp customExpectation) {
	l.InitLogger("testlog.log")

	path := filepath.Join(testDataRoot, filepath.FromSlash(exp.relPath), simulationLogFileName)
	log := parseBinaryLog(t, path)

	// Header level checks.
	if log.run.GatlingVersion != customGatlingVersion {
		t.Errorf("version %q, expected %q", log.run.GatlingVersion, customGatlingVersion)
	}
	validateDecodedString(t, "simulationClass", log.run.SimulationClassName)
	if !strings.HasPrefix(log.run.SimulationClassName, exp.simulationClass) {
		t.Errorf("simulation class %q, expected prefix %q", log.run.SimulationClassName, exp.simulationClass)
	}

	// Record count regression guards.
	if len(log.requests) != exp.requestCount {
		t.Errorf("request count %d, expected %d", len(log.requests), exp.requestCount)
	}
	if len(log.groups) != exp.groupCount {
		t.Errorf("group count %d, expected %d", len(log.groups), exp.groupCount)
	}
	if len(log.users) != exp.userCount {
		t.Errorf("user count %d, expected %d", len(log.users), exp.userCount)
	}
	if len(log.errs) != exp.errorCount {
		t.Errorf("error count %d, expected %d", len(log.errs), exp.errorCount)
	}

	// Validate + collect every decoded string, then compare the distinct sets with the report.
	scenarioSet := map[string]int{}
	requestNameSet := map[string]int{}
	groupElementSet := map[string]int{}
	errorMessageSet := map[string]int{}

	for _, r := range log.requests {
		label := fmt.Sprintf("%s request name", exp.relPath)
		validateDecodedString(t, label, r.Name)
		requestNameSet[r.Name]++
		if r.Group != nil {
			for _, e := range r.Group.Hierarchy {
				validateDecodedString(t, exp.relPath+" request group element", e)
				groupElementSet[e]++
			}
		}
	}
	for _, g := range log.groups {
		for _, e := range g.Group.Hierarchy {
			validateDecodedString(t, exp.relPath+" group element", e)
			groupElementSet[e]++
		}
	}
	for _, u := range log.users {
		validateDecodedString(t, exp.relPath+" user scenario", u.Scenario)
		scenarioSet[u.Scenario]++
	}
	for _, e := range log.errs {
		validateDecodedString(t, exp.relPath+" error message", e.Message)
		errorMessageSet[e.Message]++
	}

	assertSetEqual(t, exp.relPath+" scenarios", scenarioSet, exp.scenarios)
	assertSetEqual(t, exp.relPath+" request names", requestNameSet, exp.requestNames)
	assertSetEqual(t, exp.relPath+" group elements", groupElementSet, exp.groupElements)
	assertSetEqual(t, exp.relPath+" error messages", errorMessageSet, exp.errorMessages)
}

func TestCustomWarmUpLog(t *testing.T) {
	runCustomLogTest(t, customExpectation{
		relPath:         warmUpLogPath,
		simulationClass: customSimulationClass,
		scenarios:       customScenarios,
		requestNames:    warmUpRequestNames,
		groupElements:   warmUpGroupElements,
		errorMessages:   warmUpErrorMessages,
		requestCount:    825,
		groupCount:      2848,
		userCount:       1706,
		errorCount:      11,
	})
}

func TestCustomTopApiLog(t *testing.T) {
	runCustomLogTest(t, customExpectation{
		relPath:         topApiLogPath,
		simulationClass: customSimulationClass,
		scenarios:       customScenarios,
		requestNames:    topApiRequestNames,
		groupElements:   topApiGroupElements,
		errorMessages:   nil,
		requestCount:    30699,
		groupCount:      103717,
		userCount:       61220,
		errorCount:      0,
	})
}

// customScenarios are the injected scenario names, shared by both fixtures.
var customScenarios = []string{
	`getGuestAuthToken_1`,
	`allEndpoints`,
	`getGuestAuthToken_2`,
}

// warmUpErrorMessages is the single distinct error produced by the warm-up run (11 records).
var warmUpErrorMessages = []string{
	`POST /api/users/me/drafts/{ID}?saveDraft: No attribute named 'DRAFT_ID' is defined `,
}

var warmUpRequestNames = []string{
	`GET /api/activities?fields`,
	`GET /api/admin/projects?fields`,
	`GET /api/agiles/{ID}/sprints/{ID}?fields`,
	`GET /api/appResources/{APP}/article-feedback-logo.svg`,
	`GET /api/appResources/{APP}/icon.svg`,
	`GET /api/appResources/{APP}/notes.svg`,
	`GET /api/appResources/{APP}/widgets/{NAME}/index.html`,
	`GET /api/appResources/{APP}/youtrack.svg`,
	`GET /api/articles?fields`,
	`GET /api/config?fields`,
	`GET /api/config?fields={FIELDS}`,
	`GET /api/eventSourceBus?fields`,
	`GET /api/files/{ID}?sign={SIGN}`,
	`GET /api/files/{ID}?sign={SIGN}&updated={TIMESTAMP}`,
	`GET /api/filterFields?fields`,
	`GET /api/inbox/threads?fields`,
	`GET /api/issueFolders/{ID}/sortOrder/issues?fields`,
	`GET /api/issues/{ID}/activitiesPage?fields`,
	`GET /api/issues/{ID}/links?topLinks25`,
	`GET /api/issues/{ID}/sprints?fields`,
	`GET /api/issues/{ID}?fields=aFewFields`,
	`GET /api/issues/{ID}?fields=draftComment`,
	`GET /api/issues/{ID}?fields=draftComment,isGuest`,
	`GET /api/issues/{ID}?fields=summary`,
	`GET /api/issues/{ID}?fields=usersTyping`,
	`GET /api/issues/{ID}?top,fields=allFields`,
	`GET /api/issues/{ID}?top,fields=allFields,referringQuery`,
	`GET /api/issues/{ID}?top,fields=allFields,referringQuery=query`,
	`GET /api/issues/{ID}?top,fields=allMentions,customFields`,
	`GET /api/issues?from-idea-plugin`,
	`GET /api/permissions/cache?fields`,
	`GET /api/reports?fields={FIELDS}`,
	`GET /api/savedQueries?fields`,
	`GET /api/sortedIssues?guest-query`,
	`GET /api/sortedIssues?user-emptyQuery`,
	`GET /api/sortedIssues?user-emptyQuery-folder`,
	`GET /api/sortedIssues?user-query`,
	`GET /api/sortedIssues?user-query-11`,
	`GET /api/sortedIssues?user-query-fewFields`,
	`GET /api/suggestedActions?fields`,
	`GET /api/tags?fields`,
	`GET /api/users/me/recent/issues?fields`,
	`GET /api/users/me?fields`,
	`GET /hub/api/rest/oauth2/auth`,
	`GET /oauth`,
	`POST /api/analytics?event`,
	`POST /api/articles/?publishFromDraft`,
	`POST /api/commands?applyCommand`,
	`POST /api/grazie/complete?request`,
	`POST /api/grazie/gec?request`,
	`POST /api/issues/?createIssue`,
	`POST /api/issues/similar?query`,
	`POST /api/issues/{ID}/attachments?upload`,
	`POST /api/issues/{ID}/draftComment?4000symbols`,
	`POST /api/issuesGetter?100issues`,
	`POST /api/projectDocuments/similarTextBased?query`,
	`POST /api/search/assist?emptyQuery`,
	`POST /api/sortedArticles?emptyQuery`,
	`POST /api/users/me/articleDrafts/?createDraft`,
	`POST /api/users/me/articleDrafts/{ID}?finalUpdateDraftContent`,
	`POST /api/users/me/profiles/articles`,
	`POST /api/users/me/recent/articles`,
}

var warmUpGroupElements = []string{
	`(common)`,
	`/api/activities`,
	`/api/admin/projects`,
	`/api/agiles/{ID}/sprints/{ID}`,
	`/api/analytics`,
	`/api/appResources/{APP_RESOURCE}`,
	`/api/articleViews`,
	`/api/articles`,
	`/api/articles/{ID}/activitiesPage`,
	`/api/commands`,
	`/api/config`,
	`/api/eventSourceBus`,
	`/api/files/{ID}`,
	`/api/filterFields`,
	`/api/grazie/complete`,
	`/api/grazie/gec`,
	`/api/inbox/threads`,
	`/api/issueFolders/{ID}/sortOrder/issues`,
	`/api/issues`,
	`/api/issues/`,
	`/api/issues/similar`,
	`/api/issues/{ID}`,
	`/api/issues/{ID}/activitiesPage`,
	`/api/issues/{ID}/attachments`,
	`/api/issues/{ID}/draftComment`,
	`/api/issues/{ID}/links`,
	`/api/issues/{ID}/sprints`,
	`/api/issuesGetter`,
	`/api/permissions/cache`,
	`/api/projectDocuments/similarTextBased`,
	`/api/reports`,
	`/api/savedQueries`,
	`/api/search/assist`,
	`/api/sortedArticles`,
	`/api/sortedIssues`,
	`/api/suggestedActions`,
	`/api/tags`,
	`/api/users/me`,
	`/api/users/me/articleDrafts`,
	`/api/users/me/articleDrafts/{ID}`,
	`/api/users/me/drafts/{ID}`,
	`/api/users/me/profiles/articles`,
	`/api/users/me/recent/articles`,
	`/api/users/me/recent/issues`,
	`/hub/api/rest/oauth2/auth`,
	`100-issues`,
	`4000-symbols`,
	`Apply Command`,
	`Create Issue`,
	`Empty Query`,
	`Event`,
	`Fields`,
	`From IDEA Plugin`,
	`GET`,
	`New Draft`,
	`POST`,
	`Publish an Article Draft`,
	`Query`,
	`Request`,
	`Save Draft`,
	`Upload`,
	`aFewFields`,
	`allFields`,
	`allFields,referringQuery`,
	`allMentions`,
	`application/pdf`,
	`application/zip`,
	`draftComment`,
	`emptyQuery`,
	`fewFields`,
	`files`,
	`folder`,
	`from-dashboards`,
	`guest`,
	`image/jpeg`,
	`image/png`,
	`image/svg+xml`,
	`query`,
	`summary`,
	`text/plain`,
	`thumbnails`,
	`top-links-25`,
	`top11`,
	`user`,
	`usersTyping`,
}

var topApiRequestNames = []string{
	`GET /api/activities?fields`,
	`GET /api/admin/projects?fields`,
	`GET /api/agiles/{ID}/sprints/{ID}?fields`,
	`GET /api/appResources/{APP}/article-feedback-logo.svg`,
	`GET /api/appResources/{APP}/icon.svg`,
	`GET /api/appResources/{APP}/notes.svg`,
	`GET /api/appResources/{APP}/widgets/{NAME}/index.html`,
	`GET /api/appResources/{APP}/youtrack.svg`,
	`GET /api/articles/{ID}/activitiesPage?fields`,
	`GET /api/articles/{ID}?allFields`,
	`GET /api/articles/{ID}?allMentions`,
	`GET /api/articles/{ID}?draftComment`,
	`GET /api/articles/{ID}?fields`,
	`GET /api/articles/{ID}?fields=pinnedComments`,
	`GET /api/articles?fields`,
	`GET /api/config?fields`,
	`GET /api/config?fields={FIELDS}`,
	`GET /api/eventSourceBus?fields`,
	`GET /api/files/{ID}?sign={SIGN}`,
	`GET /api/files/{ID}?sign={SIGN}&updated={TIMESTAMP}`,
	`GET /api/filterFields?fields`,
	`GET /api/inbox/folders?fields`,
	`GET /api/inbox/threads?fields`,
	`GET /api/issueFolders/{ID}/sortOrder/issues?fields`,
	`GET /api/issues/{ID}/activitiesPage?fields`,
	`GET /api/issues/{ID}/links?topLinks25`,
	`GET /api/issues/{ID}/sprints?fields`,
	`GET /api/issues/{ID}?fields=aFewFields`,
	`GET /api/issues/{ID}?fields=draftComment`,
	`GET /api/issues/{ID}?fields=draftComment,isGuest`,
	`GET /api/issues/{ID}?fields=summary`,
	`GET /api/issues/{ID}?fields=usersTyping`,
	`GET /api/issues/{ID}?top,fields=allFields`,
	`GET /api/issues/{ID}?top,fields=allFields,referringQuery`,
	`GET /api/issues/{ID}?top,fields=allFields,referringQuery=query`,
	`GET /api/issues/{ID}?top,fields=allMentions,customFields`,
	`GET /api/issues?from-idea-plugin`,
	`GET /api/permissions/cache?fields`,
	`GET /api/reports?fields={FIELDS}`,
	`GET /api/savedQueries?fields`,
	`GET /api/sortedIssues?guest-emptyQuery`,
	`GET /api/sortedIssues?guest-emptyQuery-folder`,
	`GET /api/sortedIssues?guest-query`,
	`GET /api/sortedIssues?guest-query-folder`,
	`GET /api/sortedIssues?user-emptyQuery`,
	`GET /api/sortedIssues?user-emptyQuery-folder`,
	`GET /api/sortedIssues?user-emptyQuery-folder-tree`,
	`GET /api/sortedIssues?user-query`,
	`GET /api/sortedIssues?user-query-11`,
	`GET /api/sortedIssues?user-query-fewFields`,
	`GET /api/sortedIssues?user-query-folder`,
	`GET /api/suggestedActions?fields`,
	`GET /api/tags?fields`,
	`GET /api/users/me/articleDrafts/{ID}?fields`,
	`GET /api/users/me/drafts?top,fields`,
	`GET /api/users/me/recent/issues?fields`,
	`GET /api/users/me?fields`,
	`GET /hub/api/rest/oauth2/auth`,
	`GET /oauth`,
	`POST /api/analytics?event`,
	`POST /api/articleViews?view`,
	`POST /api/articles/?publishFromDraft`,
	`POST /api/commands?applyCommand`,
	`POST /api/grazie/complete?request`,
	`POST /api/grazie/gec?request`,
	`POST /api/issues/?createIssue`,
	`POST /api/issues/similar?query`,
	`POST /api/issues/{ID}/attachments?upload`,
	`POST /api/issues/{ID}/draftComment?4000symbols`,
	`POST /api/issuesGetter?100issues`,
	`POST /api/projectDocuments/similarTextBased?query`,
	`POST /api/search/assist?emptyQuery`,
	`POST /api/searchPage?emptyQuery`,
	`POST /api/sortedArticles?emptyQuery`,
	`POST /api/users/me/articleDrafts/?createDraft`,
	`POST /api/users/me/articleDrafts/{ID}?finalUpdateDraftContent`,
	`POST /api/users/me/articleDrafts/{ID}?updateDraftContent`,
	`POST /api/users/me/profiles/articles`,
	`POST /api/users/me/recent/articles`,
}

var topApiGroupElements = []string{
	`(common)`,
	`/api/activities`,
	`/api/admin/projects`,
	`/api/agiles/{ID}/sprints/{ID}`,
	`/api/analytics`,
	`/api/appResources/{APP_RESOURCE}`,
	`/api/articleViews`,
	`/api/articles`,
	`/api/articles/{ID}`,
	`/api/articles/{ID}/activitiesPage`,
	`/api/commands`,
	`/api/config`,
	`/api/eventSourceBus`,
	`/api/files/{ID}`,
	`/api/filterFields`,
	`/api/grazie/complete`,
	`/api/grazie/gec`,
	`/api/inbox/folders`,
	`/api/inbox/threads`,
	`/api/issueFolders/{ID}/sortOrder/issues`,
	`/api/issues`,
	`/api/issues/`,
	`/api/issues/similar`,
	`/api/issues/{ID}`,
	`/api/issues/{ID}/activitiesPage`,
	`/api/issues/{ID}/attachments`,
	`/api/issues/{ID}/draftComment`,
	`/api/issues/{ID}/links`,
	`/api/issues/{ID}/sprints`,
	`/api/issuesGetter`,
	`/api/permissions/cache`,
	`/api/projectDocuments/similarTextBased`,
	`/api/reports`,
	`/api/savedQueries`,
	`/api/search/assist`,
	`/api/searchPage`,
	`/api/sortedArticles`,
	`/api/sortedIssues`,
	`/api/suggestedActions`,
	`/api/tags`,
	`/api/users/me`,
	`/api/users/me/articleDrafts`,
	`/api/users/me/articleDrafts/{ID}`,
	`/api/users/me/drafts`,
	`/api/users/me/drafts/{ID}`,
	`/api/users/me/profiles/articles`,
	`/api/users/me/recent/articles`,
	`/api/users/me/recent/issues`,
	`/hub/api/rest/oauth2/auth`,
	`100-issues`,
	`4000-symbols`,
	`Activities`,
	`Apply Command`,
	`Create Issue`,
	`Empty Query`,
	`Event`,
	`Fields`,
	`From IDEA Plugin`,
	`GET`,
	`New Draft`,
	`New article draft`,
	`POST`,
	`Publish an Article Draft`,
	`Query`,
	`Request`,
	`Upload`,
	`View`,
	`aFewFields`,
	`allFields`,
	`allFields,referringQuery`,
	`allMentions`,
	`draftComment`,
	`emptyQuery`,
	`fewFields`,
	`files`,
	`folder`,
	`folder-tree`,
	`from-dashboards`,
	`guest`,
	`image/png`,
	`query`,
	`summary`,
	`text/plain`,
	`thumbnails`,
	`top-links-25`,
	`top11`,
	`user`,
	`usersTyping`,
}
