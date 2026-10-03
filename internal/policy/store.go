package policy

import (
	"regexp"
	"strings"
	"sync"
)

type Policy struct {
	Name         string
	References   []string
	SecretName   string
	SecretKey    string
	PublicKeyPEM []byte
}

type Store struct {
	mu       sync.RWMutex
	policies map[string]Policy
	secrets  map[string]map[string][]byte
}

func NewStore() *Store {
	return &Store{policies: make(map[string]Policy), secrets: make(map[string]map[string][]byte)}
}

func (s *Store) ReplacePolicies(policies []Policy) {
	s.mu.Lock()
	defer s.mu.Unlock()
	next := make(map[string]Policy, len(policies))
	for _, p := range policies {
		next[p.Name] = p
	}
	s.policies = next
}

func (s *Store) ReplaceSecrets(secrets map[string]map[string][]byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.secrets = secrets
}

func (s *Store) Matching(image string) []Policy {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var result []Policy
	for _, p := range s.policies {
		for _, pattern := range p.References {
			if wildcardMatch(pattern, image) {
				copyPolicy := p
				if secret, ok := s.secrets[p.SecretName]; ok {
					copyPolicy.PublicKeyPEM = append([]byte(nil), secret[p.SecretKey]...)
				}
				result = append(result, copyPolicy)
				break
			}
		}
	}
	return result
}

func NormalizeImage(ref string) string {
	ref = strings.TrimSpace(ref)
	if strings.Contains(ref, "@") {
		return ref
	}
	lastSlash := strings.LastIndexByte(ref, '/')
	lastColon := strings.LastIndexByte(ref, ':')
	if lastColon <= lastSlash {
		ref += ":latest"
	}
	if !strings.Contains(ref[:strings.IndexByte(ref, '/')+1], ".") && !strings.Contains(ref[:strings.IndexByte(ref, '/')+1], ":") {
		ref = "docker.io/" + ref
	}
	return ref
}

func wildcardMatch(pattern, value string) bool {
	quoted := regexp.QuoteMeta(pattern)
	re := "^" + regexp.MustCompile(`\\\*`).ReplaceAllString(quoted, ".*") + "$"
	return regexp.MustCompile(re).MatchString(value)
}
