package policy

import "testing"

func TestWildcardMatchesSlashes(t *testing.T) {
	s := NewStore()
	s.ReplacePolicies([]Policy{{Name: "p", References: []string{"ghcr.io/acme/*"}, SecretName: "key", SecretKey: "cosign.pub"}})
	s.ReplaceSecrets(map[string]map[string][]byte{"key": {"cosign.pub": []byte("pem")}})
	if got := s.Matching("ghcr.io/acme/team/app:1"); len(got) != 1 || string(got[0].PublicKeyPEM) != "pem" {
		t.Fatalf("expected matching policy with key, got %#v", got)
	}
	if got := s.Matching("docker.io/library/alpine:3"); len(got) != 0 {
		t.Fatalf("expected no matching policy, got %#v", got)
	}
}

func TestNormalizeImageAddsDefaultRegistryAndTag(t *testing.T) {
	for input, want := range map[string]string{
		"alpine":                      "docker.io/alpine:latest",
		"alpine:3":                    "docker.io/alpine:3",
		"ghcr.io/acme/app":            "ghcr.io/acme/app:latest",
		"ghcr.io/acme/app@sha256:abc": "ghcr.io/acme/app@sha256:abc",
	} {
		if got := NormalizeImage(input); got != want {
			t.Errorf("NormalizeImage(%q) = %q, want %q", input, got, want)
		}
	}
}
