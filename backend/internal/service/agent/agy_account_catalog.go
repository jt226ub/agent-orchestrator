package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

const (
	agyAccountDescriptorFilename = "account.json"
	agyCredentialHomeDirectory   = "credential-home" //nolint:gosec // directory name, not a credential value.
	// agyCredentialFilename is the CLI's token file relative to a HOME: the CLI
	// keys its config directory off HOME, so a managed credential home is a
	// home skeleton holding only this file (and whatever the CLI writes beside
	// it when run against that home).
	agyCredentialFilename = ".gemini/antigravity-cli/antigravity-oauth-token" //nolint:gosec // file name, not a credential value.
	agyAccountVersion     = 2
	maxAgyDescriptorBytes = 16 << 10
)

type agyAccountDescriptor struct {
	Version           int                     `json:"version"`
	ID                string                  `json:"id"`
	Source            domain.AgyAccountSource `json:"source"`
	AuthMethod        domain.AgyAuthMethod    `json:"authMethod"`
	AccountEmail      *string                 `json:"accountEmail,omitempty"`
	ProviderAccountID string                  `json:"providerAccountId,omitempty"`
	CreatedAt         time.Time               `json:"createdAt"`
	VerifiedAt        time.Time               `json:"verifiedAt"`
}

type agyAccountRecord struct {
	Snapshot          domain.AgyAccountSnapshot
	Home              string
	ProviderAccountID string
	CreatedAt         time.Time
	VerifiedAt        time.Time
	useSavedHome      bool
}

type agyAccountCatalog struct {
	root   string
	logger *slog.Logger
	now    func() time.Time
	newID  func() string

	mu        sync.RWMutex
	records   map[string]agyAccountRecord
	onRemoved func([]string)
}

func newAgyAccountCatalog(root string, logger *slog.Logger) *agyAccountCatalog {
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	return &agyAccountCatalog{
		root: root, logger: logger, now: func() time.Time { return time.Now().UTC() },
		newID: uuid.NewString, records: make(map[string]agyAccountRecord),
	}
}

func agyAccountLabel(email *string) string {
	if email != nil && safeAccountEmail(*email) {
		return strings.TrimSpace(*email)
	}
	return "Antigravity account"
}

func agyValidAccountAuthMethod(method domain.AgyAuthMethod) bool {
	switch method {
	case domain.AgyAuthMethodGoogle, domain.AgyAuthMethodOther, domain.AgyAuthMethodUnknown:
		return true
	default:
		return false
	}
}

func (c *agyAccountCatalog) recordsFor(ids []string) ([]agyAccountRecord, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if len(ids) == 0 {
		return c.sortedRecordsLocked(), nil
	}
	seen := make(map[string]struct{}, len(ids))
	records := make([]agyAccountRecord, 0, len(ids))
	for _, raw := range ids {
		id := strings.TrimSpace(raw)
		if _, duplicate := seen[id]; duplicate {
			continue
		}
		record, ok := c.records[id]
		if !ok {
			return nil, unknownAgyAccountError{id: id}
		}
		seen[id] = struct{}{}
		records = append(records, record)
	}
	sortAgyAccountRecords(records)
	return records, nil
}

func (c *agyAccountCatalog) record(id string) (agyAccountRecord, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	record, ok := c.records[id]
	return record, ok
}

func (c *agyAccountCatalog) updateSnapshot(id string, update func(*domain.AgyAccountSnapshot)) {
	c.mu.Lock()
	defer c.mu.Unlock()
	record, ok := c.records[id]
	if !ok {
		return
	}
	update(&record.Snapshot)
	c.records[id] = record
}

func (c *agyAccountCatalog) updateVerifiedDescriptor(ctx context.Context, id string, observation ports.AgyAccountObservation) error { //nolint:dupl // Codex and Antigravity keep separate account contracts by design (FORK.md).
	record, ok := c.record(id)
	if !ok || (record.Snapshot.Status != domain.AgyAccountStatusValid && record.Snapshot.Status != domain.AgyAccountStatusSignedOut) {
		return errors.New("agy account is unavailable")
	}
	if err := c.mutateDescriptor(ctx, id, func(descriptor *agyAccountDescriptor) error {
		if observation.Method != domain.AgyAuthMethodUnknown {
			descriptor.AuthMethod = observation.Method
		}
		if observation.Email != nil {
			descriptor.AccountEmail = observation.Email
		}
		if credential, credentialErr := readOpaqueCredential(filepath.Join(record.Home, agyCredentialFilename)); credentialErr == nil {
			if identity, identityErr := parseAgyCredentialIdentity(credential); identityErr == nil && identity.ProviderAccountID != "" {
				descriptor.ProviderAccountID = identity.ProviderAccountID
			}
		}
		descriptor.Version = agyAccountVersion
		descriptor.VerifiedAt = c.now()
		return nil
	}); err != nil {
		return err
	}
	c.updateSnapshot(id, func(snapshot *domain.AgyAccountSnapshot) {
		if observation.Method != domain.AgyAuthMethodUnknown {
			snapshot.AuthMethod = observation.Method
		}
		if observation.Email != nil {
			snapshot.AccountEmail = observation.Email
		}
		snapshot.Label = agyAccountLabel(snapshot.AccountEmail)
	})
	return nil
}

func (c *agyAccountCatalog) sortedRecordsLocked() []agyAccountRecord {
	records := make([]agyAccountRecord, 0, len(c.records))
	for _, record := range c.records {
		records = append(records, record)
	}
	sortAgyAccountRecords(records)
	return records
}

func sortAgyAccountRecords(records []agyAccountRecord) {
	sort.SliceStable(records, func(i, j int) bool {
		if !records[i].CreatedAt.Equal(records[j].CreatedAt) {
			return records[i].CreatedAt.Before(records[j].CreatedAt)
		}
		return records[i].Snapshot.ID < records[j].Snapshot.ID
	})
}

func (c *agyAccountCatalog) refresh() error {
	if c.root == "" {
		return errors.New("agy account storage is unavailable")
	}
	if err := ensurePrivateDirectory(c.root); err != nil {
		return fmt.Errorf("prepare Antigravity account catalog: %w", err)
	}
	entries, err := os.ReadDir(c.root)
	if err != nil {
		return fmt.Errorf("read Antigravity account catalog: %w", err)
	}
	next := make(map[string]agyAccountRecord)
	for _, entry := range entries {
		id := entry.Name()
		if !isCanonicalUUIDv4(id) {
			c.logger.Debug("ignored non-account Antigravity catalog entry")
			continue
		}
		record := c.readManaged(id)
		c.preserveObservation(&record)
		next[id] = record
	}
	c.replaceRecords(next)
	return nil
}

func (c *agyAccountCatalog) preserveObservation(record *agyAccountRecord) {
	c.mu.RLock()
	previous, ok := c.records[record.Snapshot.ID]
	c.mu.RUnlock()
	if !ok || previous.Snapshot.Status != record.Snapshot.Status ||
		(record.Snapshot.Status != domain.AgyAccountStatusValid && record.Snapshot.Status != domain.AgyAccountStatusSignedOut) {
		return
	}
	record.Snapshot.Authentication = previous.Snapshot.Authentication
	record.Snapshot.Capacity = previous.Snapshot.Capacity
}

func (c *agyAccountCatalog) replaceRecords(records map[string]agyAccountRecord) {
	c.mu.Lock()
	removed := make([]string, 0)
	for id := range c.records {
		if _, ok := records[id]; !ok {
			removed = append(removed, id)
		}
	}
	c.records = records
	onRemoved := c.onRemoved
	c.mu.Unlock()
	if onRemoved != nil && len(removed) > 0 {
		onRemoved(removed)
	}
}

func (c *agyAccountCatalog) setOnRemoved(callback func([]string)) {
	c.mu.Lock()
	c.onRemoved = callback
	c.mu.Unlock()
}

func (c *agyAccountCatalog) readManaged(id string) agyAccountRecord { //nolint:dupl // Codex and Antigravity keep separate account contracts by design (FORK.md).
	accountDir := filepath.Join(c.root, id)
	home := filepath.Join(accountDir, agyCredentialHomeDirectory)
	broken := func(code, reason string) agyAccountRecord {
		return agyAccountRecord{Home: home, Snapshot: domain.AgyAccountSnapshot{
			ID: id, Label: "Unavailable Antigravity account", Source: domain.AgyAccountSourceManaged,
			Status: domain.AgyAccountStatusBroken, ReasonCode: code, Reason: reason,
			Authentication: uncheckedAuthentication(), AuthMethod: domain.AgyAuthMethodUnknown,
			Capacity: unavailableAgyCapacity(),
		}}
	}
	if err := validateCodexDirectory(accountDir, true); err != nil {
		return broken(domain.AgyAccountReasonUnsafePath, "This Antigravity account has an unsafe directory layout.")
	}
	descriptor, err := readAgyAccountDescriptor(filepath.Join(accountDir, agyAccountDescriptorFilename))
	if err != nil || descriptor.ID != id || (descriptor.Version != 1 && descriptor.Version != agyAccountVersion) || descriptor.Source != domain.AgyAccountSourceManaged || !agyValidAccountAuthMethod(descriptor.AuthMethod) || (descriptor.AccountEmail != nil && !safeAccountEmail(*descriptor.AccountEmail)) || (descriptor.ProviderAccountID != "" && !safeProviderAccountID(descriptor.ProviderAccountID)) || descriptor.CreatedAt.IsZero() || descriptor.VerifiedAt.IsZero() {
		return broken(domain.AgyAccountReasonDescriptorInvalid, "This Antigravity account descriptor is invalid.")
	}
	_, err = os.Lstat(home)
	if errors.Is(err, os.ErrNotExist) {
		return broken(domain.AgyAccountReasonHomeMissing, "This Antigravity account credential home is missing.")
	}
	if err != nil || validateCodexDirectory(home, true) != nil || !pathWithin(c.root, home) {
		return broken(domain.AgyAccountReasonUnsafePath, "This Antigravity account has an unsafe credential home.")
	}
	credentialPath := filepath.Join(home, agyCredentialFilename)
	credentialState, err := inspectCodexFile(credentialPath, true)
	if errors.Is(err, os.ErrNotExist) {
		return broken(domain.AgyAccountReasonUnsafePath, "This Antigravity account credential is unavailable or unsafe.")
	}
	if err == nil && !credentialState.exists {
		return agyAccountRecord{Home: canonicalPath(home), ProviderAccountID: descriptor.ProviderAccountID, CreatedAt: descriptor.CreatedAt, VerifiedAt: descriptor.VerifiedAt, Snapshot: domain.AgyAccountSnapshot{
			ID: id, Label: agyAccountLabel(descriptor.AccountEmail),
			Source: domain.AgyAccountSourceManaged, Status: domain.AgyAccountStatusSignedOut,
			ReasonCode: domain.AgyAccountReasonSignedOut, Reason: "This Antigravity account is signed out.",
			Authentication: signedOutAuthentication(c.now(), "Sign in again to use this Antigravity account."), AuthMethod: descriptor.AuthMethod,
			AccountEmail: descriptor.AccountEmail, Capacity: unavailableAgyCapacity(), CreatedAt: descriptor.CreatedAt,
		}}
	}
	if err != nil {
		return broken(domain.AgyAccountReasonUnsafePath, "This Antigravity account credential is unavailable or unsafe.")
	}
	return agyAccountRecord{Home: canonicalPath(home), ProviderAccountID: descriptor.ProviderAccountID, CreatedAt: descriptor.CreatedAt, VerifiedAt: descriptor.VerifiedAt, Snapshot: domain.AgyAccountSnapshot{
		ID: id, Label: agyAccountLabel(descriptor.AccountEmail),
		Source: domain.AgyAccountSourceManaged, Status: domain.AgyAccountStatusValid,
		ReasonCode: domain.AgyAccountReasonValid, Reason: "This Antigravity account is available.",
		Authentication: uncheckedAuthentication(), AuthMethod: descriptor.AuthMethod,
		AccountEmail: descriptor.AccountEmail, Capacity: uncheckedAgyAccountCapacity(), CreatedAt: descriptor.CreatedAt,
	}}
}

func (c *agyAccountCatalog) updateCredentialIdentity(ctx context.Context, id string, credential []byte) error {
	record, ok := c.record(id)
	if !ok || record.Snapshot.Status != domain.AgyAccountStatusValid {
		return errors.New("agy account is unavailable")
	}
	identity, supported := inspectAgyCredentialIdentity(credential)
	if !supported {
		// Older opaque credentials can still be associated by exact byte match,
		// but they do not contain safe local identity metadata to persist.
		return nil
	}
	return c.mutateDescriptor(ctx, id, func(descriptor *agyAccountDescriptor) error {
		descriptor.Version = agyAccountVersion
		if identity.Method != domain.AgyAuthMethodUnknown {
			descriptor.AuthMethod = identity.Method
		}
		if identity.ProviderAccountID != "" {
			descriptor.ProviderAccountID = identity.ProviderAccountID
		}
		if descriptor.AccountEmail == nil && identity.Email != nil {
			descriptor.AccountEmail = identity.Email
		}
		return nil
	})
}

func (c *agyAccountCatalog) mutateDescriptor(ctx context.Context, id string, mutate func(*agyAccountDescriptor) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	descriptorPath := filepath.Join(c.root, id, agyAccountDescriptorFilename)
	descriptor, err := readAgyAccountDescriptor(descriptorPath)
	if err != nil {
		return err
	}
	if err := mutate(&descriptor); err != nil {
		return err
	}
	data, err := json.MarshalIndent(descriptor, "", "  ")
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return writePrivateFileAtomic(descriptorPath, append(data, '\n'))
}

func (c *agyAccountCatalog) replaceCredential(ctx context.Context, id string, credential []byte, observation ports.AgyAccountObservation) (agyAccountRecord, error) {
	record, ok := c.record(id)
	if !ok || (record.Snapshot.Status != domain.AgyAccountStatusValid && record.Snapshot.Status != domain.AgyAccountStatusSignedOut) {
		return agyAccountRecord{}, errors.New("agy account is unavailable")
	}
	if len(credential) == 0 {
		return agyAccountRecord{}, errors.New("agy account credential is empty")
	}
	credentialPath := filepath.Join(record.Home, agyCredentialFilename)
	previous, previousErr := readOpaqueCredential(credentialPath)
	if previousErr != nil && !errors.Is(previousErr, os.ErrNotExist) {
		return agyAccountRecord{}, previousErr
	}
	if err := writePrivateFileAtomic(credentialPath, credential); err != nil {
		return agyAccountRecord{}, err
	}
	if err := c.updateVerifiedDescriptor(ctx, id, observation); err != nil {
		if previousErr == nil {
			_ = writePrivateFileAtomic(credentialPath, previous)
		} else {
			_ = os.Remove(credentialPath)
		}
		return agyAccountRecord{}, err
	}
	refreshed := c.readManaged(id)
	if refreshed.Snapshot.Status != domain.AgyAccountStatusValid {
		return agyAccountRecord{}, errors.New("updated Antigravity account failed validation")
	}
	refreshed.Snapshot.Authentication = agyAccountAuthenticationObservation(c.now(), observation.Authentication)
	c.mu.Lock()
	c.records[id] = refreshed
	c.mu.Unlock()
	return refreshed, nil
}

func (c *agyAccountCatalog) markSignedOut(id string) (agyAccountRecord, error) {
	record, ok := c.record(id)
	if !ok || (record.Snapshot.Status != domain.AgyAccountStatusValid && record.Snapshot.Status != domain.AgyAccountStatusSignedOut) {
		return agyAccountRecord{}, errors.New("agy account is unavailable")
	}
	credentialPath := filepath.Join(record.Home, agyCredentialFilename)
	if err := removePrivateCredential(credentialPath); err != nil && !codexFileMutationCommitted(err) {
		return agyAccountRecord{}, err
	}
	refreshed := c.readManaged(id)
	if refreshed.Snapshot.Status != domain.AgyAccountStatusSignedOut {
		return agyAccountRecord{}, errors.New("signed-out Antigravity account failed validation")
	}
	c.mu.Lock()
	c.records[id] = refreshed
	c.mu.Unlock()
	return refreshed, nil
}

func (c *agyAccountCatalog) deleteSignedOut(id string) error { //nolint:dupl // Codex and Antigravity keep separate account contracts by design (FORK.md).
	record, ok := c.record(id)
	if !ok || record.Snapshot.Status != domain.AgyAccountStatusSignedOut {
		return errors.New("agy account is not signed out")
	}
	accountDir := filepath.Join(c.root, id)
	if canonicalPath(filepath.Dir(record.Home)) != canonicalPath(accountDir) || !pathWithin(c.root, accountDir) {
		return errors.New("agy account has an unsafe directory layout")
	}
	current := c.readManaged(id)
	if current.Snapshot.Status != domain.AgyAccountStatusSignedOut {
		return errors.New("agy account is not safely deletable")
	}
	// Native Agy account reads can create caches, SQLite files, skills, and
	// temporary directories beside auth.json. The account root and credential
	// home were both revalidated above; RemoveAll deletes this exact AO-owned
	// slot and unlinks any nested symlink without following its target.
	if err := os.RemoveAll(accountDir); err != nil {
		return err
	}
	if err := syncDirectory(c.root); err != nil {
		return err
	}
	c.mu.Lock()
	delete(c.records, id)
	onRemoved := c.onRemoved
	c.mu.Unlock()
	if onRemoved != nil {
		onRemoved([]string{id})
	}
	return nil
}

// discardCommitted removes a just-created account when a compound login commit
// cannot finish. It is intentionally narrower than user-facing deletion: the
// caller must still be inside the account mutation gate and supply an exact
// catalog ID.
func (c *agyAccountCatalog) discardCommitted(id string) error {
	if !isCanonicalUUIDv4(id) {
		return errors.New("invalid Antigravity account id")
	}
	accountDir := filepath.Join(c.root, id)
	if !pathWithin(c.root, accountDir) || canonicalPath(filepath.Dir(accountDir)) != canonicalPath(c.root) {
		return errors.New("agy account has an unsafe directory layout")
	}
	if err := os.RemoveAll(accountDir); err != nil {
		return err
	}
	if err := syncDirectory(c.root); err != nil {
		return err
	}
	c.mu.Lock()
	delete(c.records, id)
	c.mu.Unlock()
	return nil
}

func readAgyAccountDescriptor(path string) (agyAccountDescriptor, error) {
	data, _, err := readCodexFileState(path, false)
	if err != nil || len(data) > maxAgyDescriptorBytes || !utf8.Valid(data) {
		return agyAccountDescriptor{}, errors.New("descriptor is not a safe regular file")
	}
	var descriptor agyAccountDescriptor
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&descriptor); err != nil {
		return agyAccountDescriptor{}, err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return agyAccountDescriptor{}, errors.New("descriptor contains trailing data")
	}
	return descriptor, nil
}

func (c *agyAccountCatalog) commitPending(pendingDir string, observation ports.AgyAccountObservation) (agyAccountRecord, error) { //nolint:dupl // Codex and Antigravity keep separate account contracts by design (FORK.md).
	if err := ensurePrivateDirectory(c.root); err != nil {
		return agyAccountRecord{}, err
	}
	id := c.newID()
	if !isCanonicalUUIDv4(id) {
		return agyAccountRecord{}, errors.New("generated invalid Antigravity account id")
	}
	createdAt := c.now()
	providerAccountID := ""
	credentialPath := filepath.Join(pendingDir, agyCredentialHomeDirectory, agyCredentialFilename)
	if credential, credentialErr := readOpaqueCredential(credentialPath); credentialErr == nil {
		if identity, identityErr := parseAgyCredentialIdentity(credential); identityErr == nil {
			providerAccountID = identity.ProviderAccountID
			if observation.Method == domain.AgyAuthMethodUnknown {
				observation.Method = identity.Method
			}
			// The CLI reports no identity; the ID token's email is the label.
			if observation.Email == nil {
				observation.Email = identity.Email
			}
		}
	}
	descriptor := agyAccountDescriptor{
		Version: agyAccountVersion, ID: id, Source: domain.AgyAccountSourceManaged,
		AuthMethod: observation.Method, AccountEmail: observation.Email, ProviderAccountID: providerAccountID,
		CreatedAt: createdAt, VerifiedAt: createdAt,
	}
	data, err := json.MarshalIndent(descriptor, "", "  ")
	if err != nil {
		return agyAccountRecord{}, err
	}
	descriptorPath := filepath.Join(pendingDir, agyAccountDescriptorFilename)
	if err := writePrivateFileAtomic(descriptorPath, append(data, '\n')); err != nil {
		return agyAccountRecord{}, fmt.Errorf("write Antigravity account descriptor: %w", err)
	}
	target := filepath.Join(c.root, id)
	if err := os.Rename(pendingDir, target); err != nil {
		return agyAccountRecord{}, fmt.Errorf("commit Antigravity account: %w", err)
	}
	if err := syncDirectory(c.root); err != nil {
		return agyAccountRecord{}, err
	}
	record := c.readManaged(id)
	if record.Snapshot.Status != domain.AgyAccountStatusValid {
		return agyAccountRecord{}, errors.New("committed Antigravity account failed validation")
	}
	c.mu.Lock()
	c.records[id] = record
	c.mu.Unlock()
	return record, nil
}

func agyCreatePendingCredentialHome(pendingRoot, operationID string) (string, string, error) {
	if !isCanonicalUUIDv4(operationID) {
		return "", "", errors.New("invalid login operation id")
	}
	if err := ensurePrivateDirectory(pendingRoot); err != nil {
		return "", "", err
	}
	pendingDir := filepath.Join(pendingRoot, operationID)
	if err := os.Mkdir(pendingDir, 0o700); err != nil {
		return "", "", err
	}
	if err := protectCodexPrivateDirectory(pendingDir); err != nil {
		_ = os.RemoveAll(pendingDir)
		return "", "", err
	}
	home := filepath.Join(pendingDir, agyCredentialHomeDirectory)
	if err := os.Mkdir(home, 0o700); err != nil {
		_ = os.RemoveAll(pendingDir)
		return "", "", err
	}
	if err := protectCodexPrivateDirectory(home); err != nil {
		_ = os.RemoveAll(pendingDir)
		return "", "", err
	}
	// The CLI writes its token under <home>/.gemini/antigravity-cli; create
	// that path private so the credential's directory is ours, not the CLI's.
	if err := os.MkdirAll(filepath.Dir(filepath.Join(home, agyCredentialFilename)), 0o700); err != nil {
		_ = os.RemoveAll(pendingDir)
		return "", "", err
	}
	return pendingDir, home, nil
}

func agyCleanupPendingCredentialHomes(pendingRoot string) error { //nolint:dupl // Codex and Antigravity keep separate account contracts by design (FORK.md).
	if err := ensurePrivateDirectory(pendingRoot); err != nil {
		return err
	}
	entries, err := os.ReadDir(pendingRoot)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		path := filepath.Join(pendingRoot, entry.Name())
		info, statErr := os.Lstat(path)
		if statErr != nil {
			return statErr
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			if removeErr := os.Remove(path); removeErr != nil {
				return removeErr
			}
			continue
		}
		if !isCanonicalUUIDv4(entry.Name()) {
			// This is a private transient root, not the durable account catalog.
			// A crash may leave a non-UUID staging directory behind. Remove only
			// an owned, non-symlinked directory rooted directly below this private
			// parent; never follow an unsafe entry.
			if validateCodexDirectory(path, true) != nil {
				return errors.New("unsafe Antigravity staging directory owner")
			}
			if removeErr := os.RemoveAll(path); removeErr != nil {
				return removeErr
			}
			continue
		}
		if validateCodexDirectory(path, true) != nil {
			return errors.New("unsafe Antigravity staging directory owner")
		}
		if removeErr := os.RemoveAll(path); removeErr != nil {
			return removeErr
		}
	}
	return syncDirectory(pendingRoot)
}

type unknownAgyAccountError struct{ id string }

func (e unknownAgyAccountError) Error() string { return "unknown Antigravity account id" }
