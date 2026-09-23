package agent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

type fakeRotationAccounts struct {
	accounts  AgyAccounts
	switches  []string
	switchErr error
	ensures   int
}

func (f *fakeRotationAccounts) CachedAgyAccounts(context.Context) (AgyAccounts, error) {
	return f.accounts, nil
}

func (f *fakeRotationAccounts) EnsureAgyAccounts(context.Context, []string, AgyAccountEnsureOptions) (AgyAccounts, error) {
	f.ensures++
	return f.accounts, nil
}

func (f *fakeRotationAccounts) StartAgyAccountSwitch(_ context.Context, cfg ports.AgyAccountSwitchConfig) (domain.AgyAccountSwitch, error) {
	if f.switchErr != nil {
		return domain.AgyAccountSwitch{}, f.switchErr
	}
	f.switches = append(f.switches, cfg.TargetAccountID)
	for i := range f.accounts.Accounts {
		f.accounts.Accounts[i].Active = f.accounts.Accounts[i].ID == cfg.TargetAccountID
	}
	return domain.AgyAccountSwitch{ID: "sw", Phase: domain.AgyAccountSwitchCompleted}, nil
}

func (f *fakeRotationAccounts) GetAgyAccountSwitch(context.Context, string) (domain.AgyAccountSwitch, error) {
	return domain.AgyAccountSwitch{ID: "sw", Phase: domain.AgyAccountSwitchCompleted}, nil
}

type fakeRotationSessions struct {
	relaunched   []AgyRelaunchedSession
	busy         []domain.SessionID
	relaunchArgs [][]domain.SessionID
	notices      map[domain.SessionID][]string
}

func (f *fakeRotationSessions) RelaunchIdleAgySessions(_ context.Context, only []domain.SessionID) ([]AgyRelaunchedSession, []domain.SessionID, error) {
	f.relaunchArgs = append(f.relaunchArgs, only)
	return f.relaunched, f.busy, nil
}

func (f *fakeRotationSessions) AgyOrchestrators(context.Context) ([]domain.SessionID, error) {
	return []domain.SessionID{"orch-1"}, nil
}

func (f *fakeRotationSessions) NotifyOrchestrator(_ context.Context, o domain.SessionID, msg string) {
	if f.notices == nil {
		f.notices = map[domain.SessionID][]string{}
	}
	f.notices[o] = append(f.notices[o], msg)
}

func rotationSnapshot(id string, active bool, fiveHourUsed float64, fiveHourReset time.Time, weeklyUsed float64, observed time.Time) domain.AgyAccountSnapshot {
	week := observed.Add(96 * time.Hour)
	return domain.AgyAccountSnapshot{
		ID: id, Label: id + "@example.com", Active: active, Status: domain.AgyAccountStatusValid,
		Authentication: domain.AgentAuthenticationObservation{State: domain.AgentAuthenticationAuthorized},
		Capacity: domain.AgyCapacitySnapshot{
			ObservedAt: &observed,
			Overall: &domain.AgyCapacityBucket{
				Primary:   &domain.AgyCapacityWindow{UsedPercent: fiveHourUsed, ResetsAt: &fiveHourReset},
				Secondary: &domain.AgyCapacityWindow{UsedPercent: weeklyUsed, ResetsAt: &week},
			},
		},
	}
}

func newTestRotator(accounts *fakeRotationAccounts, sessions *fakeRotationSessions, now *time.Time) *AgyAccountRotator {
	r := NewAgyAccountRotator(accounts, sessions, nil, "")
	r.now = func() time.Time { return *now }
	return r
}

// The whole path the owner asked for: the active account's 5-hour limit is
// spent, the backup has capacity, so AO switches, relaunches the idle workers
// onto it and tells their orchestrator once; a busy worker is moved when it
// goes idle.
func TestAgyRotationSwitchesRelaunchesAndNotifies(t *testing.T) {
	now := time.Date(2026, 9, 23, 17, 0, 0, 0, time.UTC)
	accounts := &fakeRotationAccounts{accounts: AgyAccounts{Accounts: []domain.AgyAccountSnapshot{
		rotationSnapshot("a", true, 100, now.Add(3*time.Hour), 40, now),
		rotationSnapshot("b", false, 5, now.Add(4*time.Hour), 10, now),
	}}}
	sessions := &fakeRotationSessions{
		relaunched: []AgyRelaunchedSession{{ID: "w-1", Parent: "orch-1", DisplayName: "minimap"}, {ID: "w-2", Parent: "orch-1"}},
		busy:       []domain.SessionID{"w-3"},
	}
	rotator := newTestRotator(accounts, sessions, &now)

	if err := rotator.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(accounts.switches, []string{"b"}) {
		t.Fatalf("switches = %v, want [b]", accounts.switches)
	}
	if len(sessions.relaunchArgs) != 1 || sessions.relaunchArgs[0] != nil {
		t.Fatalf("relaunch calls = %v, want one for every idle worker", sessions.relaunchArgs)
	}
	notices := sessions.notices["orch-1"]
	if len(notices) != 1 {
		t.Fatalf("orchestrator got %d notices, want one for both workers: %q", len(notices), notices)
	}
	for _, want := range []string{"a@example.com reached its limit", "b@example.com", "w-1 (\"minimap\")", "w-2", "ao send --session"} {
		if !strings.Contains(notices[0], want) {
			t.Fatalf("notice missing %q:\n%s", want, notices[0])
		}
	}

	// The busy worker goes idle: next tick relaunches it, and only it.
	sessions.relaunched = []AgyRelaunchedSession{{ID: "w-3", Parent: "orch-1"}}
	sessions.busy = nil
	now = now.Add(time.Minute)
	if err := rotator.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(sessions.relaunchArgs) != 2 || !slices.Equal(sessions.relaunchArgs[1], []domain.SessionID{"w-3"}) {
		t.Fatalf("relaunch calls = %v, want the busy worker alone", sessions.relaunchArgs)
	}
	if len(accounts.switches) != 1 {
		t.Fatalf("a reading taken before the switch caused another switch: %v", accounts.switches)
	}
}

func TestAgyRotationLeavesAHealthyAccountAlone(t *testing.T) {
	now := time.Date(2026, 9, 23, 17, 0, 0, 0, time.UTC)
	accounts := &fakeRotationAccounts{accounts: AgyAccounts{Accounts: []domain.AgyAccountSnapshot{
		rotationSnapshot("a", true, 40, now.Add(3*time.Hour), 40, now),
		rotationSnapshot("b", false, 5, now.Add(4*time.Hour), 10, now),
	}}}
	sessions := &fakeRotationSessions{}
	if err := newTestRotator(accounts, sessions, &now).Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(accounts.switches) != 0 || len(sessions.relaunchArgs) != 0 || len(sessions.notices) != 0 {
		t.Fatalf("acted on a healthy account: switches=%v relaunches=%v notices=%v", accounts.switches, sessions.relaunchArgs, sessions.notices)
	}
}

// No account has 5-hour capacity: switch to the one that resets first, relaunch
// nothing yet, and relaunch the idle workers once it has reset.
func TestAgyRotationWaitsForTheSoonestResetThenRelaunches(t *testing.T) {
	now := time.Date(2026, 9, 23, 17, 0, 0, 0, time.UTC)
	reset := now.Add(40 * time.Minute)
	accounts := &fakeRotationAccounts{accounts: AgyAccounts{Accounts: []domain.AgyAccountSnapshot{
		rotationSnapshot("a", true, 100, now.Add(3*time.Hour), 40, now),
		rotationSnapshot("b", false, 100, reset, 10, now),
	}}}
	sessions := &fakeRotationSessions{relaunched: []AgyRelaunchedSession{{ID: "w-1", Parent: "orch-1"}}}
	rotator := newTestRotator(accounts, sessions, &now)

	if err := rotator.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(accounts.switches, []string{"b"}) {
		t.Fatalf("switches = %v, want the soonest reset", accounts.switches)
	}
	if len(sessions.relaunchArgs) != 0 {
		t.Fatal("relaunched workers onto an account that has not reset yet")
	}
	if n := sessions.notices["orch-1"]; len(n) != 1 || !strings.Contains(n[0], "resets first") {
		t.Fatalf("waiting notice = %q", n)
	}

	// Still before the reset: nothing happens.
	now = reset.Add(-time.Minute)
	if err := rotator.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(sessions.relaunchArgs) != 0 {
		t.Fatal("relaunched before the reset")
	}

	// After the reset a fresh reading shows capacity: relaunch the idle workers.
	now = reset.Add(time.Minute)
	accounts.accounts.Accounts[1] = rotationSnapshot("b", true, 0, reset.Add(5*time.Hour), 10, now)
	if err := rotator.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(sessions.relaunchArgs) != 1 {
		t.Fatalf("relaunch calls after the reset = %v, want one", sessions.relaunchArgs)
	}
	if n := sessions.notices["orch-1"]; len(n) != 2 || !strings.Contains(n[1], "has reset") {
		t.Fatalf("reset notice = %q", n)
	}
}

func TestAgyRotationReportsAWeeklyExhaustedAccountOnce(t *testing.T) {
	now := time.Date(2026, 9, 23, 17, 0, 0, 0, time.UTC)
	accounts := &fakeRotationAccounts{accounts: AgyAccounts{Accounts: []domain.AgyAccountSnapshot{
		rotationSnapshot("a", true, 100, now.Add(3*time.Hour), 40, now),
		rotationSnapshot("b", false, 5, now.Add(4*time.Hour), 100, now),
		rotationSnapshot("c", false, 5, now.Add(4*time.Hour), 10, now),
	}}}
	sessions := &fakeRotationSessions{relaunched: []AgyRelaunchedSession{{ID: "w-1", Parent: "orch-1"}}}
	if err := newTestRotator(accounts, sessions, &now).Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(accounts.switches, []string{"c"}) {
		t.Fatalf("switches = %v, want c (b is out of its weekly limit)", accounts.switches)
	}
	if n := sessions.notices["orch-1"]; len(n) != 1 || !strings.Contains(n[0], "b@example.com is out of its weekly limit") {
		t.Fatalf("notice = %q, want the weekly flag", n)
	}
}

func TestAgyRotationDoesNotRelaunchWhenTheSwitchFails(t *testing.T) {
	now := time.Date(2026, 9, 23, 17, 0, 0, 0, time.UTC)
	accounts := &fakeRotationAccounts{
		accounts: AgyAccounts{Accounts: []domain.AgyAccountSnapshot{
			rotationSnapshot("a", true, 100, now.Add(3*time.Hour), 40, now),
			rotationSnapshot("b", false, 5, now.Add(4*time.Hour), 10, now),
		}},
		switchErr: errors.New("keychain did not keep the credential"),
	}
	sessions := &fakeRotationSessions{}
	if err := newTestRotator(accounts, sessions, &now).Tick(context.Background()); err == nil {
		t.Fatal("a failed switch was not reported")
	}
	if len(sessions.relaunchArgs) != 0 || len(sessions.notices) != 0 {
		t.Fatal("relaunched or notified after a switch that did not happen")
	}
}

// The work a rotation still owes survives an AO restart (such as a rebuild and
// install): running agy workers keep the account they started with through the
// restart, so a worker left busy at the switch must still be moved afterwards,
// and a reset still waited for.
func TestAgyRotationStateSurvivesARestart(t *testing.T) {
	now := time.Date(2026, 9, 23, 17, 0, 0, 0, time.UTC)
	statePath := filepath.Join(t.TempDir(), "rotation.json")
	accounts := &fakeRotationAccounts{accounts: AgyAccounts{Accounts: []domain.AgyAccountSnapshot{
		rotationSnapshot("a", true, 100, now.Add(3*time.Hour), 40, now),
		rotationSnapshot("b", false, 5, now.Add(4*time.Hour), 10, now),
	}}}
	sessions := &fakeRotationSessions{busy: []domain.SessionID{"w-busy"}}
	first := NewAgyAccountRotator(accounts, sessions, nil, statePath)
	first.now = func() time.Time { return now }
	if err := first.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(first.pendingBusy, []domain.SessionID{"w-busy"}) {
		t.Fatalf("pending = %v, want the busy worker", first.pendingBusy)
	}

	// A new daemon: a fresh rotator over the same state file.
	sessions.busy = nil
	sessions.relaunched = []AgyRelaunchedSession{{ID: "w-busy", Parent: "orch-1"}}
	second := NewAgyAccountRotator(accounts, sessions, nil, statePath)
	second.now = func() time.Time { return now.Add(time.Minute) }
	if !slices.Equal(second.pendingBusy, []domain.SessionID{"w-busy"}) || !second.lastSwitch.Equal(now) {
		t.Fatalf("restored state: pending=%v lastSwitch=%v", second.pendingBusy, second.lastSwitch)
	}
	if err := second.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	last := sessions.relaunchArgs[len(sessions.relaunchArgs)-1]
	if !slices.Equal(last, []domain.SessionID{"w-busy"}) {
		t.Fatalf("after the restart the owed worker was not moved: last relaunch = %v", last)
	}
	if len(accounts.switches) != 1 {
		t.Fatalf("the restart caused another switch: %v", accounts.switches)
	}
}

// A corrupt state file must not stop the daemon or the rotation.
func TestAgyRotationIgnoresAnUnreadableStateFile(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "rotation.json")
	if err := os.WriteFile(statePath, []byte("not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	r := NewAgyAccountRotator(&fakeRotationAccounts{}, &fakeRotationSessions{}, nil, statePath)
	if len(r.pendingBusy) != 0 || !r.waitUntil.IsZero() {
		t.Fatalf("corrupt state produced work: %+v", r)
	}
}

// The loop seen live: agy's sign-in probe timed out, leaving both accounts'
// authentication "unknown" while their capacity readings were fresh and both
// 5-hour windows spent. The rotation relaunched every idle worker onto the same
// account each minute. It must stay on the account that resets first and
// relaunch nothing until that reset.
func TestAgyRotationDoesNotLoopWhenTheSignInCheckIsUnknown(t *testing.T) {
	now := time.Date(2026, 9, 23, 20, 0, 0, 0, time.UTC)
	a := rotationSnapshot("a", true, 100, now.Add(47*time.Minute), 60, now)
	b := rotationSnapshot("b", false, 100, now.Add(2*time.Hour), 30, now)
	a.Authentication.State = domain.AgentAuthenticationUnknown
	b.Authentication.State = domain.AgentAuthenticationUnknown
	accounts := &fakeRotationAccounts{accounts: AgyAccounts{Accounts: []domain.AgyAccountSnapshot{a, b}}}
	sessions := &fakeRotationSessions{relaunched: []AgyRelaunchedSession{{ID: "w-1", Parent: "orch-1"}}}
	rotator := newTestRotator(accounts, sessions, &now)

	for range 3 {
		if err := rotator.Tick(context.Background()); err != nil {
			t.Fatal(err)
		}
		now = now.Add(time.Minute)
	}
	if len(accounts.switches) != 0 || len(sessions.relaunchArgs) != 0 {
		t.Fatalf("acted before the reset: switches=%v relaunches=%v", accounts.switches, sessions.relaunchArgs)
	}
	if want := a.Capacity.Overall.Primary.ResetsAt; !rotator.waitUntil.Equal(*want) {
		t.Fatalf("waitUntil = %v, want the active account's reset %v", rotator.waitUntil, *want)
	}
}

// With no account it can choose and no reset it knows of, the rotation does
// nothing: no switch, no relaunch, no notice.
func TestAgyRotationDoesNothingWhenNoAccountCanBeChosen(t *testing.T) {
	now := time.Date(2026, 9, 23, 20, 0, 0, 0, time.UTC)
	a := rotationSnapshot("a", true, 100, now.Add(47*time.Minute), 60, now)
	b := rotationSnapshot("b", false, 100, now.Add(2*time.Hour), 30, now)
	a.Authentication.State = domain.AgentAuthenticationUnauthorized
	b.Authentication.State = domain.AgentAuthenticationUnauthorized
	accounts := &fakeRotationAccounts{accounts: AgyAccounts{Accounts: []domain.AgyAccountSnapshot{a, b}}}
	sessions := &fakeRotationSessions{relaunched: []AgyRelaunchedSession{{ID: "w-1", Parent: "orch-1"}}}
	rotator := newTestRotator(accounts, sessions, &now)

	for range 3 {
		if err := rotator.Tick(context.Background()); err != nil {
			t.Fatal(err)
		}
		now = now.Add(time.Minute)
	}
	if len(accounts.switches) != 0 || len(sessions.relaunchArgs) != 0 || len(sessions.notices) != 0 {
		t.Fatalf("acted with nothing to act on: switches=%v relaunches=%v notices=%v", accounts.switches, sessions.relaunchArgs, sessions.notices)
	}
}
