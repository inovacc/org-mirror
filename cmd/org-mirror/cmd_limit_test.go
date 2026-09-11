package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/inovacc/org-mirror/internal/githubapi"
)

func sampleLimits() githubapi.RateLimits {
	return githubapi.RateLimits{Resources: map[string]githubapi.RateLimit{
		"core":   {Limit: 5000, Used: 120, Remaining: 4880, Reset: 1700003600},
		"search": {Limit: 30, Used: 1, Remaining: 29, Reset: 1700000060},
	}}
}

func TestRenderLimitsListsCoreFirstWithACountdown(t *testing.T) {
	var out bytes.Buffer
	if err := renderLimits(&out, sampleLimits(), time.Unix(1700000000, 0)); err != nil {
		t.Fatalf("render: %v", err)
	}
	text := out.String()

	if !strings.Contains(text, "core") || !strings.Contains(text, "4880") {
		t.Fatalf("core row missing from:\n%s", text)
	}
	if !strings.Contains(text, "1h0m0s") {
		t.Fatalf("core countdown missing from:\n%s", text)
	}
	coreAt := strings.Index(text, "core")
	searchAt := strings.Index(text, "search")
	if coreAt < 0 || searchAt < 0 || coreAt > searchAt {
		t.Fatalf("core must be listed before search:\n%s", text)
	}
}

func TestRenderLimitsShowsAPastResetAsAvailableNow(t *testing.T) {
	var out bytes.Buffer
	if err := renderLimits(&out, sampleLimits(), time.Unix(1700009999, 0)); err != nil {
		t.Fatalf("render: %v", err)
	}

	// The timestamp column contains hyphens, so assert on the countdown column
	// itself: every row whose reset is in the past must read "now".
	rows := strings.Split(strings.TrimSpace(out.String()), "\n")[1:]
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want 2:\n%s", len(rows), out.String())
	}
	for _, row := range rows {
		if !strings.HasSuffix(strings.TrimSpace(row), "now") {
			t.Fatalf("row %q must show a past reset as now", row)
		}
	}
}

func TestRenderLimitsJSONRoundTrips(t *testing.T) {
	var out bytes.Buffer
	if err := renderLimitsJSON(&out, sampleLimits()); err != nil {
		t.Fatalf("render json: %v", err)
	}
	var decoded githubapi.RateLimits
	if err := json.Unmarshal(out.Bytes(), &decoded); err != nil {
		t.Fatalf("decode: %v\n%s", err, out.String())
	}
	if decoded.Resources["core"].Remaining != 4880 {
		t.Fatalf("decoded = %#v", decoded.Resources["core"])
	}
}

func TestBudgetVerdict(t *testing.T) {
	cases := []struct {
		name         string
		remaining    int
		repositories int
		wantSuffices bool
	}{
		{name: "plenty", remaining: 4880, repositories: 300, wantSuffices: true},
		{name: "exactly enough", remaining: 3, repositories: 300, wantSuffices: true},
		{name: "short", remaining: 1, repositories: 300, wantSuffices: false},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			limits := githubapi.RateLimits{Resources: map[string]githubapi.RateLimit{
				"core": {Limit: 5000, Remaining: testCase.remaining, Reset: 1700003600},
			}}
			verdict := budgetVerdict(limits, testCase.repositories)
			suffices := strings.Contains(verdict, "enough")
			if suffices != testCase.wantSuffices {
				t.Fatalf("verdict %q, want suffices=%v", verdict, testCase.wantSuffices)
			}
		})
	}
}

func TestBudgetVerdictIsEmptyWithoutACoreResource(t *testing.T) {
	if verdict := budgetVerdict(githubapi.RateLimits{}, 10); verdict != "" {
		t.Fatalf("verdict = %q, want empty", verdict)
	}
}

func TestNewLimitCommandAcceptsAnOptionalOrganization(t *testing.T) {
	command := newLimitCommand()
	if command.Use != "limit [organization]" {
		t.Fatalf("use = %q", command.Use)
	}
	if command.Flags().Lookup("json") == nil {
		t.Fatal("limit must expose a json flag")
	}
	if err := command.Args(command, []string{}); err != nil {
		t.Fatalf("no argument must be valid: %v", err)
	}
	if err := command.Args(command, []string{"acme"}); err != nil {
		t.Fatalf("one argument must be valid: %v", err)
	}
	if err := command.Args(command, []string{"acme", "extra"}); err == nil {
		t.Fatal("two arguments must be rejected")
	}
}
