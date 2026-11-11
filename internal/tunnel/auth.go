package tunnel

// AuthManager validates connector API keys against a fixed allow-list.
type AuthManager struct {
	apiKeys map[string]struct{}
}

// IsValid reports whether key is a known API key.
func (am *AuthManager) IsValid(key string) bool {
	_, found := am.apiKeys[key]
	return found
}

// NewAuthManager creates an AuthManager that accepts the given API keys.
func NewAuthManager(keys []string) *AuthManager {
	am := AuthManager{
		apiKeys: make(map[string]struct{}),
	}
	for _, key := range keys {
		am.apiKeys[key] = struct{}{}
	}
	return &am
}
