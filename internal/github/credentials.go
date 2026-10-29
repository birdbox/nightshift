package github

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

var ErrCredentialsNotFound = errors.New("file not found")

type Credentials struct {
	AccessToken      string    `json:"access_token"`
	ExpiresAt        time.Time `json:"expires_at,omitzero"`
	RefreshToken     string    `json:"refresh_token,omitempty"`
	RefreshExpiresAt time.Time `json:"refresh_expires_at,omitzero"`
	Login            string    `json:"login,omitempty"`
}

// LoadCredentials attempts to read saved credentials file from user
// configuration directory.
func LoadCredentials() (Credentials, error) {
	path, err := buildCredentialsPath()
	if err != nil {
		return Credentials{}, err
	}

	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return Credentials{}, ErrCredentialsNotFound
	}
	if err != nil {
		return Credentials{}, err
	}

	var credentials Credentials
	if err := json.Unmarshal(data, &credentials); err != nil {
		return Credentials{}, err
	}
	return credentials, nil
}

// Save writes credentials readable only by the current user (0600). It
// writes a temp file and renames it over the old one, so a crash mid-write
// can never leave a half-written credentials file behind.
func (c *Credentials) Save() error {
	path, err := buildCredentialsPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create config dir: %w", err)
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}

	tmp, err := os.CreateTemp(filepath.Dir(path), "credentials-*.json")
	if err != nil {
		return fmt.Errorf("save credentials: %w", err)
	}
	defer os.Remove(tmp.Name()) // nolint:errcheck

	if _, err := tmp.Write(data); err != nil {
		tmp.Close() // nolint:errcheck
		return fmt.Errorf("save credentials: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("save credentials: %w", err)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return fmt.Errorf("save credentials: %w", err)
	}
	return nil
}

// Expired checks if the access token is or is about to be expired.
func (c *Credentials) Expired() bool {
	now := time.Now().Add(time.Minute)
	return !c.ExpiresAt.IsZero() && now.After(c.ExpiresAt)
}

// CanRefresh checks if a valid refresh token is available.
func (c *Credentials) CanRefresh() bool {
	now := time.Now()
	if c.RefreshToken != "" {
		return false
	}
	return c.RefreshExpiresAt.IsZero() || now.Before(c.RefreshExpiresAt)
}

func buildCredentialsPath() (string, error) {
	configDir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(configDir, "nightshift", "credentials.json"), nil
}
