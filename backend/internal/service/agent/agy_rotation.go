package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

const (
	// AgyRotationInterval is how often the rotation looks at the accounts.
	AgyRotationInterval = time.Minute
	// agyRotationRefreshEvery bounds how old a capacity reading may be before
	// the rotation asks for new ones; per-account reads are cached for 5 min.
	agyRotationRefreshEvery = 2 * time.Minute
	// agyRotationSwitchWait bounds how long one tick waits for a switch.
	agyRotationSwitchWait = time.Minute
)

// agyRotationAccounts is the part of the agent service the rotation drives.
type agyRotationAccounts interface {
	CachedAgyAccounts(ctx context.Context) (AgyAccounts, error)
	EnsureAgyAccounts(ctx context.Context, ids []string, options AgyAccountEnsureOptions) (AgyAccounts, error)
	StartAgyAccountSwitch(ctx context.Context, cfg ports.AgyAccountSwitchConfig) (domain.AgyAccountSwitch, error)
	GetAgyAccountSwitch(ctx context.Context, id string) (domain.AgyAccountSwitch, error)
}

// AgyRelaunchedSession is one Antigravity worker moved onto the device account.
type AgyRelaunchedSession struct {
	ID          domain.SessionID
	Parent      domain.SessionID
	DisplayName string
	// Err is set when the exit or the resume failed; the session then needs a
	// restore by hand.
	Err error
}

// AgyRotationSessions is what the rotation needs from the session side.
type AgyRotationSessions interface {
	// RelaunchIdleAgySessions relaunches live, idle Antigravity terminal
	// sessions -- keeping worktree and conversation -- so they sign in as the
	// device account. A running agy process keeps the account it started with,
	// so a switch alone does not reach them. only restricts the set; nil means
	// every live Antigravity session. Busy sessions are returned untouched.
	RelaunchIdleAgySessions(ctx context.Context, only []domain.SessionID) (relaunched []AgyRelaunchedSession, busy []domain.SessionID, err error)
	// AgyOrchestrators lists the live orchestrators of live Antigravity sessions.
	AgyOrchestrators(ctx context.Context) ([]domain.SessionID, error)
	// NotifyOrchestrator delivers an AO notice, queued while the orchestrator is busy.
	NotifyOrchestrator(ctx context.Context, orchestrator domain.SessionID, message string)
}

// AgyAccountRotator switches the device Antigravity account when the active one
// reaches its limit, as planAgyRotation decides, relaunches idle Antigravity
// workers onto the new account and tells their orchestrators. It acts only on
// fresh capacity readings and never on an account whose capacity is unknown.
type AgyAccountRotator struct {
	accounts agyRotationAccounts
	sessions AgyRotationSessions
	logger   *slog.Logger
	now      func() time.Time

	// statePath keeps the work a rotation still owes -- busy workers to move,
	// a reset to wait for -- across an AO restart. Empty keeps it in memory.
	statePath string

	mu          sync.Mutex
	lastRefresh time.Time
	lastSwitch  time.Time
	waitUntil   time.Time
	pendingBusy []domain.SessionID
	announced   map[string]time.Time // weekly flags already reported, by account
}

// agyRotationState is the part of a rotation that must outlive an AO restart:
// running agy workers keep the account they started with through a restart
// (their terminals are adopted, not relaunched), so work still owed to them
// cannot be rediscovered from the account readings alone.
type agyRotationState struct {
	PendingBusy []domain.SessionID `json:"pendingBusy,omitempty"`
	WaitUntil   time.Time          `json:"waitUntil,omitzero"`
	LastSwitch  time.Time          `json:"lastSwitch,omitzero"`
}

// NewAgyAccountRotator builds the rotation over the agent service and the
// session side, resuming any work a previous daemon left in statePath.
func NewAgyAccountRotator(accounts agyRotationAccounts, sessions AgyRotationSessions, logger *slog.Logger, statePath string) *AgyAccountRotator {
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	r := &AgyAccountRotator{
		accounts: accounts, sessions: sessions, logger: logger, statePath: statePath,
		now:       func() time.Time { return time.Now().UTC() },
		announced: map[string]time.Time{},
	}
	r.load()
	return r
}

func (r *AgyAccountRotator) load() {
	if r.statePath == "" {
		return
	}
	data, err := os.ReadFile(r.statePath)
	if errors.Is(err, os.ErrNotExist) {
		return
	}
	var state agyRotationState
	if err == nil {
		err = json.Unmarshal(data, &state)
	}
	if err != nil {
		r.logger.Warn("agy rotation: saved state unreadable; starting clean", "path", r.statePath, "error", err)
		return
	}
	r.pendingBusy, r.waitUntil, r.lastSwitch = state.PendingBusy, state.WaitUntil, state.LastSwitch
	if len(r.pendingBusy) > 0 || !r.waitUntil.IsZero() {
		r.logger.Info("agy rotation: resuming saved work", "pendingWorkers", len(r.pendingBusy), "waitUntil", r.waitUntil)
	}
}

// save records the owed work after every change; a failed write is logged, not
// fatal, since the readings will still drive the next switch.
func (r *AgyAccountRotator) save() {
	if r.statePath == "" {
		return
	}
	data, err := json.Marshal(agyRotationState{PendingBusy: r.pendingBusy, WaitUntil: r.waitUntil, LastSwitch: r.lastSwitch})
	if err == nil {
		err = writePrivateFileAtomic(r.statePath, data)
	}
	if err != nil {
		r.logger.Warn("agy rotation: could not save state", "path", r.statePath, "error", err)
	}
}

// Run ticks every interval until ctx ends.
func (r *AgyAccountRotator) Run(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = AgyRotationInterval
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := r.Tick(ctx); err != nil {
				r.logger.Warn("agy rotation: tick failed", "error", err)
			}
		}
	}
}

// Tick looks at the accounts once and acts on what it finds.
func (r *AgyAccountRotator) Tick(ctx context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	defer r.save()
	now := r.now()

	accounts, err := r.accounts.CachedAgyAccounts(ctx)
	if err != nil {
		return nil // account management is unavailable; nothing to rotate
	}
	if accounts.CurrentSwitch != nil || len(accounts.Accounts) == 0 {
		return nil
	}
	if now.Sub(r.lastRefresh) >= agyRotationRefreshEvery {
		refreshed, err := r.accounts.EnsureAgyAccounts(ctx, nil, AgyAccountEnsureOptions{})
		if err != nil {
			return fmt.Errorf("refresh accounts: %w", err)
		}
		accounts, r.lastRefresh = refreshed, now
	}

	// Workers that were busy at the last switch are moved once they go idle.
	if len(r.pendingBusy) > 0 {
		relaunched, busy, err := r.sessions.RelaunchIdleAgySessions(ctx, r.pendingBusy)
		if err != nil {
			return fmt.Errorf("relaunch workers: %w", err)
		}
		r.pendingBusy = busy
		r.notifyRelaunched(ctx, relaunched, activeLabel(accounts), "It went idle after the switch and has been relaunched onto the new account.")
	}

	active, ok := activeSnapshot(accounts)
	if !ok || active.Capacity.ObservedAt == nil {
		return nil
	}
	// Never act on a reading older than the last switch: it describes the
	// account before it changed.
	if !r.lastSwitch.IsZero() && active.Capacity.ObservedAt.Before(r.lastSwitch) {
		return nil
	}

	candidates := make([]agyRotationCandidate, 0, len(accounts.Accounts))
	for _, a := range accounts.Accounts {
		candidates = append(candidates, agyRotationCandidateFrom(a))
	}
	plan, act := planAgyRotation(candidates, now)
	weekly := r.newWeeklyFlags(plan.WeeklyExhausted, accounts)

	if !r.waitUntil.IsZero() {
		if now.Before(r.waitUntil) {
			return nil
		}
		if act && plan.TargetID == "" && !plan.WaitUntil.IsZero() && plan.WaitUntil.After(now) {
			r.waitUntil = plan.WaitUntil // the reset moved; keep waiting
			return nil
		}
		if !act {
			// The account reset: bring the idle workers back onto it.
			r.waitUntil = time.Time{}
			return r.relaunchAll(ctx, accounts, fmt.Sprintf("%s's 5-hour limit has reset.", active.Label), weekly)
		}
	}
	if !act {
		return nil
	}

	if plan.TargetID != "" {
		if err := r.switchTo(ctx, plan.TargetID); err != nil {
			return err
		}
		r.lastSwitch = now
		accounts, _ = r.accounts.CachedAgyAccounts(ctx)
	}
	if plan.WaitUntil.IsZero() {
		return r.relaunchAll(ctx, accounts, fmt.Sprintf("%s reached its limit; Antigravity is now on %s.", active.Label, labelOf(accounts, plan.TargetID)), weekly)
	}
	r.waitUntil = plan.WaitUntil
	on := active.Label
	if plan.TargetID != "" {
		on = labelOf(accounts, plan.TargetID)
	}
	r.notifyAll(ctx, fmt.Sprintf("[AO] Every Antigravity account has used its 5-hour limit. Antigravity is on %s, which resets first, at %s. AO will relaunch idle Antigravity workers when it does.%s",
		on, plan.WaitUntil.UTC().Format(time.RFC3339), weekly))
	return nil
}

func (r *AgyAccountRotator) switchTo(ctx context.Context, target string) error {
	sw, err := r.accounts.StartAgyAccountSwitch(ctx, ports.AgyAccountSwitchConfig{
		TargetAccountID: target,
		IdempotencyKey:  fmt.Sprintf("auto-rotation-%s-%d", target, r.now().UnixNano()),
	})
	if err != nil {
		return fmt.Errorf("switch to %s: %w", target, err)
	}
	deadline := time.Now().Add(agyRotationSwitchWait)
	for sw.Phase != domain.AgyAccountSwitchCompleted {
		if sw.Phase == domain.AgyAccountSwitchFailed || sw.Phase == domain.AgyAccountSwitchRecoveryRequired {
			return fmt.Errorf("switch to %s ended %s", target, sw.Phase)
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("switch to %s did not finish within %s", target, agyRotationSwitchWait)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(250 * time.Millisecond):
		}
		if sw, err = r.accounts.GetAgyAccountSwitch(ctx, sw.ID); err != nil {
			return fmt.Errorf("switch to %s: %w", target, err)
		}
	}
	r.logger.Info("agy rotation: switched account", "target", target)
	return nil
}

// relaunchAll moves every idle Antigravity worker onto the device account and
// remembers the busy ones for later ticks.
func (r *AgyAccountRotator) relaunchAll(ctx context.Context, accounts AgyAccounts, why, weekly string) error {
	relaunched, busy, err := r.sessions.RelaunchIdleAgySessions(ctx, nil)
	if err != nil {
		return fmt.Errorf("relaunch workers: %w", err)
	}
	r.pendingBusy = busy
	if len(busy) > 0 {
		ids := make([]string, len(busy))
		for i, id := range busy {
			ids[i] = string(id)
		}
		why += fmt.Sprintf(" Still mid-turn on the previous account, and moved automatically once idle (or now, if idle, with `ao session restart-agent <id>`): %s.", strings.Join(ids, ", "))
	}
	r.notifyRelaunched(ctx, relaunched, activeLabel(accounts), why+weekly)
	return nil
}

// notifyRelaunched sends each orchestrator one notice for its workers.
func (r *AgyAccountRotator) notifyRelaunched(ctx context.Context, relaunched []AgyRelaunchedSession, account, why string) {
	byParent := map[domain.SessionID][]AgyRelaunchedSession{}
	for _, s := range relaunched {
		if s.Err != nil {
			r.logger.Warn("agy rotation: could not relaunch worker", "session", s.ID, "error", s.Err)
		}
		if s.Parent != "" {
			byParent[s.Parent] = append(byParent[s.Parent], s)
		}
	}
	for parent, sessions := range byParent {
		var b strings.Builder
		fmt.Fprintf(&b, "[AO] Antigravity account rotation. %s\n", why)
		fmt.Fprintf(&b, "Workers relaunched onto %s, each keeping its worktree and conversation; they are idle, so continue each with `ao send --session <id> --message \"...\"`:", account)
		var failed []string
		for _, s := range sessions {
			if s.Err != nil {
				failed = append(failed, string(s.ID))
				continue
			}
			fmt.Fprintf(&b, "\n- %s", s.ID)
			if name := strings.TrimSpace(domain.SanitizeControlChars(s.DisplayName)); name != "" {
				fmt.Fprintf(&b, " (%q)", name)
			}
		}
		if len(failed) > 0 {
			fmt.Fprintf(&b, "\nCould not relaunch; retry with `ao session restart-agent <id>` (or `ao session restore <id>` if it is terminated): %s", strings.Join(failed, ", "))
		}
		r.sessions.NotifyOrchestrator(ctx, parent, b.String())
	}
}

func (r *AgyAccountRotator) notifyAll(ctx context.Context, message string) {
	orchestrators, err := r.sessions.AgyOrchestrators(ctx)
	if err != nil {
		r.logger.Warn("agy rotation: could not list orchestrators", "error", err)
		return
	}
	for _, o := range orchestrators {
		r.sessions.NotifyOrchestrator(ctx, o, message)
	}
}

// newWeeklyFlags logs accounts newly out of their weekly window and renders
// them for the next notice; each is reported once per weekly window.
func (r *AgyAccountRotator) newWeeklyFlags(flags map[string]time.Time, accounts AgyAccounts) string {
	ids := make([]string, 0, len(flags))
	for id, until := range flags {
		if seen, ok := r.announced[id]; ok && seen.Equal(until) {
			continue
		}
		r.announced[id] = until
		ids = append(ids, id)
	}
	sort.Strings(ids)
	var b strings.Builder
	for _, id := range ids {
		until := flags[id].UTC().Format(time.RFC3339)
		r.logger.Warn("agy rotation: account out of its weekly limit; skipped until reset", "account", id, "until", until)
		fmt.Fprintf(&b, " %s is out of its weekly limit and is skipped until %s.", labelOf(accounts, id), until)
	}
	return b.String()
}

func activeSnapshot(accounts AgyAccounts) (domain.AgyAccountSnapshot, bool) {
	for _, a := range accounts.Accounts {
		if a.Active {
			return a, true
		}
	}
	return domain.AgyAccountSnapshot{}, false
}

func activeLabel(accounts AgyAccounts) string {
	if a, ok := activeSnapshot(accounts); ok {
		return a.Label
	}
	return "the active account"
}

func labelOf(accounts AgyAccounts, id string) string {
	for _, a := range accounts.Accounts {
		if a.ID == id {
			return a.Label
		}
	}
	return id
}
