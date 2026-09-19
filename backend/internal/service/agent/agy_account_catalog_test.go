package agent

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

const agyTestAccountID = "72d4db6e-da2c-414c-a6a9-fdbd09a006b6"

// snapshots is test-only inspection. Production callers use the catalog's
// scoped read methods instead of materializing every account snapshot.
func (c *agyAccountCatalog) snapshots() []domain.AgyAccountSnapshot {
	c.mu.RLock()
	defer c.mu.RUnlock()
	records := c.sortedRecordsLocked()
	out := make([]domain.AgyAccountSnapshot, 0, len(records))
	for _, record := range records {
		out = append(out, record.Snapshot)
	}
	return out
}

func agyCommitTestAccount(t *testing.T, catalog *agyAccountCatalog, pendingRoot, operationID string, observed ports.AgyAccountObservation) agyAccountRecord {
	t.Helper()
	return agyCommitTestAccountWithCredential(t, catalog, pendingRoot, operationID, []byte("opaque-agy-credential\x00\xff"), observed)
}

func agyCommitTestAccountWithCredential(t *testing.T, catalog *agyAccountCatalog, pendingRoot, operationID string, credential []byte, observed ports.AgyAccountObservation) agyAccountRecord {
	t.Helper()
	pendingDir, home, err := createPendingCredentialHome(pendingRoot, operationID)
	if err != nil {
		t.Fatal(err)
	}
	if err := writePrivateFileAtomic(filepath.Join(home, agyCredentialFilename), credential); err != nil {
		t.Fatal(err)
	}
	record, err := catalog.commitPending(pendingDir, observed)
	if err != nil {
		t.Fatal(err)
	}
	return record
}

// agyTestOAuthCredential builds a credential in the CLI's token-file shape: the
// account id and an email derived from it travel in the ID token's claims.
func agyTestOAuthCredential(accountID, accessToken string) []byte {
	data, err := json.Marshal(map[string]any{
		"token": map[string]string{
			"access_token":  accessToken,
			"token_type":    "Bearer",
			"refresh_token": "refresh-" + accessToken,
			"expiry":        "2030-01-01T00:00:00Z",
		},
		"auth_method": "oauth",
		"id_token":    agyTestIDToken(accountID),
	})
	if err != nil {
		panic(err)
	}
	return data
}

// agyTestIDToken is an unsigned JWT whose payload carries sub and email; the
// identity parser reads claims without verifying a signature.
func agyTestIDToken(accountID string) string {
	if accountID == "" {
		return ""
	}
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none","typ":"JWT"}`))
	payload, err := json.Marshal(map[string]any{"sub": accountID, "email": accountID + "@example.com", "email_verified": true})
	if err != nil {
		panic(err)
	}
	return header + "." + base64.RawURLEncoding.EncodeToString(payload) + "."
}

func TestParseAgyCredentialIdentity(t *testing.T) {
	for name, tc := range map[string]struct {
		credential []byte
		method     domain.AgyAuthMethod
		accountID  string
		wantError  bool
	}{
		"oauth account id": {credential: agyTestOAuthCredential("account-123", "access"), method: domain.AgyAuthMethodGoogle, accountID: "account-123"},
		"no id token":      {credential: []byte(`{"token":{"access_token":"access"}}`), method: domain.AgyAuthMethodGoogle},
		"workspace":        {credential: []byte(`{"token":{"refresh_token":"r"},"auth_method":"business"}`), method: domain.AgyAuthMethodGoogle},
		"malformed":        {credential: []byte(`{"token":`), wantError: true},
		"unsupported":      {credential: []byte(`{}`), wantError: true},
	} {
		t.Run(name, func(t *testing.T) {
			identity, err := parseAgyCredentialIdentity(tc.credential)
			if (err != nil) != tc.wantError {
				t.Fatalf("error = %v, wantError %v", err, tc.wantError)
			}
			if identity.Method != tc.method || identity.ProviderAccountID != tc.accountID {
				t.Fatalf("safe identity fields = (%q, %q)", identity.Method, identity.ProviderAccountID)
			}
			if name == "oauth account id" && (identity.Email == nil || *identity.Email != "account-123@example.com") {
				t.Fatalf("email claim = %v, want the ID token's email", identity.Email)
			}
		})
	}
}

func TestAgyAccountCatalogPersistsSafeProviderAccountID(t *testing.T) {
	root := filepath.Join(t.TempDir(), "accounts")
	pending := filepath.Join(filepath.Dir(root), "pending-accounts")
	catalog := newAgyAccountCatalog(root, nil)
	catalog.newID = func() string { return agyTestAccountID }
	record := agyCommitTestAccountWithCredential(t, catalog, pending, "b60a377d-da68-4a61-86f2-f31f04c571f2", agyTestOAuthCredential("provider-account", "access"), ports.AgyAccountObservation{
		Authentication: domain.AgentAuthenticationUnknown,
		Method:         domain.AgyAuthMethodGoogle,
	})
	if record.ProviderAccountID != "provider-account" {
		t.Fatalf("provider account id = %q", record.ProviderAccountID)
	}
	descriptor, err := readAgyAccountDescriptor(filepath.Join(root, agyTestAccountID, agyAccountDescriptorFilename))
	if err != nil {
		t.Fatal(err)
	}
	if descriptor.Version != agyAccountVersion || descriptor.ProviderAccountID != "provider-account" {
		t.Fatalf("descriptor = %#v", descriptor)
	}
}

func TestAgyAccountCatalogLazilyUpgradesLegacyDescriptorIdentity(t *testing.T) {
	root := filepath.Join(t.TempDir(), "accounts")
	pending := filepath.Join(filepath.Dir(root), "pending-accounts")
	catalog := newAgyAccountCatalog(root, nil)
	catalog.newID = func() string { return agyTestAccountID }
	credential := agyTestOAuthCredential("provider-account", "access")
	record := agyCommitTestAccountWithCredential(t, catalog, pending, "b60a377d-da68-4a61-86f2-f31f04c571f2", credential, ports.AgyAccountObservation{Method: domain.AgyAuthMethodGoogle})
	descriptorPath := filepath.Join(root, agyTestAccountID, agyAccountDescriptorFilename)
	descriptor, err := readAgyAccountDescriptor(descriptorPath)
	if err != nil {
		t.Fatal(err)
	}
	descriptor.Version = 1
	descriptor.ProviderAccountID = ""
	data, err := json.MarshalIndent(descriptor, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := writePrivateFileAtomic(descriptorPath, append(data, '\n')); err != nil {
		t.Fatal(err)
	}
	if err := catalog.refresh(); err != nil {
		t.Fatal(err)
	}
	if err := catalog.updateCredentialIdentity(context.Background(), record.Snapshot.ID, credential); err != nil {
		t.Fatal(err)
	}
	upgraded, err := readAgyAccountDescriptor(descriptorPath)
	if err != nil {
		t.Fatal(err)
	}
	if upgraded.Version != agyAccountVersion || upgraded.ProviderAccountID != "provider-account" {
		t.Fatalf("upgraded descriptor = %#v", upgraded)
	}
}

func TestAgyAccountCatalogCommitsStrictPrivateOpaqueSlot(t *testing.T) {
	root := filepath.Join(t.TempDir(), "accounts")
	pending := filepath.Join(filepath.Dir(root), "pending-accounts")
	catalog := newAgyAccountCatalog(root, nil)
	catalog.newID = func() string { return agyTestAccountID }
	catalog.now = func() time.Time { return time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC) }
	email := "person@example.com"
	record := agyCommitTestAccount(t, catalog, pending, "b60a377d-da68-4a61-86f2-f31f04c571f2", ports.AgyAccountObservation{
		Authentication: domain.AgentAuthenticationAuthorized,
		Method:         domain.AgyAuthMethodGoogle,
		Email:          &email,
	})
	if record.Snapshot.ID != agyTestAccountID || record.Snapshot.Label != email || record.Snapshot.Status != domain.AgyAccountStatusValid {
		t.Fatalf("account = %#v", record.Snapshot)
	}
	accountDir := filepath.Join(root, agyTestAccountID)
	for path, want := range map[string]os.FileMode{
		accountDir: 0o700,
		filepath.Join(accountDir, agyCredentialHomeDirectory):                        0o700,
		filepath.Join(accountDir, agyAccountDescriptorFilename):                      0o600,
		filepath.Join(accountDir, agyCredentialHomeDirectory, agyCredentialFilename): 0o600,
	} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Mode().Perm(); got != want {
			t.Errorf("%s mode = %o, want %o", path, got, want)
		}
	}
	data, err := os.ReadFile(filepath.Join(accountDir, agyAccountDescriptorFilename))
	if err != nil {
		t.Fatal(err)
	}
	var descriptor map[string]any
	if err := json.Unmarshal(data, &descriptor); err != nil {
		t.Fatal(err)
	}
	if len(descriptor) != 7 || descriptor["id"] != agyTestAccountID || descriptor["accountEmail"] != email {
		t.Fatalf("descriptor = %s", data)
	}
	for _, forbidden := range []string{"credential", "token", "plan", "capacity", "usage", "authUrl"} {
		if _, exists := descriptor[forbidden]; exists {
			t.Errorf("descriptor contains forbidden field %q", forbidden)
		}
	}
}

func TestAgyAccountCatalogAllowsDuplicateEmailAndOrdersByCreation(t *testing.T) {
	root := filepath.Join(t.TempDir(), "accounts")
	pending := filepath.Join(filepath.Dir(root), "pending-accounts")
	catalog := newAgyAccountCatalog(root, nil)
	ids := []string{agyTestAccountID, "bb1e9a5d-37ad-43f8-83bd-13de8168f8af"}
	times := []time.Time{
		time.Date(2026, 8, 31, 13, 0, 0, 0, time.UTC),
		time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC),
	}
	index := 0
	catalog.newID = func() string { return ids[index] }
	catalog.now = func() time.Time { return times[index] }
	email := "same@example.com"
	first := agyCommitTestAccount(t, catalog, pending, "b60a377d-da68-4a61-86f2-f31f04c571f2", ports.AgyAccountObservation{Authentication: domain.AgentAuthenticationAuthorized, Method: domain.AgyAuthMethodGoogle, Email: &email})
	index++
	second := agyCommitTestAccount(t, catalog, pending, "1c5de3ab-82d0-4a68-a06b-8495cdeab909", ports.AgyAccountObservation{Authentication: domain.AgentAuthenticationAuthorized, Method: domain.AgyAuthMethodGoogle, Email: &email})
	if err := catalog.refresh(); err != nil {
		t.Fatal(err)
	}
	snapshots := catalog.snapshots()
	if len(snapshots) != 2 || snapshots[0].ID != second.Snapshot.ID || snapshots[1].ID != first.Snapshot.ID {
		t.Fatalf("ordered accounts = %#v", snapshots)
	}
	if err := os.RemoveAll(filepath.Join(root, second.Snapshot.ID)); err != nil {
		t.Fatal(err)
	}
	if err := catalog.refresh(); err != nil {
		t.Fatal(err)
	}
	if got := catalog.snapshots(); len(got) != 1 || got[0].ID != first.Snapshot.ID {
		t.Fatalf("accounts after rediscovery = %#v", got)
	}
}

func TestAgyAccountCatalogSurfacesUnsafeAndMalformedSlotsWithoutMetadataLeak(t *testing.T) {
	root := filepath.Join(t.TempDir(), "accounts")
	if err := ensurePrivateDirectory(root); err != nil {
		t.Fatal(err)
	}
	id := agyTestAccountID
	dir := filepath.Join(root, id)
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	malformed := `{"version":1,"id":"` + id + `","source":"managed","authMethod":"chatgpt","accountEmail":"secret@example.com","createdAt":"2026-08-31T12:00:00Z","verifiedAt":"2026-08-31T12:00:00Z","token":"secret"}`
	if err := os.WriteFile(filepath.Join(dir, agyAccountDescriptorFilename), []byte(malformed), 0o600); err != nil {
		t.Fatal(err)
	}
	catalog := newAgyAccountCatalog(root, nil)
	if err := catalog.refresh(); err != nil {
		t.Fatal(err)
	}
	got := catalog.snapshots()
	if len(got) != 1 || got[0].Status != domain.AgyAccountStatusBroken || got[0].ReasonCode != domain.AgyAccountReasonDescriptorInvalid {
		t.Fatalf("broken account = %#v", got)
	}
	if got[0].Label == "secret@example.com" || got[0].AccountEmail != nil {
		t.Fatalf("malformed descriptor metadata leaked: %#v", got[0])
	}
}

func TestAgyAccountCatalogRejectsSymlinkedCredentialHome(t *testing.T) {
	root := filepath.Join(t.TempDir(), "accounts")
	pending := filepath.Join(filepath.Dir(root), "pending-accounts")
	catalog := newAgyAccountCatalog(root, nil)
	catalog.newID = func() string { return agyTestAccountID }
	record := agyCommitTestAccount(t, catalog, pending, "b60a377d-da68-4a61-86f2-f31f04c571f2", ports.AgyAccountObservation{Authentication: domain.AgentAuthenticationAuthorized, Method: domain.AgyAuthMethodGoogle})
	home := filepath.Join(root, record.Snapshot.ID, agyCredentialHomeDirectory)
	if err := os.RemoveAll(home); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(t.TempDir(), home); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if err := catalog.refresh(); err != nil {
		t.Fatal(err)
	}
	broken, _ := catalog.record(record.Snapshot.ID)
	if broken.Snapshot.Status != domain.AgyAccountStatusBroken || broken.Snapshot.ReasonCode != domain.AgyAccountReasonUnsafePath {
		t.Fatalf("symlinked account = %#v", broken.Snapshot)
	}
}

func TestAgyAccountCatalogRetainsSignedOutSlotAndReplacesItsCredential(t *testing.T) {
	root := filepath.Join(t.TempDir(), "accounts")
	pending := filepath.Join(filepath.Dir(root), "pending-accounts")
	catalog := newAgyAccountCatalog(root, nil)
	catalog.newID = func() string { return agyTestAccountID }
	email := "person@example.com"
	observation := ports.AgyAccountObservation{
		Authentication: domain.AgentAuthenticationAuthorized,
		Method:         domain.AgyAuthMethodGoogle,
		Email:          &email,
	}
	record := agyCommitTestAccount(t, catalog, pending, "b60a377d-da68-4a61-86f2-f31f04c571f2", observation)

	signedOut, err := catalog.markSignedOut(record.Snapshot.ID)
	if err != nil {
		t.Fatal(err)
	}
	if signedOut.Snapshot.Status != domain.AgyAccountStatusSignedOut || signedOut.Snapshot.Label != email || signedOut.Snapshot.Authentication.State != domain.AgentAuthenticationUnauthorized {
		t.Fatalf("signed-out account = %#v", signedOut.Snapshot)
	}
	if _, err := os.Stat(filepath.Join(record.Home, agyCredentialFilename)); !os.IsNotExist(err) {
		t.Fatalf("signed-out credential still exists: %v", err)
	}
	if err := catalog.refresh(); err != nil {
		t.Fatal(err)
	}
	rediscovered, _ := catalog.record(record.Snapshot.ID)
	if rediscovered.Snapshot.Status != domain.AgyAccountStatusSignedOut {
		t.Fatalf("rediscovered account = %#v", rediscovered.Snapshot)
	}

	reauthenticated, err := catalog.replaceCredential(context.Background(), record.Snapshot.ID, []byte("replacement-opaque-credential"), observation)
	if err != nil {
		t.Fatal(err)
	}
	if reauthenticated.Snapshot.ID != record.Snapshot.ID || reauthenticated.Snapshot.Status != domain.AgyAccountStatusValid || reauthenticated.Snapshot.Authentication.State != domain.AgentAuthenticationAuthorized {
		t.Fatalf("reauthenticated account = %#v", reauthenticated.Snapshot)
	}
	if snapshots := catalog.snapshots(); len(snapshots) != 1 {
		t.Fatalf("reauthentication created a duplicate account: %#v", snapshots)
	}
}

func TestAgyAccountCatalogDeletesSignedOutSlot(t *testing.T) {
	root := filepath.Join(t.TempDir(), "accounts")
	pending := filepath.Join(filepath.Dir(root), "pending-accounts")
	catalog := newAgyAccountCatalog(root, nil)
	catalog.newID = func() string { return agyTestAccountID }
	record := agyCommitTestAccount(t, catalog, pending, "b60a377d-da68-4a61-86f2-f31f04c571f2", ports.AgyAccountObservation{
		Authentication: domain.AgentAuthenticationAuthorized,
		Method:         domain.AgyAuthMethodGoogle,
	})
	if _, err := catalog.markSignedOut(record.Snapshot.ID); err != nil {
		t.Fatal(err)
	}
	providerCacheDir := filepath.Join(record.Home, "skills", ".system")
	if err := os.MkdirAll(providerCacheDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(record.Home, "models_cache.json"), []byte("provider cache"), 0o644); err != nil {
		t.Fatal(err)
	}

	removed := make(chan []string, 1)
	catalog.setOnRemoved(func(ids []string) { removed <- ids })
	if err := catalog.deleteSignedOut(record.Snapshot.ID); err != nil {
		t.Fatal(err)
	}

	if _, ok := catalog.record(record.Snapshot.ID); ok {
		t.Fatal("deleted account remains in the catalog")
	}
	if _, err := os.Stat(filepath.Join(root, record.Snapshot.ID)); !os.IsNotExist(err) {
		t.Fatalf("deleted account directory still exists: %v", err)
	}
	select {
	case ids := <-removed:
		if len(ids) != 1 || ids[0] != record.Snapshot.ID {
			t.Fatalf("removed account IDs = %#v", ids)
		}
	default:
		t.Fatal("account removal callback was not delivered")
	}
}
