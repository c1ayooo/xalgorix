// Package llm — multi-provider API key store (LiteLLM-style).
//
// KeyStore holds API keys for multiple LLM providers simultaneously,
// allowing the Router to pick the right key based on which provider
// a model resolves to. Unlike the existing single-provider profile
// model, the KeyStore lets operators configure OpenAI + Anthropic +
// Google keys at once.
//
// Thread-safe for concurrent reads and writes.
package llm

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
)

// ProviderKey holds the API key and configuration for one provider.
type ProviderKey struct {
	ProviderID  string `json:"provider_id"`
	APIKey      string `json:"api_key"`
	HeaderStyle string `json:"header_style"` // "openai", "anthropic", "gemini"
	BaseURL     string `json:"base_url"`     // override; empty = use catalog default
}

// KeyStore manages API keys for multiple LLM providers.
type KeyStore struct {
	mu       sync.RWMutex
	keys     map[string]ProviderKey // providerID → key
	filePath string                 // persistence path
}

// NewKeyStore creates a new KeyStore that persists to the given path.
// If the file exists, it loads keys from it. If not, starts empty.
func NewKeyStore(dataDir string) (*KeyStore, error) {
	fp := filepath.Join(dataDir, "llm_keys.json")
	if st, err := os.Stat(fp); err == nil && st.IsDir() {
		// v4.4.22 prerelease server wiring accidentally passed a
		// file path into NewKeyStore, creating
		// <dataDir>/llm_keys.json/llm_keys.json. Keep reading and
		// writing that nested file when present so existing saved
		// keys remain usable after the constructor call site is
		// corrected.
		fp = filepath.Join(fp, "llm_keys.json")
	}
	ks := &KeyStore{
		keys:     make(map[string]ProviderKey),
		filePath: fp,
	}
	if err := ks.load(); err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	return ks, nil
}

// Get returns the ProviderKey for the given provider ID.
// Returns (key, true) if found, (zero, false) if not.
func (ks *KeyStore) Get(providerID string) (ProviderKey, bool) {
	ks.mu.RLock()
	defer ks.mu.RUnlock()
	k, ok := ks.keys[providerID]
	return k, ok
}

// Set stores a ProviderKey and persists to disk.
func (ks *KeyStore) Set(ctx context.Context, key ProviderKey) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	ks.mu.Lock()
	defer ks.mu.Unlock()
	next := cloneProviderKeys(ks.keys)
	next[key.ProviderID] = key
	if err := ks.saveKeys(next); err != nil {
		return err
	}
	ks.keys = next
	return nil
}

// SetMultiple stores multiple ProviderKeys atomically and persists.
func (ks *KeyStore) SetMultiple(ctx context.Context, keys []ProviderKey) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	ks.mu.Lock()
	defer ks.mu.Unlock()
	next := cloneProviderKeys(ks.keys)
	for _, k := range keys {
		next[k.ProviderID] = k
	}
	if err := ks.saveKeys(next); err != nil {
		return err
	}
	ks.keys = next
	return nil
}

// Delete removes a provider's key and persists.
func (ks *KeyStore) Delete(ctx context.Context, providerID string) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	ks.mu.Lock()
	defer ks.mu.Unlock()
	next := cloneProviderKeys(ks.keys)
	delete(next, providerID)
	if err := ks.saveKeys(next); err != nil {
		return err
	}
	ks.keys = next
	return nil
}

// List returns all configured provider keys. API keys are masked
// for display (showing only the last 4 characters).
func (ks *KeyStore) List() []ProviderKey {
	ks.mu.RLock()
	defer ks.mu.RUnlock()
	result := make([]ProviderKey, 0, len(ks.keys))
	for _, k := range ks.keys {
		masked := k
		masked.APIKey = maskKey(k.APIKey)
		result = append(result, masked)
	}
	return result
}

// ListUnmasked returns all configured provider keys with full
// API keys. Used internally by the Router — never exposed via API.
func (ks *KeyStore) ListUnmasked() []ProviderKey {
	ks.mu.RLock()
	defer ks.mu.RUnlock()
	result := make([]ProviderKey, 0, len(ks.keys))
	for _, k := range ks.keys {
		result = append(result, k)
	}
	return result
}

// HasKey returns true if a non-empty API key is configured for
// the given provider.
func (ks *KeyStore) HasKey(providerID string) bool {
	ks.mu.RLock()
	defer ks.mu.RUnlock()
	k, ok := ks.keys[providerID]
	return ok && k.APIKey != ""
}

// ConfiguredProviders returns the list of provider IDs that have
// API keys configured.
func (ks *KeyStore) ConfiguredProviders() []string {
	ks.mu.RLock()
	defer ks.mu.RUnlock()
	result := make([]string, 0, len(ks.keys))
	for pid, k := range ks.keys {
		if k.APIKey != "" {
			result = append(result, pid)
		}
	}
	return result
}

// load reads keys from the JSON file. Returns os.ErrNotExist
// if the file doesn't exist (not an error).
func (ks *KeyStore) load() error {
	data, err := os.ReadFile(ks.filePath)
	if err != nil {
		return err
	}
	var keys []ProviderKey
	if err := json.Unmarshal(data, &keys); err != nil {
		return err
	}
	for _, k := range keys {
		ks.keys[k.ProviderID] = k
	}
	return nil
}

func cloneProviderKeys(src map[string]ProviderKey) map[string]ProviderKey {
	out := make(map[string]ProviderKey, len(src)+1)
	for id, key := range src {
		out[id] = key
	}
	return out
}

// saveKeys writes a candidate map atomically. The caller commits it to ks.keys
// only after this returns nil, so a failed write never leaves memory and disk
// disagreeing.
func (ks *KeyStore) saveKeys(keys map[string]ProviderKey) error {
	list := make([]ProviderKey, 0, len(keys))
	for _, k := range keys {
		list = append(list, k)
	}
	data, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Dir(ks.filePath)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".llm_keys-*.tmp")
	if err != nil {
		return err
	}
	tmp := f.Name()
	if _, err := f.Write(data); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Chmod(0600); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, ks.filePath); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

// maskKey returns a masked version of an API key, showing only
// the last 4 characters: "sk-abc123xyz" → "****xyz"
func maskKey(key string) string {
	if len(key) <= 4 {
		return "****"
	}
	return "****" + key[len(key)-4:]
}
