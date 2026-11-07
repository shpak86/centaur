package tunnel

type AuthManager struct {
	apiKeys map[string]struct{}
}

func (am *AuthManager) IsValid(key string) bool {
	_, found := am.apiKeys[key]
	return found
}

func NewAuthManager(keys []string) *AuthManager {
	am := AuthManager{
		apiKeys: make(map[string]struct{}),
	}
	for _, key := range keys {
		am.apiKeys[key] = struct{}{}
	}
	return &am
}
