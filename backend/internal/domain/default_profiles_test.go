package domain

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestParseDefaultProfilesValidatesLikeProjectProfiles(t *testing.T) {
	doc, err := ParseDefaultProfiles([]byte(`{"profiles":{"flash-coder":{"agent":"agy","agentConfig":{"model":"gemini-3.8-flash-high"},"rulesFile":"flash-coder.md","quota":{"warnBelowPercent":20},"fallback":{"profile":"pro-expert"}},"pro-expert":{"agent":"agy"}}}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(doc.Profiles) != 2 || doc.Profiles["flash-coder"].RulesFile != "flash-coder.md" {
		t.Fatalf("doc = %+v", doc)
	}
	empty, err := ParseDefaultProfiles([]byte("  \n"))
	if err != nil || len(empty.Profiles) != 0 {
		t.Fatalf("empty document: %+v, %v", empty, err)
	}
	for name, data := range map[string]string{
		"not json":               `{`,
		"unknown harness":        `{"profiles":{"x":{"agent":"nope"}}}`,
		"escaping rules file":    `{"profiles":{"x":{"rulesFile":"../contract.md"}}}`,
		"path separator":         `{"profiles":{"a/b":{}}}`,
		"dangling fallback":      `{"profiles":{"x":{"quota":{"refuseBelowPercent":10},"fallback":{"profile":"missing"}}}}`,
		"fallback without quota": `{"profiles":{"x":{"fallback":{"profile":"y"}},"y":{}}}`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseDefaultProfiles([]byte(data)); err == nil {
				t.Fatalf("accepted %s", data)
			}
		})
	}
}

func TestWithDefaultProfilesProjectEntriesWin(t *testing.T) {
	cfg := ProjectConfig{Profiles: map[string]RoleProfile{"pro-expert": {Harness: HarnessClaudeCode}}}
	defaults := map[string]RoleProfile{
		"pro-expert":  {Harness: HarnessAgy, RulesFile: "pro-expert.md"},
		"flash-coder": {Harness: HarnessAgy, RulesFile: "flash-coder.md"},
	}
	merged := cfg.WithDefaultProfiles(defaults)
	if merged.Profiles["pro-expert"].Harness != HarnessClaudeCode {
		t.Fatalf("project profile was overwritten: %+v", merged.Profiles["pro-expert"])
	}
	if merged.Profiles["flash-coder"].RulesFile != "flash-coder.md" {
		t.Fatalf("default profile missing: %+v", merged.Profiles)
	}
	if !merged.IsDefaultProfile("flash-coder") || merged.IsDefaultProfile("pro-expert") || merged.IsDefaultProfile("missing") {
		t.Fatalf("origin tracking wrong: %v", merged.defaultProfiles)
	}
	if len(cfg.Profiles) != 1 || cfg.IsDefaultProfile("flash-coder") {
		t.Fatal("WithDefaultProfiles mutated the receiver")
	}
	folded, err := merged.WithProfile(KindWorker, "flash-coder")
	if err != nil {
		t.Fatal(err)
	}
	if folded.Worker.Harness != HarnessAgy || !folded.IsDefaultProfile("flash-coder") {
		t.Fatalf("fold lost the default: %+v", folded.Worker)
	}
	if same := cfg.WithDefaultProfiles(nil); len(same.Profiles) != 1 {
		t.Fatalf("nil defaults changed the config: %+v", same)
	}
	if err := merged.Validate(); err != nil {
		t.Fatalf("merged config invalid: %v", err)
	}
	encoded, err := json.Marshal(merged)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "defaultProfiles") {
		t.Fatal("origin record leaked into JSON")
	}
}
