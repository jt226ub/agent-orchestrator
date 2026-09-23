package agent

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// fakeAgyKeychain stands in for the item agy 1.2.9 signs in from.
type fakeAgyKeychain struct {
	value     []byte
	present   bool
	dropWrite bool // the write succeeds but the item does not keep the value
	writes    int
}

func (k *fakeAgyKeychain) read(context.Context) ([]byte, bool, error) {
	return append([]byte(nil), k.value...), k.present, nil
}

func (k *fakeAgyKeychain) write(_ context.Context, credential []byte) error {
	k.writes++
	if !k.dropWrite {
		k.value = append([]byte(nil), credential...)
	}
	return nil
}

// Only the user's own home may reach the real keychain. Every manager a test
// builds -- and any scratch home -- must get the no-op, or an activation test
// would overwrite the developer's real Antigravity sign-in.
func TestDefaultAgyKeychainNeverReachesTheRealKeychainForAnotherHome(t *testing.T) {
	if _, ok := defaultAgyKeychain(t.TempDir()).(noAgyKeychain); !ok {
		t.Fatal("a temporary home got the real keychain")
	}
	if _, ok := defaultAgyKeychain("").(noAgyKeychain); !ok {
		t.Fatal("an empty home got the real keychain")
	}
	manager := newTestAgyAccountManager(t, nil, nil)
	if _, ok := manager.keychain.(noAgyKeychain); !ok {
		t.Fatalf("test manager keychain = %T, want the no-op", manager.keychain)
	}
}

func TestDecodeAgyKeychainValue(t *testing.T) {
	credential := []byte(`{"token":{"access_token":"a"},"id_token":"x.y.z"}`)
	for _, tt := range []struct{ name, value string }{
		{"base64", agyKeychainBase64Prefix + base64.StdEncoding.EncodeToString(credential)},
		{"hex", agyKeychainHexPrefix + hex.EncodeToString(credential)},
		{"raw", string(credential)},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, err := decodeAgyKeychainValue(tt.value)
			if err != nil || !bytes.Equal(got, credential) {
				t.Fatalf("decode = %q, %v; want %q", got, err, credential)
			}
		})
	}
}

// Reads and writes go through /usr/bin/security exactly as agy's own keyring
// does, and the secret travels on stdin, never in the argument list.
func TestSecurityAgyKeychainSpeaksAgysProtocol(t *testing.T) {
	credential := []byte(`{"token":{"access_token":"secret"},"id_token":"x.y.z"}`)
	encoded := agyKeychainBase64Prefix + base64.StdEncoding.EncodeToString(credential)
	var calls [][]string
	var stdins []string
	keychain := securityAgyKeychain{run: func(_ context.Context, stdin []byte, args ...string) ([]byte, int, error) {
		calls = append(calls, args)
		stdins = append(stdins, string(stdin))
		if args[0] == "find-generic-password" {
			return []byte(encoded + "\n"), 0, nil
		}
		return nil, 0, nil
	}}

	got, present, err := keychain.read(context.Background())
	if err != nil || !present || !bytes.Equal(got, credential) {
		t.Fatalf("read = %q, %v, %v", got, present, err)
	}
	if want := []string{"find-generic-password", "-s", "gemini", "-a", "antigravity", "-w"}; !slices.Equal(calls[0], want) {
		t.Fatalf("read args = %v, want %v", calls[0], want)
	}

	if err := keychain.write(context.Background(), credential); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(calls[1], []string{"-i"}) {
		t.Fatalf("write args = %v, want only -i", calls[1])
	}
	if want := "add-generic-password -U -s gemini -a antigravity -w '" + encoded + "'\n"; stdins[1] != want {
		t.Fatalf("write stdin = %q, want %q", stdins[1], want)
	}
	for _, arg := range calls[1] {
		if strings.Contains(arg, "secret") || strings.Contains(arg, encoded) {
			t.Fatal("the credential appeared in the argument list")
		}
	}
}

func TestSecurityAgyKeychainReportsAMissingItem(t *testing.T) {
	keychain := securityAgyKeychain{run: func(context.Context, []byte, ...string) ([]byte, int, error) {
		return nil, agySecurityItemNotFound, errors.New("security find-generic-password: The specified item could not be found in the keychain.")
	}}
	got, present, err := keychain.read(context.Background())
	if err != nil || present || got != nil {
		t.Fatalf("read = %q, %v, %v; want no item and no error", got, present, err)
	}
}

// The switch that wrote only the file left agy signing in as the previous
// account. Activation must put the target into the keychain as well.
func TestAgyActivationMirrorsTheTargetIntoTheKeychain(t *testing.T) {
	fixture := agyNewSwitchFixture(t)
	source := agyTestOAuthCredential("source-account", "source-access")
	keychain := &fakeAgyKeychain{value: source, present: true}
	fixture.manager.keychain = keychain
	targetPath := filepath.Join(fixture.target.Home, agyCredentialFilename)
	target, err := readOpaqueCredential(targetPath)
	if err != nil {
		t.Fatal(err)
	}

	if err := fixture.manager.activateFromCredentialLocked(context.Background(), fixture.target.Snapshot.ID, targetPath, source); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(keychain.value, target) {
		t.Fatal("the keychain still holds the previous account after the switch")
	}
	if fixture.manager.activeAccountID() != fixture.target.Snapshot.ID {
		t.Fatalf("active account = %q", fixture.manager.activeAccountID())
	}
}

// No keychain item means agy reads the file, so the switch must not create one.
func TestAgyActivationLeavesTheKeychainAloneWhenAgyReadsTheFile(t *testing.T) {
	fixture := agyNewSwitchFixture(t)
	keychain := &fakeAgyKeychain{}
	fixture.manager.keychain = keychain
	targetPath := filepath.Join(fixture.target.Home, agyCredentialFilename)
	if err := fixture.manager.activateFromCredentialLocked(context.Background(), fixture.target.Snapshot.ID, targetPath, agyTestOAuthCredential("source-account", "source-access")); err != nil {
		t.Fatal(err)
	}
	if keychain.writes != 0 || keychain.present {
		t.Fatalf("activation wrote a keychain item agy was not using: writes=%d", keychain.writes)
	}
}

// A switch agy would not honour is reported as not committed, and the device
// file goes back to the source so AO and agy still agree.
func TestAgyActivationRestoresTheFileWhenTheKeychainDoesNotTakeTheSwitch(t *testing.T) {
	fixture := agyNewSwitchFixture(t)
	source := agyTestOAuthCredential("source-account", "source-access")
	fixture.manager.keychain = &fakeAgyKeychain{value: source, present: true, dropWrite: true}
	targetPath := filepath.Join(fixture.target.Home, agyCredentialFilename)

	err := fixture.manager.activateFromCredentialLocked(context.Background(), fixture.target.Snapshot.ID, targetPath, source)
	if !errors.Is(err, ports.ErrAgyAccountSwitchNotCommitted) {
		t.Fatalf("err = %v, want not committed", err)
	}
	global, readErr := readOpaqueCredential(fixture.manager.globalCredentialPath())
	if readErr != nil || !bytes.Equal(global, source) {
		t.Fatal("the device credential file was not restored to the source")
	}
	if fixture.manager.activeAccountID() == fixture.target.Snapshot.ID {
		t.Fatal("a switch agy would not honour was reported active")
	}
}

// The device file follows the keychain, which agy refreshes and signs in from;
// an absent or unreadable item leaves the file alone.
func TestAgySyncGlobalCredentialFromKeychain(t *testing.T) {
	onDisk := agyTestOAuthCredential("source-account", "stale-access")
	live := agyTestOAuthCredential("source-account", "fresh-access")
	for _, tt := range []struct {
		name     string
		keychain *fakeAgyKeychain
		want     []byte
	}{
		{"keychain wins", &fakeAgyKeychain{value: live, present: true}, live},
		{"no item", &fakeAgyKeychain{}, onDisk},
		{"not a credential", &fakeAgyKeychain{value: []byte("not json"), present: true}, onDisk},
	} {
		t.Run(tt.name, func(t *testing.T) {
			manager := newTestAgyAccountManager(t, nil, nil)
			if err := os.MkdirAll(filepath.Dir(manager.globalCredentialPath()), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := agyWriteGlobalCredentialAtomic(manager.globalCredentialPath(), onDisk); err != nil {
				t.Fatal(err)
			}
			manager.keychain = tt.keychain
			manager.syncGlobalCredentialFromKeychain(context.Background())
			got, err := readOpaqueCredential(manager.globalCredentialPath())
			if err != nil || !bytes.Equal(got, tt.want) {
				t.Fatalf("device file = %q, %v; want %q", got, err, tt.want)
			}
		})
	}
}
