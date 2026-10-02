package agent

import (
	"bytes"
	"context"
	"encoding/base64"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"
)

// fakeHomeSecurity stands in for /usr/bin/security against one managed home.
type fakeHomeSecurity struct {
	calls   [][]string
	homes   []string
	item    []byte // the gemini/antigravity item, nil when absent
	created int
}

func (f *fakeHomeSecurity) run(_ context.Context, env []string, stdin []byte, args ...string) ([]byte, int, error) {
	f.calls = append(f.calls, args)
	for _, entry := range env {
		if strings.HasPrefix(entry, "HOME=") {
			f.homes = append(f.homes, strings.TrimPrefix(entry, "HOME="))
		}
	}
	switch args[0] {
	case "create-keychain":
		f.created++
		return nil, 0, os.WriteFile(args[len(args)-1], []byte("keychain"), 0o600)
	case "find-generic-password":
		if f.item == nil {
			return nil, agySecurityItemNotFound, nil
		}
		return []byte(agyKeychainBase64Prefix + base64.StdEncoding.EncodeToString(f.item) + "\n"), 0, nil
	case "-i":
		value := string(stdin)
		start := strings.Index(value, agyKeychainBase64Prefix) + len(agyKeychainBase64Prefix)
		decoded, err := base64.StdEncoding.DecodeString(value[start:strings.LastIndex(value, "'")])
		f.item = decoded
		return nil, 0, err
	}
	return nil, 0, nil
}

func agyTestManagedHome(t *testing.T, credential []byte) string {
	t.Helper()
	home := t.TempDir()
	path := filepath.Join(home, agyCredentialFilename)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, credential, 0o600); err != nil {
		t.Fatal(err)
	}
	return home
}

func TestAgyHomeKeychainPrepareCreatesOnceLinksUnlocksAndMirrorsTheFile(t *testing.T) {
	fileCredential := agyTestOAuthCredential("provider-a", "file-access")
	home := agyTestManagedHome(t, fileCredential)
	security := &fakeHomeSecurity{}
	keychains := &securityAgyHomeKeychains{run: security.run}

	for range 2 {
		if err := keychains.prepare(context.Background(), home); err != nil {
			t.Fatal(err)
		}
	}
	if security.created != 1 {
		t.Fatalf("create-keychain ran %d times, want once", security.created)
	}
	link, err := os.Readlink(filepath.Join(home, "Library", "Keychains", agyHomeKeychainLink))
	if err != nil || link != agyHomeKeychainName {
		t.Fatalf("default-keychain link = %q, %v", link, err)
	}
	unlocks := 0
	for _, call := range security.calls {
		if call[0] == "unlock-keychain" {
			unlocks++
		}
	}
	if unlocks != 2 {
		t.Fatalf("unlock-keychain ran %d times, want once per prepare", unlocks)
	}
	for _, got := range security.homes {
		if got != home {
			t.Fatalf("security ran with HOME=%q, want the managed home only", got)
		}
	}
	// No item yet: nothing is written, so agy reads the file.
	if security.item != nil {
		t.Fatal("prepare created a keychain item agy had not made")
	}

	// Once agy keeps an item, a newer file written by AO (a sign-in again)
	// replaces it, so agy cannot read a stale token first.
	security.item = agyTestOAuthCredential("provider-a", "stale-access")
	if err := keychains.prepare(context.Background(), home); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(security.item, fileCredential) {
		t.Fatal("the keychain still holds the stale token after prepare")
	}
}

func TestAgyHomeKeychainSyncCopiesARefreshedTokenBackToTheFile(t *testing.T) {
	onDisk := agyTestOAuthCredential("provider-a", "old-access")
	for _, tt := range []struct {
		name string
		item []byte
		want []byte
	}{
		{"refreshed by agy", agyTestOAuthCredential("provider-a", "new-access"), agyTestOAuthCredential("provider-a", "new-access")},
		{"no item", nil, onDisk},
		{"another account's token", agyTestOAuthCredential("provider-b", "b-access"), onDisk},
		{"not a credential", []byte("not json"), onDisk},
	} {
		t.Run(tt.name, func(t *testing.T) {
			home := agyTestManagedHome(t, onDisk)
			keychains := &securityAgyHomeKeychains{run: (&fakeHomeSecurity{item: tt.item}).run}
			_ = keychains.syncCredential(context.Background(), home)
			got, err := readOpaqueCredential(filepath.Join(home, agyCredentialFilename))
			if err != nil || !bytes.Equal(got, tt.want) {
				t.Fatalf("credential file = %q, %v; want %q", got, err, tt.want)
			}
		})
	}
}

// The prompt itself: with no keychain under a managed HOME, a save blocks on
// macOS's "A keychain cannot be found" dialog. After prepare, the same save
// completes at once and stays out of the user's own keychain list.
func TestAgyHomeKeychainLetsSecuritySaveWithoutAPrompt(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("macOS keychain only")
	}
	if _, err := os.Stat(agySecurityPath); err != nil {
		t.Skip("no /usr/bin/security")
	}
	userList := func() string {
		out, err := exec.Command(agySecurityPath, "list-keychains", "-d", "user").Output()
		if err != nil {
			t.Fatal(err)
		}
		return string(out)
	}
	before := userList()
	home := agyTestManagedHome(t, agyTestOAuthCredential("provider-a", "access"))
	keychains := &securityAgyHomeKeychains{run: runSecurityWithEnv}
	if err := keychains.prepare(context.Background(), home); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	save := exec.CommandContext(ctx, agySecurityPath, "add-generic-password", "-U", "-s", "ao-test", "-a", "ao-test", "-w", "value")
	save.Env = agyHomeEnv(home)
	if out, err := save.CombinedOutput(); err != nil {
		t.Fatalf("save under the managed home failed or waited on a prompt: %v %s", err, out)
	}
	find := exec.Command(agySecurityPath, "find-generic-password", "-s", "ao-test", "-a", "ao-test", "-w")
	find.Env = agyHomeEnv(home)
	if out, err := find.Output(); err != nil || strings.TrimSpace(string(out)) != "value" {
		t.Fatalf("read back = %q, %v", out, err)
	}
	if after := userList(); after != before {
		t.Fatalf("the user's keychain list changed:\nbefore %s\nafter  %s", before, after)
	}
	if slices.Contains(strings.Fields(userList()), home) {
		t.Fatal("the managed keychain joined the user's list")
	}
}
