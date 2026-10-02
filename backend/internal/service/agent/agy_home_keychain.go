package agent

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// agy 1.2.9 saves its token, user tier and project through /usr/bin/security
// into the default keychain. AO runs agy for a saved account with HOME at that
// account's private home, where macOS finds no default keychain: every save
// opened "A keychain cannot be found" (Reset To Defaults / Cancel) and agy
// waited 5 s before falling back to its credential file. Each managed home
// therefore gets a keychain of its own, which macOS finds as the default
// through a login.keychain-db link. The real file has another name because a
// keychain named login is unlocked with the user's login password, not its own.
// Every security call runs with HOME at the managed home, so the keychain never
// joins the user's own keychain list.
const (
	agyHomeKeychainName = "agy.keychain-db"
	agyHomeKeychainLink = "login.keychain-db"
	// agyHomeKeychainPassword only lets AO unlock the keychain without a
	// prompt after a restart. It guards nothing the credential file next to it
	// does not already hold, so it is not a secret.
	agyHomeKeychainPassword = "ao-agy-account" //nolint:gosec // not a secret; see above.
)

// agyHomeKeychains keeps a managed home's keychain and its credential file in
// step around each agy run. The file stays AO's source of truth: before a run
// it is copied into the keychain agy reads first, and a token agy refreshed
// during the run is copied back to the file afterwards.
type agyHomeKeychains interface {
	prepare(ctx context.Context, home string) error
	syncCredential(ctx context.Context, home string) error
}

// noAgyHomeKeychains is used off macOS and for any manager that does not run
// against the user's own home, tests included.
type noAgyHomeKeychains struct{}

func (noAgyHomeKeychains) prepare(context.Context, string) error        { return nil }
func (noAgyHomeKeychains) syncCredential(context.Context, string) error { return nil }

type securityAgyHomeKeychains struct {
	mu  sync.Mutex
	run func(ctx context.Context, env []string, stdin []byte, args ...string) ([]byte, int, error)
}

func defaultAgyHomeKeychains(globalHome string) agyHomeKeychains {
	if _, ok := defaultAgyKeychain(globalHome).(noAgyKeychain); ok {
		return noAgyHomeKeychains{}
	}
	return &securityAgyHomeKeychains{run: runSecurityWithEnv}
}

// agyHomeEnv is the daemon's environment with HOME at a managed home, which is
// how security finds that home's keychain instead of the user's.
func agyHomeEnv(home string) []string {
	env := make([]string, 0, len(os.Environ())+1)
	for _, entry := range os.Environ() {
		if !strings.HasPrefix(entry, "HOME=") {
			env = append(env, entry)
		}
	}
	return append(env, "HOME="+home)
}

func (k *securityAgyHomeKeychains) keychain(home string) agyKeychain {
	env := agyHomeEnv(home)
	return securityAgyKeychain{run: func(ctx context.Context, stdin []byte, args ...string) ([]byte, int, error) {
		return k.run(ctx, env, stdin, args...)
	}}
}

func (k *securityAgyHomeKeychains) prepare(ctx context.Context, home string) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	env := agyHomeEnv(home)
	dir := filepath.Join(home, "Library", "Keychains")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	path := filepath.Join(dir, agyHomeKeychainName)
	if _, err := os.Stat(path); errors.Is(err, fs.ErrNotExist) {
		if _, _, err := k.run(ctx, env, nil, "create-keychain", "-p", agyHomeKeychainPassword, path); err != nil {
			return fmt.Errorf("create the account keychain: %w", err)
		}
		// No settings: the keychain never locks on a timer or on sleep.
		if _, _, err := k.run(ctx, env, nil, "set-keychain-settings", path); err != nil {
			return fmt.Errorf("configure the account keychain: %w", err)
		}
	} else if err != nil {
		return err
	}
	link := filepath.Join(dir, agyHomeKeychainLink)
	if _, err := os.Lstat(link); errors.Is(err, fs.ErrNotExist) {
		if err := os.Symlink(agyHomeKeychainName, link); err != nil {
			return err
		}
	} else if err != nil {
		return err
	}
	// A keychain is locked again after every restart; unlocking it here keeps
	// agy's reads and saves from asking for its password.
	if _, _, err := k.run(ctx, env, nil, "unlock-keychain", "-p", agyHomeKeychainPassword, path); err != nil {
		return fmt.Errorf("unlock the account keychain: %w", err)
	}
	credential, err := readOpaqueCredential(filepath.Join(home, agyCredentialFilename))
	if err != nil {
		return nil // no file yet: agy has nothing to read either
	}
	return mirrorAgyCredentialToKeychain(ctx, k.keychain(home), credential)
}

func (k *securityAgyHomeKeychains) syncCredential(ctx context.Context, home string) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	saved, present, err := k.keychain(home).read(ctx)
	if err != nil || !present || len(saved) == 0 {
		return err
	}
	savedIdentity, err := parseAgyCredentialIdentity(saved)
	if err != nil {
		return fmt.Errorf("the account keychain does not hold an Antigravity credential: %w", err)
	}
	path := filepath.Join(home, agyCredentialFilename)
	current, err := readOpaqueCredential(path)
	if err != nil {
		return err
	}
	if bytes.Equal(current, saved) {
		return nil
	}
	// Never let a keychain write one account's token over another's file.
	if currentIdentity, err := parseAgyCredentialIdentity(current); err == nil &&
		currentIdentity.ProviderAccountID != "" && currentIdentity.ProviderAccountID != savedIdentity.ProviderAccountID {
		return errors.New("the account keychain holds a different account; the credential file was left alone")
	}
	return agyWriteGlobalCredentialAtomic(path, saved)
}

// prepareHomeKeychain readies a managed home's keychain before agy runs there
// and returns the step that copies a refreshed token back afterwards. A
// failure is logged, not fatal: agy still falls back to the credential file.
func (m *agyAccountManager) prepareHomeKeychain(ctx context.Context, home string) func() {
	if err := m.homeKeychains.prepare(ctx, home); err != nil {
		m.logger.Warn("agy accounts: account keychain not ready; agy may prompt for a keychain", "error", err)
	}
	return func() {
		if err := m.homeKeychains.syncCredential(context.WithoutCancel(ctx), home); err != nil {
			m.logger.Warn("agy accounts: could not copy the refreshed token to the account's credential file", "error", err)
		}
	}
}
