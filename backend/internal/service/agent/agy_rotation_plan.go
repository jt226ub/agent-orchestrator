package agent

import (
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

// agyRotationExhaustedPercent is the used share at which a window counts as
// spent. The CLI reports fractions (34.722...), so a window at its limit can
// read a hair under 100.
const agyRotationExhaustedPercent = 99.5

// agyRotationWindow is one plan window of the Gemini model group.
type agyRotationWindow struct {
	Known       bool
	UsedPercent float64
	ResetsAt    time.Time
}

// exhausted reports a window that is spent and has not reset yet.
func (w agyRotationWindow) exhausted(now time.Time) bool {
	return w.Known && w.UsedPercent >= agyRotationExhaustedPercent &&
		(w.ResetsAt.IsZero() || now.Before(w.ResetsAt))
}

// agyRotationCandidate is one Antigravity account as the rotation sees it. The
// windows are the Gemini group's -- the meter every profile but the Claude-model
// ones draws on -- with the 5-hour window as Primary and the weekly as Secondary.
type agyRotationCandidate struct {
	ID       string
	Label    string
	Active   bool
	Usable   bool
	FiveHour agyRotationWindow
	Weekly   agyRotationWindow
}

// agyRotationPlan is what one reading of the accounts calls for.
type agyRotationPlan struct {
	// TargetID is the account to switch to; empty means stay.
	TargetID string
	// WaitUntil is set when no account has 5-hour capacity left: the time the
	// chosen account resets, at which idle workers are relaunched. Zero when the
	// switch lands on an account that can work now.
	WaitUntil time.Time
	// WeeklyExhausted lists accounts out of their weekly window, each until its
	// weekly reset; they are skipped until then.
	WeeklyExhausted map[string]time.Time
}

func agyRotationCandidateFrom(a domain.AgyAccountSnapshot) agyRotationCandidate {
	// Only a definite "unauthorized" rules an account out. agy's sign-in probe
	// times out under load and leaves the state "unknown"; requiring
	// "authorized" made every account unusable. A decision still needs a known
	// capacity reading, which only a working credential produces.
	c := agyRotationCandidate{
		ID:     a.ID,
		Label:  a.Label,
		Active: a.Active,
		Usable: a.Status == domain.AgyAccountStatusValid &&
			a.Authentication.State != domain.AgentAuthenticationUnauthorized,
	}
	if overall := a.Capacity.Overall; overall != nil {
		c.FiveHour = agyRotationWindowFrom(overall.Primary)
		c.Weekly = agyRotationWindowFrom(overall.Secondary)
	}
	return c
}

func agyRotationWindowFrom(w *domain.AgyCapacityWindow) agyRotationWindow {
	if w == nil {
		return agyRotationWindow{}
	}
	out := agyRotationWindow{Known: true, UsedPercent: w.UsedPercent}
	if w.ResetsAt != nil {
		out.ResetsAt = *w.ResetsAt
	}
	return out
}

// planAgyRotation decides what to do when the active account hits a limit:
//
//  1. Nothing while the active account still has 5-hour and weekly capacity.
//  2. Otherwise the next account in order, wrapping round, that is signed in,
//     has weekly capacity and has 5-hour capacity left.
//  3. If none has 5-hour capacity, the account whose 5-hour window resets
//     soonest (staying put if that is the active one), with WaitUntil set to
//     that reset so idle workers are relaunched once it has.
//  4. If every account is out of its weekly window, stay and wait for the
//     earliest weekly reset.
//
// An account whose capacity is unknown is never chosen: the rotation does not
// guess. ok is false when there is nothing to do.
func planAgyRotation(accounts []agyRotationCandidate, now time.Time) (agyRotationPlan, bool) {
	plan := agyRotationPlan{WeeklyExhausted: map[string]time.Time{}}
	active := -1
	for i, a := range accounts {
		if a.Active {
			active = i
		}
		if a.Weekly.exhausted(now) {
			plan.WeeklyExhausted[a.ID] = a.Weekly.ResetsAt
		}
	}
	if active < 0 || !accounts[active].FiveHour.Known {
		return agyRotationPlan{}, false
	}
	current := accounts[active]
	if !current.FiveHour.exhausted(now) && !current.Weekly.exhausted(now) {
		return agyRotationPlan{}, false
	}
	viable := func(a agyRotationCandidate) bool {
		return a.Usable && a.FiveHour.Known && a.Weekly.Known && !a.Weekly.exhausted(now)
	}

	for step := 1; step < len(accounts); step++ {
		a := accounts[(active+step)%len(accounts)]
		if viable(a) && !a.FiveHour.exhausted(now) {
			plan.TargetID = a.ID
			return plan, true
		}
	}

	soonest := -1
	for i, a := range accounts {
		if !viable(a) || a.FiveHour.ResetsAt.IsZero() {
			continue
		}
		if soonest < 0 || a.FiveHour.ResetsAt.Before(accounts[soonest].FiveHour.ResetsAt) {
			soonest = i
		}
	}
	if soonest >= 0 {
		if soonest != active {
			plan.TargetID = accounts[soonest].ID
		}
		plan.WaitUntil = accounts[soonest].FiveHour.ResetsAt
		return plan, true
	}

	// Every account is out of its weekly window: wait for the first to return.
	for _, until := range plan.WeeklyExhausted {
		if !until.IsZero() && (plan.WaitUntil.IsZero() || until.Before(plan.WaitUntil)) {
			plan.WaitUntil = until
		}
	}
	if plan.WaitUntil.IsZero() {
		// No account can be chosen and no reset is known: there is nothing to do.
		// Reporting "act" here with no target made the rotation relaunch every
		// idle worker, onto the same account, every minute.
		return agyRotationPlan{}, false
	}
	return plan, true
}
