package agent

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

const apiKeyFile = "/etc/ethagent/api-key"
const apiKeyLength = 32 // 256 bits

// loadOrCreateAPIKey returns the API key, generating it on first run.
// The key is stored at /etc/ethagent/api-key with permissions 0600.
// ethctl reads this file (or uses ETHAGENT_API_KEY env var) to authenticate.
func loadOrCreateAPIKey() (string, error) {
	// Check env override first (useful for CI and testing)
	if key := os.Getenv("ETHAGENT_API_KEY"); key != "" {
		return key, nil
	}

	data, err := os.ReadFile(apiKeyFile)
	if err == nil {
		key := strings.TrimSpace(string(data))
		if len(key) >= 32 {
			return key, nil
		}
	}

	if !os.IsNotExist(err) && err != nil {
		return "", fmt.Errorf("read api key: %w", err)
	}

	// Generate new key
	raw := make([]byte, apiKeyLength)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("generate api key: %w", err)
	}
	key := hex.EncodeToString(raw)

	if err := os.MkdirAll(filepath.Dir(apiKeyFile), 0750); err != nil {
		return "", fmt.Errorf("create config dir: %w", err)
	}
	// 0600: only root can read — ethctl must SSH or use sudo to retrieve it
	if err := os.WriteFile(apiKeyFile, []byte(key+"\n"), 0600); err != nil {
		return "", fmt.Errorf("write api key: %w", err)
	}

	return key, nil
}

// requireAPIKey is HTTP middleware that enforces Bearer token auth on
// mutating endpoints (POST). Read-only endpoints (GET) are unrestricted
// so Prometheus scraping and monitoring still work without credentials.
func requireAPIKey(apiKey string, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// GET/HEAD requests are unauthenticated — monitoring-safe
		if r.Method == http.MethodGet || r.Method == http.MethodHead {
			next(w, r)
			return
		}

		auth := r.Header.Get("Authorization")
		if auth == "" {
			http.Error(w, "Authorization header required for write operations", http.StatusUnauthorized)
			return
		}

		const prefix = "Bearer "
		if !strings.HasPrefix(auth, prefix) {
			http.Error(w, "Authorization must use Bearer scheme", http.StatusUnauthorized)
			return
		}

		token := auth[len(prefix):]
		// Constant-time comparison to prevent timing attacks
		if subtle.ConstantTimeCompare([]byte(token), []byte(apiKey)) != 1 {
			http.Error(w, "Invalid API key", http.StatusForbidden)
			return
		}

		next(w, r)
	}
}
