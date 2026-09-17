package agy

import (
	"context"
	"errors"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func TestParseAgyUsageMapsGroupsToBuckets(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "usage.json"))
	if err != nil {
		t.Fatal(err)
	}
	observedAt := time.Date(2026, 9, 17, 15, 45, 0, 0, time.UTC)
	got, err := parseAgyUsage(raw, observedAt)
	if err != nil {
		t.Fatal(err)
	}
	if !got.ObservedAt.Equal(observedAt) {
		t.Fatalf("observedAt = %v, want %v", got.ObservedAt, observedAt)
	}
	if got.Overall == nil || got.Overall.LimitID != "gemini" || got.Overall.DisplayName == nil || *got.Overall.DisplayName != "Gemini Models" {
		t.Fatalf("overall = %#v, want the Gemini group", got.Overall)
	}
	if got.Overall.Primary == nil || math.Abs(got.Overall.Primary.UsedPercent-1.689) > 0.01 || *got.Overall.Primary.WindowDurationMinutes != 300 {
		t.Fatalf("primary = %#v, want the 5-hour window", got.Overall.Primary)
	}
	if got.Overall.Secondary == nil || math.Abs(got.Overall.Secondary.UsedPercent-24.075) > 0.01 || *got.Overall.Secondary.WindowDurationMinutes != 10080 {
		t.Fatalf("secondary = %#v, want the weekly window", got.Overall.Secondary)
	}
	wantReset := time.Date(2026, 9, 17, 17, 43, 35, 0, time.UTC)
	if got.Overall.Primary.ResetsAt == nil || !got.Overall.Primary.ResetsAt.Equal(wantReset) {
		t.Fatalf("primary resetsAt = %v, want %v", got.Overall.Primary.ResetsAt, wantReset)
	}
	if len(got.AdditionalBuckets) != 1 || got.AdditionalBuckets[0].LimitID != "3p" || got.AdditionalBuckets[0].Primary.UsedPercent != 0 || got.AdditionalBuckets[0].Secondary.UsedPercent != 0 {
		t.Fatalf("additional = %#v, want the Claude and GPT group at zero use", got.AdditionalBuckets)
	}
}

func TestParseAgyUsageClassifiesFailures(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want error
	}{
		{name: "signed out", raw: `{"status":"ERROR","error":"Authentication required. Please visit the URL to sign in."}`, want: ports.ErrAgyCapacitySignedOut},
		{name: "other error", raw: `{"status":"ERROR","error":"No capacity available"}`, want: ports.ErrAgyCapacityRequestRejected},
		{name: "no groups", raw: `{"status":"SUCCESS","command":{"name":"usage","data":{"groups":[]}}}`, want: ports.ErrAgyCapacityRequestRejected},
		{name: "unknown windows only", raw: `{"status":"SUCCESS","command":{"name":"usage","data":{"groups":[{"name":"X","buckets":[{"id":"x-daily","window":"daily","remaining_fraction":0.5}]}]}}}`, want: ports.ErrAgyCapacityRequestRejected},
		{name: "malformed", raw: `not json`, want: ports.ErrAgyCapacityRequestRejected},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := parseAgyUsage([]byte(tc.raw), time.Now())
			if !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
		})
	}
}

func TestParseAgyUsageFallsBackToFirstGroupAndClampsPercent(t *testing.T) {
	raw := `{"status":"SUCCESS","command":{"name":"usage","data":{"groups":[{"name":"Claude and GPT models","buckets":[{"id":"3p-5h","window":"5h","remaining_fraction":-0.2,"reset_time":"bad"},{"id":"3p-weekly","window":"weekly","remaining_fraction":1.4}]}]}}}`
	got, err := parseAgyUsage([]byte(raw), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if got.Overall == nil || got.Overall.LimitID != "3p" || len(got.AdditionalBuckets) != 0 {
		t.Fatalf("observation = %#v, want the only group promoted to overall", got)
	}
	if got.Overall.Primary.UsedPercent != 100 || got.Overall.Primary.ResetsAt != nil {
		t.Fatalf("primary = %#v, want clamped to 100 with no reset time", got.Overall.Primary)
	}
	if got.Overall.Secondary.UsedPercent != 0 {
		t.Fatalf("secondary = %#v, want clamped to 0", got.Overall.Secondary)
	}
}

func TestReadAgyCapacityRunsTheUsageCommand(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture")
	}
	dir := t.TempDir()
	fixture, err := os.ReadFile(filepath.Join("testdata", "usage.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "usage.json"), fixture, 0o600); err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" > \"$(dirname \"$0\")/args\"\nenv > \"$(dirname \"$0\")/env\"\ncat \"$(dirname \"$0\")/usage.json\"\n"
	binary := filepath.Join(dir, "agy")
	if err := os.WriteFile(binary, []byte(script), 0o700); err != nil { //nolint:gosec // test fixture must be executable
		t.Fatal(err)
	}
	t.Setenv("GEMINI_API_KEY", "must-not-reach-the-cli")
	plugin := &Plugin{resolvedBinary: binary}
	got, err := plugin.ReadAgyCapacity(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got.Overall == nil || got.Overall.LimitID != "gemini" {
		t.Fatalf("observation = %#v", got)
	}
	args, _ := os.ReadFile(filepath.Join(dir, "args"))
	if string(args) != "-p /usage --output-format json\n" {
		t.Fatalf("args = %q", args)
	}
	env, _ := os.ReadFile(filepath.Join(dir, "env"))
	if contains := string(env); containsLine(contains, "GEMINI_API_KEY=") {
		t.Fatal("GEMINI_API_KEY reached the CLI")
	}
}

func TestReadAgyCapacityReportsSignedOutFromStderr(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture")
	}
	dir := t.TempDir()
	binary := filepath.Join(dir, "agy")
	if err := os.WriteFile(binary, []byte("#!/bin/sh\necho 'Authentication required. Please visit the URL' >&2\nexit 1\n"), 0o700); err != nil { //nolint:gosec // test fixture must be executable
		t.Fatal(err)
	}
	plugin := &Plugin{resolvedBinary: binary}
	_, err := plugin.ReadAgyCapacity(context.Background())
	if !errors.Is(err, ports.ErrAgyCapacitySignedOut) {
		t.Fatalf("err = %v, want signed out", err)
	}
}

func containsLine(text, prefix string) bool {
	for _, line := range splitLines(text) {
		if len(line) >= len(prefix) && line[:len(prefix)] == prefix {
			return true
		}
	}
	return false
}

func splitLines(text string) []string {
	var lines []string
	start := 0
	for i := 0; i < len(text); i++ {
		if text[i] == '\n' {
			lines = append(lines, text[start:i])
			start = i + 1
		}
	}
	if start < len(text) {
		lines = append(lines, text[start:])
	}
	return lines
}
