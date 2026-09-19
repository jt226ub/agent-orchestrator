package store_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func TestAgyAccountSwitchIdempotencyAndSingleActiveConstraint(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st := newTestStore(t)
	now := time.Now().UTC().Truncate(time.Second)
	first := domain.AgyAccountSwitch{
		ID: "switch-a", SourceKind: domain.AgyAccountSwitchSourceDevice, TargetAccountID: "account-b",
		IdempotencyKey: "request-a",
		Phase:          domain.AgyAccountSwitchActivatingAccount, CreatedAt: now, UpdatedAt: now,
	}

	created, inserted, err := st.CreateAgyAccountSwitch(ctx, first)
	if err != nil || !inserted || created.ID != first.ID || created.SourceKind != domain.AgyAccountSwitchSourceDevice || created.SourceAccountID != "" {
		t.Fatalf("create switch: got=%+v inserted=%v err=%v", created, inserted, err)
	}
	replayed, inserted, err := st.CreateAgyAccountSwitch(ctx, first)
	if err != nil || inserted || replayed.ID != first.ID {
		t.Fatalf("replay switch: got=%+v inserted=%v err=%v", replayed, inserted, err)
	}
	conflict := first
	conflict.ID = "switch-b"
	conflict.TargetAccountID = "account-c"
	if _, _, err := st.CreateAgyAccountSwitch(ctx, conflict); !errors.Is(err, ports.ErrAgyAccountSwitchIdempotencyConflict) {
		t.Fatalf("idempotency conflict error = %v", err)
	}
	other := first
	other.ID = "switch-c"
	other.IdempotencyKey = "request-c"
	if _, _, err := st.CreateAgyAccountSwitch(ctx, other); !errors.Is(err, ports.ErrAgyAccountSwitchInProgress) {
		t.Fatalf("active switch conflict error = %v", err)
	}
}

func TestAgyAccountSwitchRejectsObsoletePhases(t *testing.T) {
	t.Parallel()
	for _, phase := range []string{"waiting_for_safe_boundary", "cancelled"} {
		t.Run(phase, func(t *testing.T) {
			t.Parallel()
			st := newTestStore(t)
			now := time.Now().UTC().Truncate(time.Second)
			switchRecord := domain.AgyAccountSwitch{
				ID: "switch-" + phase, SourceAccountID: "account-a", TargetAccountID: "account-b",
				IdempotencyKey: "request-" + phase,
				Phase:          domain.AgyAccountSwitchPhase(phase), CreatedAt: now, UpdatedAt: now,
			}

			if _, _, err := st.CreateAgyAccountSwitch(context.Background(), switchRecord); err == nil {
				t.Fatalf("create switch with obsolete phase %q succeeded", phase)
			}
		})
	}
}

func TestAgyAccountSwitchTransitionsAreCompareAndSwap(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st := newTestStore(t)
	now := time.Now().UTC().Truncate(time.Second)
	sw := domain.AgyAccountSwitch{
		ID: "switch-cas", SourceAccountID: "account-a", TargetAccountID: "account-b",
		IdempotencyKey: "request-cas",
		Phase:          domain.AgyAccountSwitchActivatingAccount, CreatedAt: now, UpdatedAt: now,
	}
	if _, _, err := st.CreateAgyAccountSwitch(ctx, sw); err != nil {
		t.Fatal(err)
	}
	sw.Phase = domain.AgyAccountSwitchCompleted
	sw.UpdatedAt = now.Add(time.Second)
	if ok, err := st.UpdateAgyAccountSwitch(ctx, sw, domain.AgyAccountSwitchActivatingAccount); err != nil || !ok {
		t.Fatalf("switch transition: ok=%v err=%v", ok, err)
	}
	if ok, err := st.UpdateAgyAccountSwitch(ctx, sw, domain.AgyAccountSwitchActivatingAccount); err != nil || ok {
		t.Fatalf("stale switch transition: ok=%v err=%v", ok, err)
	}
}
