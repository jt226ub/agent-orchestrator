package domain

import (
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
)

// ProjectConfig is the typed per-project configuration — the SQLite twin of the
// legacy agent-orchestrator.yaml `projects.<id>` block. It is persisted as one
// JSON blob per project and resolved at spawn. Each field is typed and
// validated; there is no free-form map.
//
// Only fields with a live consumer are modeled: DefaultBranch, Env, Symlinks,
// PostCreate, AgentConfig, prompt rules, and the role overrides are consumed at
// spawn; SessionPrefix feeds the display prefix. Settings whose consumers do not
// yet exist (tracker/SCM per-project config) are intentionally absent and land in
// focused follow-up PRs alongside the code that reads them.
type ProjectConfig struct {
	// CanonicalRepoURL explicitly trusts one upstream repository for PR claims.
	// Numeric PR references use this repository when set; checkout/push stays on origin.
	CanonicalRepoURL string `json:"canonicalRepoURL,omitempty"`
	// DefaultBranch is the base branch new session worktrees are created from.
	// Empty and DefaultBranchAuto both mean infer each repository's Git default.
	DefaultBranch string `json:"defaultBranch,omitempty"`
	// SessionPrefix overrides the displayed session-id prefix.
	SessionPrefix string `json:"sessionPrefix,omitempty"`

	// Env are extra environment variables forwarded into worker session
	// runtimes. AO-internal vars (AO_SESSION, AO_PROJECT_ID, …) always win.
	Env map[string]string `json:"env,omitempty"`
	// Symlinks are repo-relative paths symlinked into each session workspace.
	Symlinks []string `json:"symlinks,omitempty"`
	// PostCreate are shell commands run in the workspace after it is created.
	PostCreate []string `json:"postCreate,omitempty"`

	// AgentRules are project-specific standing instructions for worker sessions.
	AgentRules string `json:"agentRules,omitempty"`
	// AgentRulesFile is a repo-relative Markdown/text file whose contents are
	// appended to AgentRules for worker sessions.
	AgentRulesFile string `json:"agentRulesFile,omitempty"`
	// OrchestratorRules are project-specific standing instructions for
	// orchestrator sessions.
	OrchestratorRules string `json:"orchestratorRules,omitempty"`

	// AgentConfig is the default agent config for the project.
	AgentConfig AgentConfig `json:"agentConfig,omitempty"`
	// Worker and Orchestrator are role-specific harness/agent-config overrides.
	Worker       RoleOverride `json:"worker,omitempty"`
	Orchestrator RoleOverride `json:"orchestrator,omitempty"`
	// Profiles are named bundles of harness, agent config, standing rules and
	// environment that a role override or a single spawn can name instead of
	// repeating them (`ao spawn --profile NAME`). A profile is resolved at spawn
	// and folded into the role override; the session remembers its name so a
	// restore reapplies the same bundle.
	Profiles map[string]RoleProfile `json:"profiles,omitempty"`
	// Templates are named assignments of profiles to the role slots (worker,
	// orchestrator, reviewers) plus the orchestrator's delegation plan.
	// ApplyTemplate binds one in a single step; a template never changes a profile.
	Templates map[string]WorkflowTemplate `json:"templates,omitempty"`
	// Template is the name of the template last applied, so the desktop and the
	// orchestrator prompt know which plan is in force. Editing a slot by hand
	// leaves the name in place; reapplying rebinds the slots.
	Template string `json:"template,omitempty"`

	// Reviewers names the agent(s) that review a worker's PR when a review is
	// triggered. It is configured independently of the Worker override; an empty
	// list falls back to claude-code (see ResolveReviewerHarness).
	Reviewers []ReviewerConfig `json:"reviewers,omitempty"`
	// TrackerIntake controls issue-driven worker spawning. It is opt-in and
	// read-only toward the tracker in v1: matching issues spawn sessions, but the
	// tracker is not commented on or transitioned.
	TrackerIntake TrackerIntakeConfig `json:"trackerIntake,omitempty"`

	// ContainerReap controls whether AO reaps a worker session's ao.session-
	// labeled Docker containers on terminal state / kill. Enabled by default;
	// set Disabled to opt a project out entirely. Per-container sparing uses
	// the ao.spare=true label instead (see dockerreap.SpareLabel) so the
	// opt-out travels with the container at `docker run` time rather than
	// drifting out of sync with a project-config list.
	ContainerReap ContainerReapConfig `json:"containerReap,omitempty"`

	// AutoReview controls whether new worker sessions spawned for this project
	// have automatic PR review enabled by default. The default (false) leaves
	// sessions with auto-review off; enabling it copies the setting into each
	// new session at spawn time. Users can still override the per-session toggle
	// after spawn.
	AutoReview bool `json:"autoReview,omitempty"`
}

// ContainerReapConfig is the project-level opt-out for #2652's Docker
// container reaping on session terminal state.
type ContainerReapConfig struct {
	// Disabled turns off container reaping for every session in this project.
	// Per-container sparing (ao.spare=true) is unaffected either way.
	Disabled bool `json:"disabled,omitempty"`
}

// ReviewerConfig names one reviewer agent by harness. The harness is drawn from
// the reviewer vocabulary (ReviewerHarness), which is distinct from the worker
// AgentHarness set.
type ReviewerConfig struct {
	Harness     ReviewerHarness `json:"harness"`
	AgentConfig AgentConfig     `json:"agentConfig,omitempty"`
	// Profile names a ProjectConfig.Profiles entry the reviewer follows: at
	// review time the profile's harness and agent config win over the inline
	// fields, which ApplyTemplate fills as a snapshot for display.
	Profile string `json:"profile,omitempty"`
}

// FallbackReviewerHarness is the reviewer used when a project configures none
// and the worker's harness is not itself a supported reviewer.
const FallbackReviewerHarness = ReviewerClaudeCode

// ResolveReviewers returns the reviewer list with each profile-bound entry
// resolved against the current profile (the profile's set fields win over the
// inline snapshot), so a profile edit reaches the next review without
// reapplying the template. Unknown profile names keep the snapshot; Validate
// refuses them when the config is set.
func (c ProjectConfig) ResolveReviewers() []ReviewerConfig {
	if len(c.Reviewers) == 0 {
		return nil
	}
	out := make([]ReviewerConfig, len(c.Reviewers))
	for i, rv := range c.Reviewers {
		out[i] = rv.resolved(c.Profiles)
	}
	return out
}

func (rv ReviewerConfig) resolved(profiles map[string]RoleProfile) ReviewerConfig {
	profile, ok := profiles[strings.TrimSpace(rv.Profile)]
	if !ok {
		return rv
	}
	if profile.Harness != "" {
		rv.Harness = ReviewerHarness(profile.Harness)
	}
	rv.AgentConfig = mergeAgentConfig(rv.AgentConfig, profile.AgentConfig)
	return rv
}

// mergeAgentConfig returns base with override's set fields applied.
func mergeAgentConfig(base, override AgentConfig) AgentConfig {
	if override.Model != "" {
		base.Model = override.Model
	}
	if override.Effort != "" {
		base.Effort = override.Effort
	}
	if override.Mode != "" {
		base.Mode = override.Mode
	}
	if override.Permissions != "" {
		base.Permissions = override.Permissions
	}
	return base
}

// ResolveReviewerHarness picks the reviewer harness for a worker. A configured
// reviewer wins. Otherwise only the original, unattended-safe reviewer set is
// inherited from the worker. Every other reviewer requires explicit selection,
// so adding an experimental adapter never silently changes an existing project.
func (c ProjectConfig) ResolveReviewerHarness(worker AgentHarness) ReviewerHarness {
	if len(c.Reviewers) > 0 {
		return c.Reviewers[0].Harness
	}
	switch worker {
	case HarnessClaudeCode:
		return ReviewerClaudeCode
	case HarnessCodex:
		return ReviewerCodex
	case HarnessOpenCode:
		return ReviewerOpenCode
	case HarnessMuse:
		return ReviewerMuse
	case HarnessKimchi:
		return ReviewerKimchi
	}
	return FallbackReviewerHarness
}

// RoleOverride overrides the harness and/or agent config for a session role.
// Profile names a ProjectConfig.Profiles entry whose settings fold into this
// override at spawn; the profile's set fields win over the inline ones.
type RoleOverride struct {
	Harness     AgentHarness `json:"agent,omitempty"`
	AgentConfig AgentConfig  `json:"agentConfig,omitempty"`
	Profile     string       `json:"profile,omitempty"`
}

// RoleProfile is one named bundle in ProjectConfig.Profiles.
type RoleProfile struct {
	Harness     AgentHarness `json:"agent,omitempty"`
	AgentConfig AgentConfig  `json:"agentConfig,omitempty"`
	// RulesFile is a repo-relative Markdown/text file appended to the session's
	// standing rules after the project's own, so the role's instructions are
	// the most specific text the agent reads.
	RulesFile string `json:"rulesFile,omitempty"`
	// Env are extra environment variables for sessions on this profile; a key
	// set here wins over the project's Env, and AO-internal vars still win over both.
	Env map[string]string `json:"env,omitempty"`
	// Quota gates spawns on the harness's plan capacity (today: Antigravity's
	// shared Gemini meter). Nil means capacity is advisory for this profile.
	Quota *ProfileQuota `json:"quota,omitempty"`
	// Fallback names the profile a spawn uses instead when Quota refuses this
	// one, so a plan that is out of capacity hands the task to another arm.
	Fallback *ProfileFallback `json:"fallback,omitempty"`
}

// ProfileQuota is a profile's admission policy against the fresh capacity
// snapshot: at or below RefuseBelowPercent remaining the spawn is refused (or
// handed to the Fallback profile); at or below WarnBelowPercent it proceeds
// with a warning. Zero WarnBelowPercent never warns; zero RefuseBelowPercent
// refuses only an exhausted plan.
type ProfileQuota struct {
	WarnBelowPercent   float64 `json:"warnBelowPercent,omitempty" minimum:"0" maximum:"100"`
	RefuseBelowPercent float64 `json:"refuseBelowPercent,omitempty" minimum:"0" maximum:"100"`
}

// ProfileFallback names the profile that takes a spawn this profile's Quota
// refuses. One hop: the fallback's own Quota is checked, but not its fallback.
type ProfileFallback struct {
	Profile string `json:"profile,omitempty"`
}

// ResolveProfileName picks the profile a spawn of the given kind uses: the
// explicit name when the caller passed one, else the role override's.
func (c ProjectConfig) ResolveProfileName(kind SessionKind, explicit string) string {
	if name := strings.TrimSpace(explicit); name != "" {
		return name
	}
	if kind == KindOrchestrator {
		return strings.TrimSpace(c.Orchestrator.Profile)
	}
	return strings.TrimSpace(c.Worker.Profile)
}

// WithProfile folds the named profile into the config for one session role:
// the role override takes the profile's harness and agent-config fields (the
// profile's set fields win, unset fields keep the override's), Env takes the
// profile's keys, and the profile's RulesFile is exposed for prompt assembly.
// An empty name returns c unchanged; an unknown name is an error so a spawn
// fails before any durable state exists.
func (c ProjectConfig) WithProfile(kind SessionKind, name string) (ProjectConfig, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return c, nil
	}
	profile, ok := c.Profiles[name]
	if !ok {
		return c, fmt.Errorf("profile %q is not defined in the project config", name)
	}
	role := c.Worker
	if kind == KindOrchestrator {
		role = c.Orchestrator
	}
	if profile.Harness != "" {
		role.Harness = profile.Harness
	}
	role.AgentConfig = mergeAgentConfig(role.AgentConfig, profile.AgentConfig)
	role.Profile = name
	if kind == KindOrchestrator {
		c.Orchestrator = role
	} else {
		c.Worker = role
	}
	if len(profile.Env) > 0 {
		env := make(map[string]string, len(c.Env)+len(profile.Env))
		for k, v := range c.Env {
			env[k] = v
		}
		for k, v := range profile.Env {
			env[k] = v
		}
		c.Env = env
	}
	return c, nil
}

// ProfileRulesFile returns the named profile's rules file, or "" when the name
// is empty or unknown (WithProfile has already refused unknown names at spawn).
func (c ProjectConfig) ProfileRulesFile(name string) string {
	if profile, ok := c.Profiles[strings.TrimSpace(name)]; ok {
		return strings.TrimSpace(profile.RulesFile)
	}
	return ""
}

// WorkflowTemplate is one named entry in ProjectConfig.Templates: which profile
// fills each role slot, and the orchestrator's delegation plan. Every field is
// optional; an empty slot unbinds that role's profile when the template is
// applied, so a template always describes the whole assignment.
type WorkflowTemplate struct {
	Orchestrator string   `json:"orchestrator,omitempty"`
	Worker       string   `json:"worker,omitempty"`
	Reviewers    []string `json:"reviewers,omitempty"`
	// OrchestratorRulesFile is a repo-relative file appended last to the
	// orchestrator's standing rules while the template is active: the plan that
	// says which profile takes which kind of task.
	OrchestratorRulesFile string `json:"orchestratorRulesFile,omitempty"`
}

// ApplyTemplate binds the named template's profiles to the role slots: the
// worker and orchestrator overrides take the template's profile names (an
// empty name unbinds the slot's profile and keeps its inline fields), the
// reviewer list is replaced by the template's reviewer profiles (harness and
// agent config copied as a display snapshot, resolved live by
// ResolveReviewers), and Template records the name. Profiles are not changed.
// An unknown template, or a template naming an unknown profile, is an error.
func (c ProjectConfig) ApplyTemplate(name string) (ProjectConfig, error) {
	name = strings.TrimSpace(name)
	tpl, ok := c.Templates[name]
	if !ok {
		return c, fmt.Errorf("template %q is not defined in the project config", name)
	}
	for _, slot := range []struct{ role, profile string }{{"worker", tpl.Worker}, {"orchestrator", tpl.Orchestrator}} {
		if p := strings.TrimSpace(slot.profile); p != "" {
			if _, ok := c.Profiles[p]; !ok {
				return c, fmt.Errorf("template %q: %s names unknown profile %q", name, slot.role, p)
			}
		}
	}
	reviewers := make([]ReviewerConfig, 0, len(tpl.Reviewers))
	for _, p := range tpl.Reviewers {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		profile, ok := c.Profiles[p]
		if !ok {
			return c, fmt.Errorf("template %q: reviewers names unknown profile %q", name, p)
		}
		if profile.Harness == "" {
			return c, fmt.Errorf("template %q: reviewer profile %q sets no agent", name, p)
		}
		reviewers = append(reviewers, ReviewerConfig{Harness: ReviewerHarness(profile.Harness), AgentConfig: profile.AgentConfig, Profile: p})
	}
	c.Worker.Profile = strings.TrimSpace(tpl.Worker)
	c.Orchestrator.Profile = strings.TrimSpace(tpl.Orchestrator)
	if len(reviewers) == 0 {
		reviewers = nil
	}
	c.Reviewers = reviewers
	c.Template = name
	return c, nil
}

// TemplateRulesFile returns the active template's orchestrator plan file, or
// "" when no template is active or it sets none.
func (c ProjectConfig) TemplateRulesFile() string {
	if tpl, ok := c.Templates[strings.TrimSpace(c.Template)]; ok {
		return strings.TrimSpace(tpl.OrchestratorRulesFile)
	}
	return ""
}

const (
	// DefaultBranchAuto tells callers to infer the Git default branch for each
	// repository instead of naming one branch for the whole project.
	DefaultBranchAuto = "auto"
	// DefaultBranchName is the branch AO selects when it creates a repository.
	// Automatic resolution never uses it as a guess for existing repositories.
	DefaultBranchName = "main"
)

// DefaultProjectConfig returns the config a project has when it sets nothing:
// automatic per-repository branch resolution. Every other field defaults to
// its zero value (no env/symlinks/post-create, agent + role defaults).
func DefaultProjectConfig() ProjectConfig {
	return ProjectConfig{
		DefaultBranch: DefaultBranchAuto,
	}
}

// WithDefaults overlays DefaultProjectConfig onto c, filling only fields the
// project left unset. A set field is always preserved.
func (c ProjectConfig) WithDefaults() ProjectConfig {
	def := DefaultProjectConfig()
	if c.DefaultBranch == "" {
		c.DefaultBranch = def.DefaultBranch
	}
	c.TrackerIntake = c.TrackerIntake.WithDefaults()
	return c
}

// WorktreeBaseBranch translates project configuration into the workspace
// interface. An empty value tells the workspace adapter to resolve a remote
// HEAD independently for the repository it is materializing.
func (c ProjectConfig) WorktreeBaseBranch() string {
	branch := c.WithDefaults().DefaultBranch
	if branch == DefaultBranchAuto {
		return ""
	}
	return branch
}

// IsZero reports whether the config carries no settings, so storage can persist
// SQL NULL and resolution can skip an empty config.
func (c ProjectConfig) IsZero() bool {
	return reflect.DeepEqual(c, ProjectConfig{})
}

// Validate rejects values outside the typed vocabulary so a bad config is
// refused when it is set (CLI/API) rather than surfacing at spawn.
func (c ProjectConfig) Validate() error {
	if c.CanonicalRepoURL != "" {
		if err := c.ValidateCanonicalRepository(c.CanonicalRepoURL); err != nil {
			return err
		}
	}
	if err := c.AgentConfig.Validate(); err != nil {
		return err
	}
	if err := validateNameComponent("sessionPrefix", c.SessionPrefix); err != nil {
		return err
	}
	for role, ro := range map[string]RoleOverride{"worker": c.Worker, "orchestrator": c.Orchestrator} {
		if ro.Harness != "" && !ro.Harness.IsKnown() {
			return fmt.Errorf("%s.agent: unknown harness %q", role, ro.Harness)
		}
		if err := ro.AgentConfig.Validate(); err != nil {
			return fmt.Errorf("%s.%w", role, err)
		}
		if name := strings.TrimSpace(ro.Profile); name != "" {
			if _, ok := c.Profiles[name]; !ok {
				return fmt.Errorf("%s.profile: unknown profile %q", role, name)
			}
		}
	}
	for name, profile := range c.Profiles {
		if err := validateNameComponent("profiles."+name, name); err != nil {
			return err
		}
		if strings.TrimSpace(name) == "" || strings.TrimSpace(name) != name {
			return fmt.Errorf("profiles: name %q must be non-empty without surrounding whitespace", name)
		}
		if profile.Harness != "" && !profile.Harness.IsKnown() {
			return fmt.Errorf("profiles.%s.agent: unknown harness %q", name, profile.Harness)
		}
		if err := profile.AgentConfig.Validate(); err != nil {
			return fmt.Errorf("profiles.%s.%w", name, err)
		}
		if err := validateRepoRelative(profile.RulesFile); err != nil {
			return fmt.Errorf("profiles.%s.rulesFile %q: %w", name, profile.RulesFile, err)
		}
		if q := profile.Quota; q != nil {
			for field, value := range map[string]float64{"warnBelowPercent": q.WarnBelowPercent, "refuseBelowPercent": q.RefuseBelowPercent} {
				if value < 0 || value > 100 {
					return fmt.Errorf("profiles.%s.quota.%s: %v is not between 0 and 100", name, field, value)
				}
			}
			if q.WarnBelowPercent > 0 && q.RefuseBelowPercent > q.WarnBelowPercent {
				return fmt.Errorf("profiles.%s.quota: refuseBelowPercent %v is above warnBelowPercent %v", name, q.RefuseBelowPercent, q.WarnBelowPercent)
			}
		}
		if fb := profile.Fallback; fb != nil && strings.TrimSpace(fb.Profile) != "" {
			target := strings.TrimSpace(fb.Profile)
			if profile.Quota == nil {
				return fmt.Errorf("profiles.%s.fallback: set quota for a fallback to apply", name)
			}
			if target == name {
				return fmt.Errorf("profiles.%s.fallback: a profile cannot fall back to itself", name)
			}
			if _, ok := c.Profiles[target]; !ok {
				return fmt.Errorf("profiles.%s.fallback.profile: unknown profile %q", name, target)
			}
		}
	}
	for name, tpl := range c.Templates {
		if err := validateNameComponent("templates."+name, name); err != nil {
			return err
		}
		if strings.TrimSpace(name) == "" || strings.TrimSpace(name) != name {
			return fmt.Errorf("templates: name %q must be non-empty without surrounding whitespace", name)
		}
		for slot, profile := range map[string]string{"worker": tpl.Worker, "orchestrator": tpl.Orchestrator} {
			if p := strings.TrimSpace(profile); p != "" {
				if _, ok := c.Profiles[p]; !ok {
					return fmt.Errorf("templates.%s.%s: unknown profile %q", name, slot, p)
				}
			}
		}
		for i, p := range tpl.Reviewers {
			profile, ok := c.Profiles[strings.TrimSpace(p)]
			if !ok {
				return fmt.Errorf("templates.%s.reviewers[%d]: unknown profile %q", name, i, p)
			}
			if !ReviewerHarness(profile.Harness).IsKnown() {
				return fmt.Errorf("templates.%s.reviewers[%d]: profile %q agent %q cannot review", name, i, p, profile.Harness)
			}
		}
		if err := validateRepoRelative(tpl.OrchestratorRulesFile); err != nil {
			return fmt.Errorf("templates.%s.orchestratorRulesFile %q: %w", name, tpl.OrchestratorRulesFile, err)
		}
	}
	if name := strings.TrimSpace(c.Template); name != "" {
		if _, ok := c.Templates[name]; !ok {
			return fmt.Errorf("template: unknown template %q", name)
		}
	}
	for _, s := range c.Symlinks {
		if err := validateRepoRelative(s); err != nil {
			return fmt.Errorf("symlink %q: %w", s, err)
		}
	}
	if err := validateRepoRelative(c.AgentRulesFile); err != nil {
		return fmt.Errorf("agentRulesFile %q: %w", c.AgentRulesFile, err)
	}
	for i, rv := range c.Reviewers {
		if !rv.Harness.IsKnown() {
			return fmt.Errorf("reviewers[%d].harness: unknown harness %q", i, rv.Harness)
		}
		if err := rv.AgentConfig.Validate(); err != nil {
			return fmt.Errorf("reviewers[%d].agentConfig: %w", i, err)
		}
		if p := strings.TrimSpace(rv.Profile); p != "" {
			profile, ok := c.Profiles[p]
			if !ok {
				return fmt.Errorf("reviewers[%d].profile: unknown profile %q", i, p)
			}
			if profile.Harness != "" && !ReviewerHarness(profile.Harness).IsKnown() {
				return fmt.Errorf("reviewers[%d].profile: profile %q agent %q cannot review", i, p, profile.Harness)
			}
		}
	}
	if err := c.TrackerIntake.Validate(); err != nil {
		return err
	}
	return nil
}

func validateNoWhitespaceField(name, value string) error {
	if value == "" {
		return nil
	}
	if strings.TrimSpace(value) != value {
		return fmt.Errorf("%s: must not have leading or trailing whitespace", name)
	}
	return nil
}

func validateNameComponent(name, value string) error {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return nil
	}
	if strings.ContainsAny(trimmed, `/\`) || trimmed == "." || trimmed == ".." {
		return fmt.Errorf("%s: must not contain path separators or traversal components", name)
	}
	return nil
}

// validateRepoRelative refuses paths that would let a project config escape
// its repo root: absolute paths and any ".." segment (before or after Clean).
// The same guard runs at spawn time as defense-in-depth, but enforcing it here
// rejects bad config when it is set rather than at every later spawn.
func validateRepoRelative(p string) error {
	trimmed := strings.TrimSpace(p)
	if trimmed == "" {
		return nil
	}
	if filepath.IsAbs(trimmed) || strings.HasPrefix(trimmed, "/") || strings.HasPrefix(trimmed, `\`) {
		return fmt.Errorf("path must be repo-relative and must not escape the project root")
	}
	clean := filepath.Clean(trimmed)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return fmt.Errorf("path must be repo-relative and must not escape the project root")
	}
	for _, seg := range strings.Split(filepath.ToSlash(clean), "/") {
		if seg == ".." {
			return fmt.Errorf("path must be repo-relative and must not escape the project root")
		}
	}
	return nil
}
