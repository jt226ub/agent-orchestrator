package profiles

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apierr"
)

func TestProfilesRoundTripThroughTheDataDir(t *testing.T) {
	dir := t.TempDir()
	svc := New(dir, nil)
	ctx := context.Background()

	empty, err := svc.Get(ctx)
	if err != nil || len(empty.Profiles) != 0 || len(empty.RulesFiles) != 0 {
		t.Fatalf("empty data dir: %+v, %v", empty, err)
	}
	profiles := map[string]domain.RoleProfile{
		"flash-coder": {Harness: domain.HarnessAgy, AgentConfig: domain.AgentConfig{Model: "gemini-3.8-flash-high"}, RulesFile: "flash-coder.md"},
	}
	saved, err := svc.SetProfiles(ctx, profiles)
	if err != nil {
		t.Fatal(err)
	}
	if saved.Profiles["flash-coder"].RulesFile != "flash-coder.md" {
		t.Fatalf("saved = %+v", saved.Profiles)
	}
	data, err := os.ReadFile(filepath.Join(dir, "profiles.json"))
	if err != nil {
		t.Fatal(err)
	}
	if doc, err := domain.ParseDefaultProfiles(data); err != nil || doc.Profiles["flash-coder"].Harness != domain.HarnessAgy {
		t.Fatalf("on-disk document = %s, %v", data, err)
	}
	if info, err := os.Stat(filepath.Join(dir, "profiles.json")); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("profiles.json mode = %v, %v", info, err)
	}

	_, err = svc.SetProfiles(ctx, map[string]domain.RoleProfile{"bad": {Harness: "nope"}})
	var apiErr *apierr.Error
	if !errors.As(err, &apiErr) || apiErr.Code != "INVALID_DEFAULT_PROFILES" {
		t.Fatalf("invalid profiles error = %v", err)
	}
	if again, err := svc.Get(ctx); err != nil || len(again.Profiles) != 1 {
		t.Fatalf("invalid write replaced the document: %+v, %v", again, err)
	}
}

func TestRulesFilesAreFlatMarkdownUnderTheRulesDir(t *testing.T) {
	dir := t.TempDir()
	svc := New(dir, nil)
	ctx := context.Background()

	if _, err := svc.ReadRules(ctx, WorkerRulesFile); err == nil {
		t.Fatal("missing rules file was readable")
	} else if apiErr := new(apierr.Error); !errors.As(err, &apiErr) || apiErr.Code != "RULES_FILE_NOT_FOUND" {
		t.Fatalf("missing rules file error = %v", err)
	}
	written, err := svc.WriteRules(ctx, WorkerRulesFile, "# Worker\n\nQuote test output verbatim.\n")
	if err != nil {
		t.Fatal(err)
	}
	if written.Name != WorkerRulesFile || written.Content == "" || written.UpdatedAt.IsZero() {
		t.Fatalf("written = %+v", written)
	}
	if data, err := os.ReadFile(filepath.Join(dir, "rules", "worker.md")); err != nil || string(data) != written.Content {
		t.Fatalf("on-disk rules = %q, %v", data, err)
	}
	snapshot, err := svc.Get(ctx)
	if err != nil || len(snapshot.RulesFiles) != 1 || snapshot.RulesFiles[0].Name != WorkerRulesFile || snapshot.RulesFiles[0].SizeBytes == 0 {
		t.Fatalf("listing = %+v, %v", snapshot.RulesFiles, err)
	}
	for _, name := range []string{"../escape.md", "sub/dir.md", "notes.txt", ".hidden.md", "", "worker"} {
		if _, err := svc.WriteRules(ctx, name, "x"); err == nil {
			t.Fatalf("accepted rules file name %q", name)
		}
		if _, err := svc.ReadRules(ctx, name); err == nil {
			t.Fatalf("read accepted rules file name %q", name)
		}
	}
	if err := svc.DeleteRules(ctx, WorkerRulesFile); err != nil {
		t.Fatal(err)
	}
	if err := svc.DeleteRules(ctx, WorkerRulesFile); err != nil {
		t.Fatalf("deleting a missing file failed: %v", err)
	}
	if after, err := svc.Get(ctx); err != nil || len(after.RulesFiles) != 0 {
		t.Fatalf("listing after delete = %+v, %v", after.RulesFiles, err)
	}
}
