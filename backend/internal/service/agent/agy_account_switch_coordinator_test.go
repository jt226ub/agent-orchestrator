package agent

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/agyops"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

type agyCoordinatorCredentialFake struct {
	mu              sync.Mutex
	source          domain.AgyAccountSwitchSource
	installedTarget bool
	inspectionState domain.AgyAccountSwitchInstallationState
	confirmErrs     []error
	confirmStarted  chan struct{}
	activateErr     error
	calls           []string
}

func (f *agyCoordinatorCredentialFake) call(value string) {
	f.mu.Lock()
	f.calls = append(f.calls, value)
	f.mu.Unlock()
}

func (f *agyCoordinatorCredentialFake) WaitAgyAccountStoreReady(context.Context) error {
	f.call("store")
	return nil
}
func (f *agyCoordinatorCredentialFake) EnsureAgyDeviceAccountReconciled(context.Context) error {
	f.call("reconcile")
	return nil
}
func (f *agyCoordinatorCredentialFake) BeginAgyAccountMutation(context.Context) error {
	f.call("begin")
	return nil
}
func (f *agyCoordinatorCredentialFake) EndAgyAccountMutation() { f.call("end") }
func (f *agyCoordinatorCredentialFake) PrepareAgyAccountForSwitch(_ context.Context, switchID, targetID string) (domain.AgyAccountSwitchSource, error) {
	f.call("prepare:" + switchID + ":" + targetID)
	return f.source, nil
}
func (f *agyCoordinatorCredentialFake) InspectAgyAccountSwitch(_ context.Context, switchID string, _ domain.AgyAccountSwitchSourceKind, _, targetID string) (domain.AgyAccountSwitchInstallationState, error) {
	f.call("confirm:" + switchID + ":" + targetID)
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.confirmStarted != nil {
		select {
		case f.confirmStarted <- struct{}{}:
		default:
		}
	}
	if len(f.confirmErrs) > 0 {
		err := f.confirmErrs[0]
		f.confirmErrs = f.confirmErrs[1:]
		if err != nil {
			return "", err
		}
	}
	if !f.installedTarget {
		if f.inspectionState != "" {
			return f.inspectionState, nil
		}
		return domain.AgyAccountSwitchExternalCredential, nil
	}
	return domain.AgyAccountSwitchTargetInstalled, nil
}
func (f *agyCoordinatorCredentialFake) ActivatePreparedAgyAccountSwitch(_ context.Context, _ domain.AgyAccountSwitchSourceKind, switchID, targetID string) error {
	f.call("activate:" + switchID + ":" + targetID)
	if f.activateErr != nil {
		return f.activateErr
	}
	f.mu.Lock()
	f.installedTarget = true
	f.mu.Unlock()
	return nil
}
func (f *agyCoordinatorCredentialFake) CleanupAgyAccountSwitch(_ context.Context, switchID string) error {
	f.call("cleanup:" + switchID)
	return nil
}
func (f *agyCoordinatorCredentialFake) CleanupInactiveAgyAccountSwitches(_ context.Context, activeSwitchID string) error {
	f.call("cleanup-inactive:" + activeSwitchID)
	return nil
}

type agyCoordinatorSwitchStoreFake struct {
	mu      sync.Mutex
	record  domain.AgyAccountSwitch
	readErr error
}

func (s *agyCoordinatorSwitchStoreFake) CreateAgyAccountSwitch(_ context.Context, record domain.AgyAccountSwitch) (domain.AgyAccountSwitch, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.record.ID != "" {
		return s.record, false, nil
	}
	s.record = record
	return record, true, nil
}
func (s *agyCoordinatorSwitchStoreFake) GetAgyAccountSwitch(_ context.Context, id string) (domain.AgyAccountSwitch, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.record, s.record.ID == id, nil
}
func (s *agyCoordinatorSwitchStoreFake) GetAgyAccountSwitchByIdempotency(_ context.Context, key string) (domain.AgyAccountSwitch, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.record, s.record.IdempotencyKey == key && key != "", nil
}
func (s *agyCoordinatorSwitchStoreFake) GetActiveAgyAccountSwitch(context.Context) (domain.AgyAccountSwitch, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.readErr != nil {
		return domain.AgyAccountSwitch{}, false, s.readErr
	}
	return s.record, s.record.ID != "" && !s.record.Phase.Terminal(), nil
}
func (s *agyCoordinatorSwitchStoreFake) UpdateAgyAccountSwitch(_ context.Context, record domain.AgyAccountSwitch, expected domain.AgyAccountSwitchPhase) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.record.ID != "" && s.record.Phase != expected {
		return false, nil
	}
	s.record = record
	return true, nil
}

func TestAgyAccountSwitchCoordinatorCompletesLocalCredentialSwitch(t *testing.T) {
	credentials := &agyCoordinatorCredentialFake{source: domain.AgyAccountSwitchSource{Kind: domain.AgyAccountSwitchSourceManaged, AccountID: "source"}}
	store := &agyCoordinatorSwitchStoreFake{}
	coordinator := newAgyAccountSwitchCoordinator(context.Background(), credentials, store, agyops.NewGate(), time.Now, nil)

	if _, err := coordinator.StartAgyAccountSwitch(context.Background(), ports.AgyAccountSwitchConfig{TargetAccountID: "target", IdempotencyKey: "request-1"}); err != nil {
		t.Fatal(err)
	}
	waitCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := coordinator.Wait(waitCtx); err != nil {
		t.Fatal(err)
	}

	store.mu.Lock()
	completed := store.record
	store.mu.Unlock()
	if completed.Phase != domain.AgyAccountSwitchCompleted {
		t.Fatalf("phase = %q, want completed", completed.Phase)
	}
	credentials.mu.Lock()
	calls := append([]string(nil), credentials.calls...)
	credentials.mu.Unlock()
	for _, prefix := range []string{"store", "begin", "prepare:", "activate:", "confirm:", "cleanup:", "end", "reconcile"} {
		if !slices.ContainsFunc(calls, func(call string) bool { return strings.HasPrefix(call, prefix) }) {
			t.Fatalf("calls = %v, missing %q", calls, prefix)
		}
	}
}

func TestAgySwitchAdmissionCancelledBeforeWorkerLeavesNoPendingJournal(t *testing.T) {
	credentials := &agyCoordinatorCredentialFake{source: domain.AgyAccountSwitchSource{Kind: domain.AgyAccountSwitchSourceManaged, AccountID: "source"}}
	store := &agyCoordinatorSwitchStoreFake{}
	coordinator := newAgyAccountSwitchCoordinator(context.Background(), credentials, store, agyops.NewGate(), time.Now, nil)
	if err := coordinator.Wait(context.Background()); err != nil {
		t.Fatal(err)
	}

	_, err := coordinator.StartAgyAccountSwitch(context.Background(), ports.AgyAccountSwitchConfig{TargetAccountID: "target", IdempotencyKey: "request-cancelled"})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context canceled", err)
	}
	store.mu.Lock()
	settled := store.record
	store.mu.Unlock()
	if settled.Phase != domain.AgyAccountSwitchFailed || settled.FailureCode != "switch_cancelled_before_mutation" {
		t.Fatalf("switch = (%q,%q), want terminal pre-mutation cancellation", settled.Phase, settled.FailureCode)
	}
	credentials.mu.Lock()
	calls := append([]string(nil), credentials.calls...)
	credentials.mu.Unlock()
	if slices.ContainsFunc(calls, func(call string) bool { return strings.HasPrefix(call, "activate:") }) {
		t.Fatalf("cancelled admission mutated credentials: %v", calls)
	}
	if !slices.ContainsFunc(calls, func(call string) bool { return strings.HasPrefix(call, "cleanup:") }) {
		t.Fatalf("cancelled admission did not clean staging: %v", calls)
	}
}

func TestAgyInterruptedSwitchWithInstalledTargetCompletesLocally(t *testing.T) {
	credentials := &agyCoordinatorCredentialFake{installedTarget: true}
	store := &agyCoordinatorSwitchStoreFake{record: domain.AgyAccountSwitch{
		ID: "switch-1", SourceKind: domain.AgyAccountSwitchSourceManaged, SourceAccountID: "source",
		TargetAccountID: "target", Phase: domain.AgyAccountSwitchActivatingAccount,
	}}
	coordinator := newAgyAccountSwitchCoordinator(context.Background(), credentials, store, agyops.NewGate(), time.Now, nil)
	if err := coordinator.ReconcileAgyAccountSwitches(context.Background()); err != nil {
		t.Fatal(err)
	}
	waitCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := coordinator.Wait(waitCtx); err != nil {
		t.Fatal(err)
	}
	if store.record.Phase != domain.AgyAccountSwitchCompleted {
		t.Fatalf("phase = %q, want completed", store.record.Phase)
	}
}

func TestAgyInterruptedSwitchConclusiveNonTargetStatesCancelWithoutWriting(t *testing.T) {
	for _, state := range []domain.AgyAccountSwitchInstallationState{
		domain.AgyAccountSwitchSourceInstalled,
		domain.AgyAccountSwitchCredentialMissing,
		domain.AgyAccountSwitchExternalCredential,
	} {
		t.Run(string(state), func(t *testing.T) {
			credentials := &agyCoordinatorCredentialFake{inspectionState: state}
			store := &agyCoordinatorSwitchStoreFake{record: domain.AgyAccountSwitch{
				ID: "switch-1", SourceKind: domain.AgyAccountSwitchSourceDevice,
				TargetAccountID: "target", Phase: domain.AgyAccountSwitchRequested,
			}}
			coordinator := newAgyAccountSwitchCoordinator(context.Background(), credentials, store, agyops.NewGate(), time.Now, nil)
			if err := coordinator.ReconcileAgyAccountSwitches(context.Background()); err != nil {
				t.Fatal(err)
			}
			waitCtx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			if err := coordinator.Wait(waitCtx); err != nil {
				t.Fatal(err)
			}
			if store.record.Phase != domain.AgyAccountSwitchFailed || store.record.FailureCode != "interrupted_switch_cancelled" {
				t.Fatalf("switch = (%q,%q), want terminal cancellation", store.record.Phase, store.record.FailureCode)
			}
			credentials.mu.Lock()
			defer credentials.mu.Unlock()
			if slices.ContainsFunc(credentials.calls, func(call string) bool { return strings.HasPrefix(call, "activate:") }) {
				t.Fatalf("recovery resumed credential mutation: %v", credentials.calls)
			}
		})
	}
}

func TestAgyInterruptedSwitchRetainsFenceAcrossTransientInspectionFailure(t *testing.T) {
	transient := errors.New("temporary credential read failure")
	credentials := &agyCoordinatorCredentialFake{
		installedTarget: true,
		confirmErrs:     []error{transient, nil},
		confirmStarted:  make(chan struct{}, 2),
	}
	store := &agyCoordinatorSwitchStoreFake{record: domain.AgyAccountSwitch{
		ID: "switch-1", SourceKind: domain.AgyAccountSwitchSourceManaged, SourceAccountID: "source",
		TargetAccountID: "target", Phase: domain.AgyAccountSwitchActivatingAccount,
	}}
	gate := agyops.NewGate()
	coordinator := newAgyAccountSwitchCoordinator(context.Background(), credentials, store, gate, time.Now, nil)
	if err := coordinator.ReconcileAgyAccountSwitches(context.Background()); err != nil {
		t.Fatal(err)
	}
	select {
	case <-credentials.confirmStarted:
	case <-time.After(time.Second):
		t.Fatal("first credential inspection did not run")
	}
	if !gate.ExclusivePendingOrHeld() {
		t.Fatal("transient inspection released the Agy operation fence")
	}
	store.mu.Lock()
	phaseAfterTransient := store.record.Phase
	store.mu.Unlock()
	if phaseAfterTransient.Terminal() {
		t.Fatalf("transient inspection terminally settled switch as %q", phaseAfterTransient)
	}

	waitCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := coordinator.Wait(waitCtx); err != nil {
		t.Fatal(err)
	}
	if store.record.Phase != domain.AgyAccountSwitchCompleted {
		t.Fatalf("phase = %q, want completed after retry", store.record.Phase)
	}
}

func TestAgyRecoveryReadFailureDoesNotClaimDaemonBlockingRecovery(t *testing.T) {
	credentials := &agyCoordinatorCredentialFake{}
	store := &agyCoordinatorSwitchStoreFake{readErr: errors.New("database busy")}
	ctx, cancel := context.WithCancel(context.Background())
	gate := agyops.NewGate()
	coordinator := newAgyAccountSwitchCoordinator(ctx, credentials, store, gate, time.Now, nil)
	if err := coordinator.ReconcileAgyAccountSwitches(context.Background()); err == nil {
		t.Fatal("expected the initial diagnostic error")
	}
	if !gate.ExclusivePendingOrHeld() {
		t.Fatal("startup recovery read failure left Agy launches unfenced")
	}
	cancel()
	waitCtx, waitCancel := context.WithTimeout(context.Background(), time.Second)
	defer waitCancel()
	if err := coordinator.Wait(waitCtx); err != nil {
		t.Fatal(err)
	}
	if gate.ExclusivePendingOrHeld() {
		t.Fatal("shutdown did not release the startup recovery fence")
	}
}

func TestAgyRecoveryWithoutPendingJournalReleasesStartupFence(t *testing.T) {
	credentials := &agyCoordinatorCredentialFake{}
	store := &agyCoordinatorSwitchStoreFake{}
	gate := agyops.NewGate()
	coordinator := newAgyAccountSwitchCoordinator(context.Background(), credentials, store, gate, time.Now, nil)

	if err := coordinator.ReconcileAgyAccountSwitches(context.Background()); err != nil {
		t.Fatal(err)
	}
	if gate.ExclusivePendingOrHeld() {
		t.Fatal("no pending journal left the startup fence held")
	}
	credentials.mu.Lock()
	defer credentials.mu.Unlock()
	if !slices.Contains(credentials.calls, "cleanup-inactive:") {
		t.Fatalf("inactive staging was not cleaned: %v", credentials.calls)
	}
}
