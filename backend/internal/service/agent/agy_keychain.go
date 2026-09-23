package agent

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
)

// Antigravity CLI 1.2.9 signs in from the macOS keychain first and reads the
// device credential file only when that lookup fails. Its keyring backend
// shells out to /usr/bin/security and keeps one item, service "gemini" and
// account "antigravity", holding the same JSON as antigravity-oauth-token,
// stored as "go-keyring-base64:" + base64 (the zalando/go-keyring encoding).
// Refreshed tokens are written back to that item only, so the file goes stale.
// An account switch that rewrote only the file therefore changed nothing a
// worker used. The design note's "keychain unused" held for agy 1.2.7.
const (
	agyKeychainService      = "gemini"
	agyKeychainAccount      = "antigravity"
	agyKeychainBase64Prefix = "go-keyring-base64:"
	agyKeychainHexPrefix    = "go-keyring-encoded:"
	agySecurityPath         = "/usr/bin/security"
	// agySecurityItemNotFound is the exit status /usr/bin/security reports when
	// no item matches.
	agySecurityItemNotFound = 44
)

// agyKeychain is the keychain item agy signs in from. present is false when
// there is no item, which means agy is reading the device credential file.
type agyKeychain interface {
	read(ctx context.Context) (credential []byte, present bool, err error)
	write(ctx context.Context, credential []byte) error
}

// noAgyKeychain is used everywhere AO does not manage the user's own home: on
// other platforms, and for a scratch or test home. It reports no item, so the
// device credential file stays the only authority.
type noAgyKeychain struct{}

func (noAgyKeychain) read(context.Context) ([]byte, bool, error) { return nil, false, nil }
func (noAgyKeychain) write(context.Context, []byte) error        { return nil }

// securityAgyKeychain reaches the item through /usr/bin/security, as agy does,
// so the item's access list already trusts the caller and no prompt appears.
type securityAgyKeychain struct {
	run func(ctx context.Context, stdin []byte, args ...string) ([]byte, int, error)
}

// defaultAgyKeychain returns the real keychain only when globalHome is the
// user's own home on macOS. Any other home -- a switch staging area, a test's
// temporary directory -- gets the no-op, so nothing but the real device home
// can ever read or overwrite the user's sign-in.
func defaultAgyKeychain(globalHome string) agyKeychain {
	if runtime.GOOS != "darwin" || globalHome == "" {
		return noAgyKeychain{}
	}
	home, err := os.UserHomeDir()
	if err != nil || canonicalPath(home) != canonicalPath(globalHome) {
		return noAgyKeychain{}
	}
	if _, err := os.Stat(agySecurityPath); err != nil {
		return noAgyKeychain{}
	}
	return securityAgyKeychain{run: runSecurity}
}

func runSecurity(ctx context.Context, stdin []byte, args ...string) ([]byte, int, error) {
	cmd := exec.CommandContext(ctx, agySecurityPath, args...) //nolint:gosec // fixed system binary; arguments are constants
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return stdout.Bytes(), exitErr.ExitCode(), fmt.Errorf("security %s: %s", args[0], strings.TrimSpace(stderr.String()))
	}
	return stdout.Bytes(), 0, err
}

func (k securityAgyKeychain) read(ctx context.Context) ([]byte, bool, error) {
	out, code, err := k.run(ctx, nil, "find-generic-password", "-s", agyKeychainService, "-a", agyKeychainAccount, "-w")
	if code == agySecurityItemNotFound {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	credential, err := decodeAgyKeychainValue(strings.TrimRight(string(out), "\n"))
	if err != nil {
		return nil, false, err
	}
	return credential, true, nil
}

// write stores credential the way agy does: through `security -i` on stdin, so
// the secret never appears in a process listing, in agy's own encoding.
func (k securityAgyKeychain) write(ctx context.Context, credential []byte) error {
	value := agyKeychainBase64Prefix + base64.StdEncoding.EncodeToString(credential)
	command := fmt.Sprintf("add-generic-password -U -s %s -a %s -w '%s'\n", agyKeychainService, agyKeychainAccount, value)
	_, _, err := k.run(ctx, []byte(command), "-i")
	return err
}

// decodeAgyKeychainValue reverses the go-keyring encodings agy may have used.
// A value with neither prefix is taken as stored.
func decodeAgyKeychainValue(value string) ([]byte, error) {
	switch {
	case strings.HasPrefix(value, agyKeychainBase64Prefix):
		return base64.StdEncoding.DecodeString(strings.TrimPrefix(value, agyKeychainBase64Prefix))
	case strings.HasPrefix(value, agyKeychainHexPrefix):
		return hex.DecodeString(strings.TrimPrefix(value, agyKeychainHexPrefix))
	default:
		return []byte(value), nil
	}
}

// mirrorAgyCredentialToKeychain makes the keychain item hold credential, when
// agy keeps one, and reads it back: a switch is only real once agy would sign
// in as the target. No item means agy reads the file, so there is nothing to do.
func mirrorAgyCredentialToKeychain(ctx context.Context, keychain agyKeychain, credential []byte) error {
	_, present, err := keychain.read(ctx)
	if err != nil {
		return fmt.Errorf("read the Antigravity keychain item: %w", err)
	}
	if !present {
		return nil
	}
	if err := keychain.write(ctx, credential); err != nil {
		return fmt.Errorf("write the Antigravity keychain item: %w", err)
	}
	stored, present, err := keychain.read(ctx)
	if err != nil || !present || !bytes.Equal(stored, credential) {
		return errors.New("the Antigravity keychain item did not keep the switched credential")
	}
	return nil
}

// syncGlobalCredentialFromKeychain brings the device credential file up to date
// with the keychain item agy actually signs in from, before anything reads the
// file to decide which account is active or what a switch is replacing. Without
// it AO reported one account active while every worker ran on another, and a
// switch checkpointed a stale credential as the source.
func (m *agyAccountManager) syncGlobalCredentialFromKeychain(ctx context.Context) {
	credential, present, err := m.keychain.read(ctx)
	if err != nil {
		m.logger.Warn("agy accounts: keychain read failed; using the device credential file", "error", err)
		return
	}
	if !present || len(credential) == 0 {
		return
	}
	if _, err := parseAgyCredentialIdentity(credential); err != nil {
		m.logger.Warn("agy accounts: keychain item is not an Antigravity credential; left alone", "error", err)
		return
	}
	path := m.globalCredentialPath()
	if current, err := readOpaqueCredential(path); err == nil && bytes.Equal(current, credential) {
		return
	}
	if err := agyWriteGlobalCredentialAtomic(path, credential); err != nil {
		m.logger.Warn("agy accounts: could not sync the device credential file from the keychain", "error", err)
	}
}
