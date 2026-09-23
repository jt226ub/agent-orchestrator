package agent

import (
	"testing"
	"time"
)

func TestPlanAgyRotation(t *testing.T) {
	now := time.Date(2026, 9, 23, 17, 0, 0, 0, time.UTC)
	in := func(d time.Duration) time.Time { return now.Add(d) }
	win := func(used float64, resets time.Time) agyRotationWindow {
		return agyRotationWindow{Known: true, UsedPercent: used, ResetsAt: resets}
	}
	fresh := win(10, in(4*time.Hour))
	weekOK := win(40, in(96*time.Hour))
	spent := func(resets time.Time) agyRotationWindow { return win(100, resets) }
	acct := func(id string, active bool, fiveHour, weekly agyRotationWindow) agyRotationCandidate {
		return agyRotationCandidate{ID: id, Label: id, Active: active, Usable: true, FiveHour: fiveHour, Weekly: weekly}
	}

	for _, tt := range []struct {
		name       string
		accounts   []agyRotationCandidate
		wantOK     bool
		wantTarget string
		wantWait   time.Time
		wantWeekly []string
	}{
		{
			name:     "active still has capacity: nothing to do",
			accounts: []agyRotationCandidate{acct("a", true, fresh, weekOK), acct("b", false, fresh, weekOK)},
		},
		{
			name:       "5-hour spent: switch to the backup",
			accounts:   []agyRotationCandidate{acct("a", true, spent(in(time.Hour)), weekOK), acct("b", false, fresh, weekOK)},
			wantOK:     true,
			wantTarget: "b",
		},
		{
			name: "next in order, wrapping round, not just any",
			accounts: []agyRotationCandidate{
				acct("a", false, fresh, weekOK), acct("b", true, spent(in(time.Hour)), weekOK), acct("c", false, spent(in(2*time.Hour)), weekOK),
			},
			wantOK:     true,
			wantTarget: "a",
		},
		{
			name: "a reading a hair under 100% still counts as spent",
			accounts: []agyRotationCandidate{
				acct("a", true, win(99.8, in(time.Hour)), weekOK), acct("b", false, fresh, weekOK),
			},
			wantOK:     true,
			wantTarget: "b",
		},
		{
			name: "all 5-hour spent: switch to the soonest reset and wait for it",
			accounts: []agyRotationCandidate{
				acct("a", true, spent(in(3*time.Hour)), weekOK), acct("b", false, spent(in(40*time.Minute)), weekOK),
			},
			wantOK:     true,
			wantTarget: "b",
			wantWait:   in(40 * time.Minute),
		},
		{
			name: "all 5-hour spent and the active resets first: stay and wait",
			accounts: []agyRotationCandidate{
				acct("a", true, spent(in(20*time.Minute)), weekOK), acct("b", false, spent(in(2*time.Hour)), weekOK),
			},
			wantOK:   true,
			wantWait: in(20 * time.Minute),
		},
		{
			name: "a weekly-exhausted account is skipped and flagged until its reset",
			accounts: []agyRotationCandidate{
				acct("a", true, spent(in(time.Hour)), weekOK), acct("b", false, fresh, spent(in(72*time.Hour))), acct("c", false, fresh, weekOK),
			},
			wantOK:     true,
			wantTarget: "c",
			wantWeekly: []string{"b"},
		},
		{
			name: "active out of its weekly window moves on even with 5-hour left",
			accounts: []agyRotationCandidate{
				acct("a", true, fresh, spent(in(72*time.Hour))), acct("b", false, fresh, weekOK),
			},
			wantOK:     true,
			wantTarget: "b",
			wantWeekly: []string{"a"},
		},
		{
			name: "every account out of its weekly window: wait for the first to return",
			accounts: []agyRotationCandidate{
				acct("a", true, fresh, spent(in(72*time.Hour))), acct("b", false, fresh, spent(in(30*time.Hour))),
			},
			wantOK:     true,
			wantWait:   in(30 * time.Hour),
			wantWeekly: []string{"a", "b"},
		},
		{
			name: "signed-out and unknown-capacity accounts are never chosen",
			accounts: []agyRotationCandidate{
				acct("a", true, spent(in(time.Hour)), weekOK),
				{ID: "b", Usable: false, FiveHour: fresh, Weekly: weekOK},
				{ID: "c", Usable: true},
				acct("d", false, fresh, weekOK),
			},
			wantOK:     true,
			wantTarget: "d",
		},
		{
			name: "no other account can be chosen and no reset is known: nothing to do",
			accounts: []agyRotationCandidate{
				{ID: "a", Active: true, FiveHour: spent(in(time.Hour)), Weekly: weekOK},
				{ID: "b", FiveHour: spent(in(2 * time.Hour)), Weekly: weekOK},
			},
		},
		{
			name:     "active capacity unknown: do not guess",
			accounts: []agyRotationCandidate{{ID: "a", Active: true, Usable: true}, acct("b", false, fresh, weekOK)},
		},
		{
			name: "a window whose reset time has passed is not spent",
			accounts: []agyRotationCandidate{
				acct("a", true, spent(in(-time.Minute)), weekOK), acct("b", false, fresh, weekOK),
			},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			plan, ok := planAgyRotation(tt.accounts, now)
			if ok != tt.wantOK {
				t.Fatalf("ok = %v, want %v (plan %+v)", ok, tt.wantOK, plan)
			}
			if plan.TargetID != tt.wantTarget {
				t.Fatalf("target = %q, want %q", plan.TargetID, tt.wantTarget)
			}
			if !plan.WaitUntil.Equal(tt.wantWait) {
				t.Fatalf("wait until = %v, want %v", plan.WaitUntil, tt.wantWait)
			}
			if len(plan.WeeklyExhausted) != len(tt.wantWeekly) {
				t.Fatalf("weekly exhausted = %v, want %v", plan.WeeklyExhausted, tt.wantWeekly)
			}
			for _, id := range tt.wantWeekly {
				if _, ok := plan.WeeklyExhausted[id]; !ok {
					t.Fatalf("weekly exhausted = %v, missing %q", plan.WeeklyExhausted, id)
				}
			}
		})
	}
}
