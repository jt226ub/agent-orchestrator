package domain

import (
	"reflect"
	"testing"
)

func TestProjectConfigValidate(t *testing.T) {
	tests := []struct {
		name    string
		cfg     ProjectConfig
		wantErr bool
	}{
		{"empty ok", ProjectConfig{}, false},
		{"good agent config", ProjectConfig{AgentConfig: AgentConfig{Model: "m", Permissions: PermissionModeAuto}}, false},
		{"good agent mode", ProjectConfig{AgentConfig: AgentConfig{Mode: "ultra"}}, false},
		{"bad agent mode", ProjectConfig{AgentConfig: AgentConfig{Mode: "turbo"}}, true},
		{"bad permission", ProjectConfig{AgentConfig: AgentConfig{Permissions: "yolo"}}, true},
		{"good session prefix", ProjectConfig{SessionPrefix: "ao"}, false},
		{"session prefix with slash", ProjectConfig{SessionPrefix: "ao/project"}, true},
		{"session prefix with backslash", ProjectConfig{SessionPrefix: `ao\project`}, true},
		{"session prefix traversal component", ProjectConfig{SessionPrefix: ".."}, true},
		{"good role override", ProjectConfig{Worker: RoleOverride{Harness: HarnessCodex}}, false},
		{"good profile", ProjectConfig{Profiles: map[string]RoleProfile{"flash-coder": {Harness: HarnessAgy, AgentConfig: AgentConfig{Model: "gemini-3.8-flash-high", Permissions: PermissionModeBypassPermissions}, RulesFile: "rules/flash-coder.md", Env: map[string]string{"X": "1"}}}}, false},
		{"role names a defined profile", ProjectConfig{Worker: RoleOverride{Profile: "p"}, Profiles: map[string]RoleProfile{"p": {Harness: HarnessCodex}}}, false},
		{"role names an undefined profile", ProjectConfig{Worker: RoleOverride{Profile: "missing"}}, true},
		{"profile unknown harness", ProjectConfig{Profiles: map[string]RoleProfile{"p": {Harness: "nope"}}}, true},
		{"profile bad agent config", ProjectConfig{Profiles: map[string]RoleProfile{"p": {AgentConfig: AgentConfig{Permissions: "yolo"}}}}, true},
		{"good template", ProjectConfig{Profiles: map[string]RoleProfile{"w": {Harness: HarnessAgy}, "r": {Harness: HarnessClaudeCode}}, Templates: map[string]WorkflowTemplate{"flash-first": {Worker: "w", Reviewers: []string{"r"}, OrchestratorRulesFile: "rules/plan.md"}}, Template: "flash-first"}, false},
		{"template names an undefined worker profile", ProjectConfig{Templates: map[string]WorkflowTemplate{"t": {Worker: "missing"}}}, true},
		{"template names an undefined reviewer profile", ProjectConfig{Templates: map[string]WorkflowTemplate{"t": {Reviewers: []string{"missing"}}}}, true},
		{"template reviewer profile cannot review", ProjectConfig{Profiles: map[string]RoleProfile{"g": {Harness: HarnessGoose}}, Templates: map[string]WorkflowTemplate{"t": {Reviewers: []string{"g"}}}}, true},
		{"template plan file escapes the repo", ProjectConfig{Templates: map[string]WorkflowTemplate{"t": {OrchestratorRulesFile: "../plan.md"}}}, true},
		{"template name with slash", ProjectConfig{Templates: map[string]WorkflowTemplate{"a/b": {}}}, true},
		{"active template undefined", ProjectConfig{Template: "missing"}, true},
		{"reviewer names a defined profile", ProjectConfig{Profiles: map[string]RoleProfile{"r": {Harness: HarnessCodex}}, Reviewers: []ReviewerConfig{{Harness: ReviewerCodex, Profile: "r"}}}, false},
		{"reviewer names an undefined profile", ProjectConfig{Reviewers: []ReviewerConfig{{Harness: ReviewerCodex, Profile: "missing"}}}, true},
		{"reviewer profile cannot review", ProjectConfig{Profiles: map[string]RoleProfile{"g": {Harness: HarnessGoose}}, Reviewers: []ReviewerConfig{{Harness: ReviewerCodex, Profile: "g"}}}, true},
		{"profile rules file escapes", ProjectConfig{Profiles: map[string]RoleProfile{"p": {RulesFile: "../rules.md"}}}, true},
		{"profile name with slash", ProjectConfig{Profiles: map[string]RoleProfile{"a/b": {Harness: HarnessCodex}}}, true},
		{"profile name with whitespace", ProjectConfig{Profiles: map[string]RoleProfile{" p": {Harness: HarnessCodex}}}, true},
		{"unknown role harness", ProjectConfig{Orchestrator: RoleOverride{Harness: "nope"}}, true},
		{"bad role agent config", ProjectConfig{Worker: RoleOverride{AgentConfig: AgentConfig{Permissions: "nope"}}}, true},
		{"good symlinks", ProjectConfig{Symlinks: []string{".env", "configs/dev.toml"}}, false},
		{"symlink absolute path", ProjectConfig{Symlinks: []string{"/etc/passwd"}}, true},
		{"symlink parent escape", ProjectConfig{Symlinks: []string{"../escape"}}, true},
		{"symlink embedded parent", ProjectConfig{Symlinks: []string{"a/../../b"}}, true},
		{"symlink bare ..", ProjectConfig{Symlinks: []string{".."}}, true},
		{"good prompt rules", ProjectConfig{AgentRules: "Run tests.", AgentRulesFile: "docs/agent-rules.md", OrchestratorRules: "Delegate work."}, false},
		{"agent rules file absolute path", ProjectConfig{AgentRulesFile: "/etc/passwd"}, true},
		{"agent rules file parent escape", ProjectConfig{AgentRulesFile: "../rules.md"}, true},
		{"agent rules file cleans to dot", ProjectConfig{AgentRulesFile: "docs/.."}, true},
		{"agent rules file bare dot", ProjectConfig{AgentRulesFile: "."}, true},
		{"good reviewers", ProjectConfig{Reviewers: []ReviewerConfig{{Harness: ReviewerClaudeCode}}}, false},
		{"good codex reviewer", ProjectConfig{Reviewers: []ReviewerConfig{{Harness: ReviewerCodex}}}, false},
		{"good copilot reviewer", ProjectConfig{Reviewers: []ReviewerConfig{{Harness: ReviewerCopilot}}}, false},
		{"good cursor reviewer", ProjectConfig{Reviewers: []ReviewerConfig{{Harness: ReviewerCursor}}}, false},
		{"good Kilo Code reviewer", ProjectConfig{Reviewers: []ReviewerConfig{{Harness: ReviewerKiloCode}}}, false},
		{"good kimchi reviewer", ProjectConfig{Reviewers: []ReviewerConfig{{Harness: ReviewerKimchi}}}, false},
		{"good opencode reviewer", ProjectConfig{Reviewers: []ReviewerConfig{{Harness: ReviewerOpenCode}}}, false},
		{"good kiro reviewer", ProjectConfig{Reviewers: []ReviewerConfig{{Harness: ReviewerKiro}}}, false},
		{"good pi reviewer", ProjectConfig{Reviewers: []ReviewerConfig{{Harness: ReviewerPi}}}, false},
		{"good experimental agy reviewer", ProjectConfig{Reviewers: []ReviewerConfig{{Harness: ReviewerAgy}}}, false},
		{"unsupported continue reviewer", ProjectConfig{Reviewers: []ReviewerConfig{{Harness: "continue"}}}, true},
		{"unsupported goose reviewer", ProjectConfig{Reviewers: []ReviewerConfig{{Harness: "goose"}}}, true},
		{"unsupported vibe reviewer", ProjectConfig{Reviewers: []ReviewerConfig{{Harness: "vibe"}}}, true},
		{"good experimental Devin reviewer", ProjectConfig{Reviewers: []ReviewerConfig{{Harness: ReviewerDevin}}}, false},
		{"good experimental Droid reviewer", ProjectConfig{Reviewers: []ReviewerConfig{{Harness: ReviewerDroid}}}, false},
		{"good experimental Kimi reviewer", ProjectConfig{Reviewers: []ReviewerConfig{{Harness: ReviewerKimi}}}, false},
		{"good Muse reviewer", ProjectConfig{Reviewers: []ReviewerConfig{{Harness: ReviewerMuse}}}, false},
		{"unknown reviewer harness", ProjectConfig{Reviewers: []ReviewerConfig{{Harness: "nope"}}}, true},
		{"good interactive Amp reviewer", ProjectConfig{Reviewers: []ReviewerConfig{{Harness: ReviewerAmp}}}, false},
		{"good interactive Aider reviewer", ProjectConfig{Reviewers: []ReviewerConfig{{Harness: ReviewerAider}}}, false},
		{"good experimental Grok reviewer", ProjectConfig{Reviewers: []ReviewerConfig{{Harness: ReviewerGrok}}}, false},
		{"good experimental Crush reviewer", ProjectConfig{Reviewers: []ReviewerConfig{{Harness: ReviewerCrush}}}, false},
		{"good experimental Auggie reviewer", ProjectConfig{Reviewers: []ReviewerConfig{{Harness: ReviewerAuggie}}}, false},
		{"good experimental Cline reviewer", ProjectConfig{Reviewers: []ReviewerConfig{{Harness: ReviewerCline}}}, false},
		{"good experimental Autohand reviewer", ProjectConfig{Reviewers: []ReviewerConfig{{Harness: ReviewerAutohand}}}, false},
		{"empty reviewer harness", ProjectConfig{Reviewers: []ReviewerConfig{{Harness: ""}}}, true},
		{"tracker intake assignee rule", ProjectConfig{TrackerIntake: TrackerIntakeConfig{Enabled: true, Assignee: "alice"}}, false},
		{"tracker intake explicit github", ProjectConfig{TrackerIntake: TrackerIntakeConfig{Enabled: true, Provider: TrackerProviderGitHub, Assignee: "alice"}}, false},
		{"tracker intake no rule", ProjectConfig{TrackerIntake: TrackerIntakeConfig{Enabled: true}}, true},
		{"tracker intake unknown provider", ProjectConfig{TrackerIntake: TrackerIntakeConfig{Enabled: true, Provider: "linear", Assignee: "alice"}}, true},
		{"tracker intake repo with whitespace", ProjectConfig{TrackerIntake: TrackerIntakeConfig{Enabled: true, Repo: " acme/demo", Assignee: "alice"}}, true},
		{"tracker intake assignee with whitespace", ProjectConfig{TrackerIntake: TrackerIntakeConfig{Enabled: true, Assignee: " alice"}}, true},
		{"auto review enabled", ProjectConfig{AutoReview: true}, false},
		{"auto review disabled", ProjectConfig{AutoReview: false}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.cfg.Validate(); (err != nil) != tt.wantErr {
				t.Fatalf("Validate() err = %v, wantErr = %v", err, tt.wantErr)
			}
		})
	}
}

func TestDefaultProjectConfig(t *testing.T) {
	def := DefaultProjectConfig()

	// The one documented non-empty default.
	if def.DefaultBranch != DefaultBranchAuto {
		t.Fatalf("default DefaultBranch = %q, want %q", def.DefaultBranch, DefaultBranchAuto)
	}

	// Every other field defaults to its zero value: clearing the documented
	// default must leave the config completely empty.
	def.DefaultBranch = ""
	if !def.IsZero() {
		t.Fatalf("default config has unexpected non-zero fields: %#v", def)
	}
}

func TestProjectConfigWithDefaults(t *testing.T) {
	// An unset config gets the documented defaults.
	got := (ProjectConfig{}).WithDefaults()
	if got.DefaultBranch != DefaultBranchAuto {
		t.Fatalf("WithDefaults = %#v, want branch=%s", got, DefaultBranchAuto)
	}

	// Set fields are preserved, not overwritten.
	got = (ProjectConfig{
		DefaultBranch: "develop",
		AgentConfig:   AgentConfig{Model: "m"},
	}).WithDefaults()
	if got.DefaultBranch != "develop" {
		t.Fatalf("WithDefaults overwrote set fields: %#v", got)
	}
	if got.AgentConfig.Model != "m" {
		t.Fatalf("WithDefaults dropped a set field: %#v", got.AgentConfig)
	}
	if got.WorktreeBaseBranch() != "develop" {
		t.Fatalf("WorktreeBaseBranch = %q, want develop", got.WorktreeBaseBranch())
	}
	if got := (ProjectConfig{}).WorktreeBaseBranch(); got != "" {
		t.Fatalf("automatic WorktreeBaseBranch = %q, want empty for adapter inference", got)
	}
	if got := (ProjectConfig{DefaultBranch: DefaultBranchAuto}).WorktreeBaseBranch(); got != "" {
		t.Fatalf("explicit auto WorktreeBaseBranch = %q, want empty for adapter inference", got)
	}

	got = (ProjectConfig{TrackerIntake: TrackerIntakeConfig{Enabled: true, Assignee: "alice"}}).WithDefaults()
	if got.TrackerIntake.Provider != "" {
		t.Fatalf("TrackerIntake.Provider = %q, want empty (inferred at use time)", got.TrackerIntake.Provider)
	}

	got = (ProjectConfig{}).WithDefaults()
	if got.TrackerIntake.Provider != "" {
		t.Fatalf("disabled TrackerIntake.Provider = %q, want empty", got.TrackerIntake.Provider)
	}
}

func TestInferTrackerProvider(t *testing.T) {
	tests := []struct {
		name    string
		repoURL string
		want    TrackerProvider
	}{
		{"empty", "", TrackerProviderGitHub},
		{"https github", "https://github.com/acme/demo.git", TrackerProviderGitHub},
		{"ssh github", "git@github.com:acme/demo.git", TrackerProviderGitHub},
		{"ghe host", "https://ghe.corp.ghe.io/acme/demo.git", TrackerProviderGitHub},
		{"github with port", "https://github.com:443/org/repo.git", TrackerProviderGitHub},
		{"ssh github with port", "ssh://git@github.com:2222/org/repo.git", TrackerProviderGitHub},
		{"https gitlab.com", "https://gitlab.com/group/repo.git", TrackerProviderGitLab},
		{"ssh gitlab.com", "git@gitlab.com:group/repo.git", TrackerProviderGitLab},
		{"self-managed gitlab", "https://gitlab.internal/group/repo.git", TrackerProviderGitLab},
		{"ssh self-managed", "git@gitlab.internal:group/repo.git", TrackerProviderGitLab},
		{"self-managed with port", "https://gitlab.local:8443/group/repo.git", TrackerProviderGitLab},
		{"non-gitlab custom host", "https://dev.company.com/group/repo.git", TrackerProviderGitLab},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := InferTrackerProvider(tt.repoURL)
			if got != tt.want {
				t.Errorf("InferTrackerProvider(%q) = %q, want %q", tt.repoURL, got, tt.want)
			}
		})
	}
}

func TestResolveReviewerHarness(t *testing.T) {
	// A configured reviewer always wins, regardless of the worker harness.
	cfg := ProjectConfig{Reviewers: []ReviewerConfig{{Harness: ReviewerClaudeCode}}}
	if got := cfg.ResolveReviewerHarness(HarnessAider); got != ReviewerClaudeCode {
		t.Fatalf("configured reviewer = %q, want claude-code", got)
	}

	// No reviewer configured: preserve automatic inheritance only for the
	// original unattended-safe reviewer set.
	if got := (ProjectConfig{}).ResolveReviewerHarness(HarnessClaudeCode); got != ReviewerClaudeCode {
		t.Fatalf("claude-code worker = %q, want reviewer claude-code", got)
	}
	if got := (ProjectConfig{}).ResolveReviewerHarness(HarnessCodex); got != ReviewerCodex {
		t.Fatalf("codex worker = %q, want reviewer codex", got)
	}
	if got := (ProjectConfig{}).ResolveReviewerHarness(HarnessOpenCode); got != ReviewerOpenCode {
		t.Fatalf("opencode worker = %q, want reviewer opencode", got)
	}
	if got := (ProjectConfig{}).ResolveReviewerHarness(HarnessMuse); got != ReviewerMuse {
		t.Fatalf("muse worker = %q, want reviewer muse", got)
	}
	if got := (ProjectConfig{}).ResolveReviewerHarness(HarnessKimchi); got != ReviewerKimchi {
		t.Fatalf("kimchi worker = %q, want reviewer kimchi", got)
	}

	// A worker harness that is not itself a reviewer (e.g. crush, aider) falls
	// back to claude-code.
	if got := (ProjectConfig{}).ResolveReviewerHarness(HarnessCrush); got != FallbackReviewerHarness {
		t.Fatalf("crush worker = %q, want %q", got, FallbackReviewerHarness)
	}
	for _, worker := range []AgentHarness{
		HarnessCopilot, HarnessCursor, HarnessKilocode, HarnessKiro, HarnessPi,
		HarnessAider, HarnessAmp, HarnessQwen, HarnessAgy, HarnessContinue,
		HarnessGoose, HarnessVibe, HarnessDevin, HarnessDroid, HarnessKimi,
		HarnessGrok, HarnessCrush, HarnessAuggie, HarnessCline, HarnessAutohand,
	} {
		if got := (ProjectConfig{}).ResolveReviewerHarness(worker); got != FallbackReviewerHarness {
			t.Errorf("%s worker = %q, want explicit-selection fallback %q", worker, got, FallbackReviewerHarness)
		}
	}
}

func TestProjectConfigIsZero(t *testing.T) {
	if !(ProjectConfig{}).IsZero() {
		t.Fatal("empty config should be zero")
	}
	if (ProjectConfig{DefaultBranch: "main"}).IsZero() {
		t.Fatal("populated config should not be zero")
	}
	if (ProjectConfig{Env: map[string]string{"A": "b"}}).IsZero() {
		t.Fatal("config with env should not be zero")
	}
	if (ProjectConfig{AutoReview: true}).IsZero() {
		t.Fatal("config with autoReview enabled should not be zero")
	}
}

func TestProjectConfigWithProfile(t *testing.T) {
	cfg := ProjectConfig{
		Env:    map[string]string{"KEEP": "project", "SHARED": "project"},
		Worker: RoleOverride{Harness: HarnessCodex, AgentConfig: AgentConfig{Model: "slot-model", Mode: "low", Permissions: PermissionModeAuto}, Profile: "default-profile"},
		Profiles: map[string]RoleProfile{
			"default-profile": {Harness: HarnessAgy, AgentConfig: AgentConfig{Model: "gemini-3.8-flash-high"}, RulesFile: "rules/flash.md", Env: map[string]string{"SHARED": "profile", "ONLY": "profile"}},
			"pro-expert":      {AgentConfig: AgentConfig{Model: "gemini-3.1-pro-high", Permissions: PermissionModeBypassPermissions}},
		},
	}
	// The role override's profile is the default; an explicit name wins.
	if got := cfg.ResolveProfileName(KindWorker, ""); got != "default-profile" {
		t.Fatalf("resolved = %q, want the worker override's profile", got)
	}
	if got := cfg.ResolveProfileName(KindWorker, " pro-expert "); got != "pro-expert" {
		t.Fatalf("resolved = %q, want the explicit profile", got)
	}
	if got := cfg.ResolveProfileName(KindOrchestrator, ""); got != "" {
		t.Fatalf("orchestrator resolved = %q, want none", got)
	}
	// Folding: the profile's set fields win over the override, unset fields keep it;
	// env merges with the profile's keys winning; the original config is untouched.
	folded, err := cfg.WithProfile(KindWorker, "default-profile")
	if err != nil {
		t.Fatal(err)
	}
	if folded.Worker.Harness != HarnessAgy || folded.Worker.AgentConfig.Model != "gemini-3.8-flash-high" || folded.Worker.AgentConfig.Mode != "low" || folded.Worker.AgentConfig.Permissions != PermissionModeAuto || folded.Worker.Profile != "default-profile" {
		t.Fatalf("folded worker = %#v", folded.Worker)
	}
	if folded.Env["KEEP"] != "project" || folded.Env["SHARED"] != "profile" || folded.Env["ONLY"] != "profile" {
		t.Fatalf("folded env = %#v", folded.Env)
	}
	if cfg.Env["SHARED"] != "project" || cfg.Worker.Harness != HarnessCodex {
		t.Fatalf("WithProfile mutated the receiver: %#v", cfg)
	}
	if folded.ProfileRulesFile("default-profile") != "rules/flash.md" || folded.ProfileRulesFile("nope") != "" {
		t.Fatalf("ProfileRulesFile = %q / %q", folded.ProfileRulesFile("default-profile"), folded.ProfileRulesFile("nope"))
	}
	// A profile without a harness keeps the override's; its permissions still apply.
	pro, err := cfg.WithProfile(KindWorker, "pro-expert")
	if err != nil {
		t.Fatal(err)
	}
	if pro.Worker.Harness != HarnessCodex || pro.Worker.AgentConfig.Model != "gemini-3.1-pro-high" || pro.Worker.AgentConfig.Permissions != PermissionModeBypassPermissions {
		t.Fatalf("pro worker = %#v", pro.Worker)
	}
	// The orchestrator slot is untouched by a worker fold, and vice versa.
	if pro.Orchestrator != (RoleOverride{}) {
		t.Fatalf("orchestrator changed: %#v", pro.Orchestrator)
	}
	// An empty name is a no-op; an unknown name is an error.
	if same, err := cfg.WithProfile(KindWorker, ""); err != nil || !reflect.DeepEqual(same, cfg) {
		t.Fatalf("empty name: %#v %v", same, err)
	}
	if _, err := cfg.WithProfile(KindWorker, "missing"); err == nil {
		t.Fatal("unknown profile must be refused")
	}
}

func TestProjectConfigApplyTemplate(t *testing.T) {
	cfg := ProjectConfig{
		Worker:       RoleOverride{Harness: HarnessCodex, Profile: "old"},
		Orchestrator: RoleOverride{Profile: "old"},
		Reviewers:    []ReviewerConfig{{Harness: ReviewerCursor}},
		Profiles: map[string]RoleProfile{
			"old":          {Harness: HarnessCodex},
			"orchestrator": {Harness: HarnessClaudeCode, AgentConfig: AgentConfig{Model: "opus"}},
			"flash-coder":  {Harness: HarnessAgy, AgentConfig: AgentConfig{Model: "gemini-3.8-flash-high"}},
			"pro-expert":   {Harness: HarnessAgy, AgentConfig: AgentConfig{Model: "gemini-3.1-pro-high", Permissions: PermissionModeBypassPermissions}},
			"no-agent":     {AgentConfig: AgentConfig{Model: "m"}},
		},
		Templates: map[string]WorkflowTemplate{
			"flash-first": {Orchestrator: "orchestrator", Worker: "flash-coder", Reviewers: []string{"pro-expert"}, OrchestratorRulesFile: "rules/plan-flash-first.md"},
			"bare":        {},
		},
	}
	applied, err := cfg.ApplyTemplate("flash-first")
	if err != nil {
		t.Fatal(err)
	}
	// Slots take the template's profile names; the worker's inline harness stays.
	if applied.Worker.Profile != "flash-coder" || applied.Worker.Harness != HarnessCodex || applied.Orchestrator.Profile != "orchestrator" {
		t.Fatalf("slots = %#v / %#v", applied.Worker, applied.Orchestrator)
	}
	// Reviewers are replaced by the template's, with a snapshot of the profile.
	want := []ReviewerConfig{{Harness: ReviewerAgy, AgentConfig: AgentConfig{Model: "gemini-3.1-pro-high", Permissions: PermissionModeBypassPermissions}, Profile: "pro-expert"}}
	if !reflect.DeepEqual(applied.Reviewers, want) {
		t.Fatalf("reviewers = %#v", applied.Reviewers)
	}
	if applied.Template != "flash-first" || applied.TemplateRulesFile() != "rules/plan-flash-first.md" {
		t.Fatalf("active = %q plan = %q", applied.Template, applied.TemplateRulesFile())
	}
	if err := applied.Validate(); err != nil {
		t.Fatalf("applied config must validate: %v", err)
	}
	// Profiles are never changed, and the receiver is untouched.
	if !reflect.DeepEqual(applied.Profiles, cfg.Profiles) || cfg.Worker.Profile != "old" || cfg.Template != "" || len(cfg.Reviewers) != 1 {
		t.Fatalf("ApplyTemplate mutated profiles or the receiver: %#v", cfg)
	}
	// An empty template unbinds every slot and clears the reviewers.
	bare, err := cfg.ApplyTemplate("bare")
	if err != nil {
		t.Fatal(err)
	}
	if bare.Worker.Profile != "" || bare.Orchestrator.Profile != "" || bare.Reviewers != nil || bare.Template != "bare" || bare.TemplateRulesFile() != "" {
		t.Fatalf("bare = %#v", bare)
	}
	// Unknown templates, and templates naming unknown or agent-less reviewer
	// profiles (which Validate refuses when set), are errors rather than partial binds.
	broken := cfg
	broken.Templates = map[string]WorkflowTemplate{"bad-worker": {Worker: "missing"}, "bad-review": {Reviewers: []string{"no-agent"}}}
	for _, name := range []string{"missing", "bad-worker", "bad-review", ""} {
		if _, err := broken.ApplyTemplate(name); err == nil {
			t.Fatalf("template %q must be refused", name)
		}
	}
	if cfg.TemplateRulesFile() != "" {
		t.Fatalf("no active template must give no plan file, got %q", cfg.TemplateRulesFile())
	}
}

func TestProjectConfigResolveReviewers(t *testing.T) {
	cfg := ProjectConfig{
		Profiles: map[string]RoleProfile{
			"pro-expert": {Harness: HarnessAgy, AgentConfig: AgentConfig{Model: "gemini-3.1-pro-high"}},
			"headless":   {AgentConfig: AgentConfig{Permissions: PermissionModeBypassPermissions}},
		},
		Reviewers: []ReviewerConfig{
			// A stale snapshot: the profile's model has moved on since the template was applied.
			{Harness: ReviewerCodex, AgentConfig: AgentConfig{Model: "old", Mode: "low"}, Profile: "pro-expert"},
			// A profile without a harness keeps the inline harness and adds its fields.
			{Harness: ReviewerCursor, Profile: "headless"},
			// No profile: returned as is.
			{Harness: ReviewerClaudeCode, AgentConfig: AgentConfig{Model: "opus"}},
			// Unknown profile (refused by Validate): returned as is rather than dropped.
			{Harness: ReviewerKiro, Profile: "gone"},
		},
	}
	got := cfg.ResolveReviewers()
	want := []ReviewerConfig{
		{Harness: ReviewerAgy, AgentConfig: AgentConfig{Model: "gemini-3.1-pro-high", Mode: "low"}, Profile: "pro-expert"},
		{Harness: ReviewerCursor, AgentConfig: AgentConfig{Permissions: PermissionModeBypassPermissions}, Profile: "headless"},
		{Harness: ReviewerClaudeCode, AgentConfig: AgentConfig{Model: "opus"}},
		{Harness: ReviewerKiro, Profile: "gone"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("resolved = %#v", got)
	}
	if cfg.Reviewers[0].Harness != ReviewerCodex {
		t.Fatal("ResolveReviewers mutated the receiver")
	}
	if (ProjectConfig{}).ResolveReviewers() != nil {
		t.Fatal("no reviewers must resolve to nil")
	}
}
