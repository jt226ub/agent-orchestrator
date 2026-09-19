package agent

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

// agyCredentialIdentity contains only locally parsed matching material: the
// Google subject and email carried by the credential's ID token claims. The
// tokens themselves are never retained, logged, or exposed through the API.
// The claims are read without verifying the token signature; they are display
// and matching metadata, as Codex's tokens.account_id is, never an authority.
type agyCredentialIdentity struct {
	Method            domain.AgyAuthMethod
	ProviderAccountID string
	Email             *string
}

func parseAgyCredentialIdentity(data []byte) (agyCredentialIdentity, error) {
	var document struct {
		Token *struct {
			AccessToken  string `json:"access_token"`
			RefreshToken string `json:"refresh_token"`
		} `json:"token"`
		AuthMethod string `json:"auth_method"`
		IDToken    string `json:"id_token"`
	}
	if err := json.Unmarshal(data, &document); err != nil {
		return agyCredentialIdentity{}, errors.New("agy credential is not valid JSON")
	}
	if document.Token == nil || (strings.TrimSpace(document.Token.AccessToken) == "" && strings.TrimSpace(document.Token.RefreshToken) == "") {
		return agyCredentialIdentity{}, errors.New("agy credential does not contain a supported login")
	}
	// Every Antigravity sign-in is a Google account; auth_method only says
	// which kind ("consumer" for a personal account, otherwise a Workspace one).
	identity := agyCredentialIdentity{Method: domain.AgyAuthMethodGoogle}
	subject, email := agyIDTokenClaims(document.IDToken)
	if subject != "" && !safeProviderAccountID(subject) {
		return agyCredentialIdentity{}, errors.New("agy credential contains an invalid account identity")
	}
	identity.ProviderAccountID = subject
	if email != "" && safeAccountEmail(email) {
		identity.Email = &email
	}
	return identity, nil
}

// agyIDTokenClaims returns the sub and email claims of a JWT payload, or empty
// strings when the token is absent or malformed. Only the payload segment is
// decoded; nothing about it is trusted beyond display and matching.
func agyIDTokenClaims(token string) (string, string) {
	parts := strings.Split(strings.TrimSpace(token), ".")
	if len(parts) != 3 {
		return "", ""
	}
	payload, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(parts[1], "="))
	if err != nil {
		return "", ""
	}
	var claims struct {
		Subject string `json:"sub"`
		Email   string `json:"email"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return "", ""
	}
	return strings.TrimSpace(claims.Subject), strings.TrimSpace(claims.Email)
}

func inspectAgyCredentialIdentity(data []byte) (agyCredentialIdentity, bool) {
	identity, err := parseAgyCredentialIdentity(data)
	return identity, err == nil
}

// agyLocalCredentialIdentifiesRecord validates the non-secret identity that
// can be derived from one AO-owned credential. A credential without an ID
// token subject stays distinguishable by its private slot and exact bytes.
func agyLocalCredentialIdentifiesRecord(record agyAccountRecord, data []byte) bool {
	identity, err := parseAgyCredentialIdentity(data)
	if err != nil {
		return false
	}
	if record.ProviderAccountID != "" {
		return identity.ProviderAccountID == record.ProviderAccountID
	}
	if record.Snapshot.AuthMethod != domain.AgyAuthMethodUnknown && record.Snapshot.AuthMethod != identity.Method {
		return false
	}
	saved, savedErr := readOpaqueCredential(filepath.Join(record.Home, agyCredentialFilename))
	return savedErr == nil && bytes.Equal(saved, data)
}
