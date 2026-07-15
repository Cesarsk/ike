package config

import (
	"errors"
	"fmt"

	"github.com/zalando/go-keyring"
)

// SecretStore holds per-context credentials outside the config file.
type SecretStore interface {
	Set(context, apiKey, appKey string) error
	Get(context string) (apiKey, appKey string, err error)
	SetToken(context, token string) error
	GetToken(context string) (string, error)
	Delete(context string) error
}

const keyringService = "ike"

// KeyringStore stores credentials in the OS keychain: macOS Keychain,
// Linux Secret Service (GNOME Keyring / KWallet), Windows Credential
// Manager. This is what backs contexts added via the TUI (:ctx → a).
type KeyringStore struct{}

func (KeyringStore) Set(context, apiKey, appKey string) error {
	if err := keyring.Set(keyringService, context+":api-key", apiKey); err != nil {
		return fmt.Errorf("keychain: %w", err)
	}
	if err := keyring.Set(keyringService, context+":app-key", appKey); err != nil {
		return fmt.Errorf("keychain: %w", err)
	}
	return nil
}

func (KeyringStore) Get(context string) (string, string, error) {
	apiKey, err := keyring.Get(keyringService, context+":api-key")
	if err != nil {
		return "", "", fmt.Errorf("keychain (%s:api-key): %w", context, err)
	}
	appKey, err := keyring.Get(keyringService, context+":app-key")
	if err != nil {
		return "", "", fmt.Errorf("keychain (%s:app-key): %w", context, err)
	}
	return apiKey, appKey, nil
}

func (KeyringStore) SetToken(context, token string) error {
	if err := keyring.Set(keyringService, context+":token", token); err != nil {
		return fmt.Errorf("keychain: %w", err)
	}
	return nil
}

func (KeyringStore) GetToken(context string) (string, error) {
	token, err := keyring.Get(keyringService, context+":token")
	if err != nil {
		return "", fmt.Errorf("keychain (%s:token): %w", context, err)
	}
	return token, nil
}

func (KeyringStore) Delete(context string) error {
	for _, k := range []string{context + ":api-key", context + ":app-key", context + ":token"} {
		if err := keyring.Delete(keyringService, k); err != nil && !errors.Is(err, keyring.ErrNotFound) {
			return fmt.Errorf("keychain: %w", err)
		}
	}
	return nil
}
