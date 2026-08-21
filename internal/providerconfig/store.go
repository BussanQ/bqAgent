package providerconfig

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const currentVersion = 1

type Secret struct {
	Salt       string `json:"salt"`
	Nonce      string `json:"nonce"`
	Ciphertext string `json:"ciphertext"`
}

type Provider struct {
	ID           string   `json:"id"`
	Name         string   `json:"name"`
	APIType      string   `json:"api_type"`
	BaseURL      string   `json:"base_url,omitempty"`
	Models       []string `json:"models"`
	DefaultModel string   `json:"default_model"`
	APIKey       Secret   `json:"api_key"`
}

type Config struct {
	Version        int        `json:"version"`
	ActiveProvider string     `json:"active_provider,omitempty"`
	Providers      []Provider `json:"providers"`
}

type Store struct {
	path    string
	keyPath string
}

func NewStore(agentDir string) *Store {
	return &Store{path: filepath.Join(agentDir, "config.json"), keyPath: filepath.Join(agentDir, ".config.key")}
}

func (store *Store) Load() (Config, error) {
	data, err := os.ReadFile(store.path)
	if errors.Is(err, os.ErrNotExist) {
		return Config{Version: currentVersion, Providers: []Provider{}}, nil
	}
	if err != nil {
		return Config{}, err
	}
	var config Config
	if err := json.Unmarshal(data, &config); err != nil {
		return Config{}, fmt.Errorf("parse provider config: %w", err)
	}
	if config.Version == 0 {
		config.Version = currentVersion
	}
	if config.Providers == nil {
		config.Providers = []Provider{}
	}
	return config, nil
}

func (store *Store) Save(config Config) error {
	config.Version = currentVersion
	if err := validate(config); err != nil {
		return err
	}
	data, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(store.path), 0o700); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(store.path), "config-*.json")
	if err != nil {
		return err
	}
	temporaryName := temporary.Name()
	defer os.Remove(temporaryName)
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return err
	}
	if _, err := temporary.Write(data); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporaryName, store.path)
}

func (store *Store) EncryptAPIKey(value string) (Secret, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return Secret{}, nil
	}
	master, err := store.masterKey(true)
	if err != nil {
		return Secret{}, err
	}
	salt := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, salt); err != nil {
		return Secret{}, err
	}
	key := deriveKey(master, salt)
	block, err := aes.NewCipher(key)
	if err != nil {
		return Secret{}, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return Secret{}, err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return Secret{}, err
	}
	ciphertext := aead.Seal(nil, nonce, []byte(value), nil)
	return Secret{Salt: encode(salt), Nonce: encode(nonce), Ciphertext: encode(ciphertext)}, nil
}

func (store *Store) DecryptAPIKey(secret Secret) (string, error) {
	if secret.Ciphertext == "" {
		return "", nil
	}
	master, err := store.masterKey(false)
	if err != nil {
		return "", err
	}
	salt, err := decode(secret.Salt)
	if err != nil {
		return "", fmt.Errorf("decode api key salt: %w", err)
	}
	nonce, err := decode(secret.Nonce)
	if err != nil {
		return "", fmt.Errorf("decode api key nonce: %w", err)
	}
	ciphertext, err := decode(secret.Ciphertext)
	if err != nil {
		return "", fmt.Errorf("decode api key ciphertext: %w", err)
	}
	block, err := aes.NewCipher(deriveKey(master, salt))
	if err != nil {
		return "", err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	plaintext, err := aead.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return "", fmt.Errorf("decrypt api key: %w", err)
	}
	return string(plaintext), nil
}

func (store *Store) masterKey(create bool) ([]byte, error) {
	key, err := os.ReadFile(store.keyPath)
	if err == nil {
		if len(key) != 32 {
			return nil, fmt.Errorf("invalid provider config master key")
		}
		return key, nil
	}
	if !errors.Is(err, os.ErrNotExist) || !create {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(store.keyPath), 0o700); err != nil {
		return nil, err
	}
	key = make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, key); err != nil {
		return nil, err
	}
	if err := os.WriteFile(store.keyPath, key, 0o600); err != nil {
		return nil, err
	}
	return key, nil
}

func validate(config Config) error {
	seen := map[string]bool{}
	for _, provider := range config.Providers {
		id := strings.TrimSpace(provider.ID)
		if id == "" || strings.ContainsAny(id, " /\\") {
			return fmt.Errorf("invalid provider id %q", provider.ID)
		}
		if seen[id] {
			return fmt.Errorf("duplicate provider id %q", id)
		}
		seen[id] = true
		if strings.TrimSpace(provider.Name) == "" || strings.TrimSpace(provider.DefaultModel) == "" || len(provider.Models) == 0 {
			return fmt.Errorf("provider %q requires name, models, and default_model", id)
		}
	}
	if config.ActiveProvider != "" && !seen[config.ActiveProvider] {
		return fmt.Errorf("active provider %q does not exist", config.ActiveProvider)
	}
	return nil
}

func deriveKey(master, salt []byte) []byte {
	hash := sha256.New()
	hash.Write(master)
	hash.Write(salt)
	return hash.Sum(nil)
}

func encode(value []byte) string          { return base64.RawStdEncoding.EncodeToString(value) }
func decode(value string) ([]byte, error) { return base64.RawStdEncoding.DecodeString(value) }
