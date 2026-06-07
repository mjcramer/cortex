package slackadmin

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// TokenState is the persisted Slack app-configuration token pair. Slack's
// configuration access tokens expire every ~12h, and the refresh token is
// single-use: each rotation returns a new refresh token that must be persisted
// or the chain is broken and you have to re-seed from api.slack.com.
type TokenState struct {
	AppID        string    `json:"app_id"`
	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token"`
	ExpiresAt    time.Time `json:"expires_at"`
}

// LoadTokenState reads token state from disk. A non-existent file is reported
// via os.IsNotExist so callers can bootstrap from a seed refresh token.
func LoadTokenState(path string) (*TokenState, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var s TokenState
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, fmt.Errorf("parse token state %s: %w", path, err)
	}
	return &s, nil
}

// Save writes the token state atomically (temp file + rename) with 0600 perms,
// creating the parent directory if needed.
func (s *TokenState) Save(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create token dir: %w", err)
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return fmt.Errorf("write token state: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("commit token state: %w", err)
	}
	return nil
}

// NeedsRotation reports whether the access token is missing or within a minute
// of expiry. A zero ExpiresAt (freshly bootstrapped state) always needs one.
func (s *TokenState) NeedsRotation() bool {
	return s.AccessToken == "" || time.Until(s.ExpiresAt) < time.Minute
}
