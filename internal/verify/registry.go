package verify

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

type Registry interface {
	Resolve(ref string) (string, error)
	Bundle(ref, digest string) ([]byte, error)
}

const sigstoreBundleMediaType = "application/vnd.dev.sigstore.bundle.v0.3+json"

type HTTPRegistry struct {
	client *http.Client
	auth   map[string]string
}

func NewRegistry(client *http.Client, dockerConfigDir string) *HTTPRegistry {
	r := &HTTPRegistry{client: client}
	if dockerConfigDir != "" {
		data, err := os.ReadFile(filepath.Join(dockerConfigDir, "config.json"))
		if err == nil {
			var config struct {
				Auths map[string]struct {
					Auth string `json:"auth"`
				} `json:"auths"`
			}
			if json.Unmarshal(data, &config) == nil {
				r.auth = make(map[string]string)
				for host, entry := range config.Auths {
					host = strings.TrimPrefix(host, "https://")
					host = strings.TrimPrefix(host, "http://")
					r.auth[strings.TrimSuffix(host, "/")] = entry.Auth
				}
			}
		}
	}
	return r
}

func splitRef(ref string) (host, repository, tag string) {
	host = "docker.io"
	repository = ref
	if i := strings.IndexByte(ref, '/'); i >= 0 && (strings.Contains(ref[:i], ".") || strings.Contains(ref[:i], ":") || ref[:i] == "localhost") {
		host, repository = ref[:i], ref[i+1:]
	}
	tag = "latest"
	if i := strings.LastIndexByte(repository, ':'); i >= 0 {
		tag, repository = repository[i+1:], repository[:i]
	}
	if i := strings.LastIndexByte(repository, '@'); i >= 0 {
		tag, repository = repository[i+1:], repository[:i]
	}
	return
}

func (r *HTTPRegistry) request(method, host, path string) (*http.Response, error) {
	resp, err := r.doRequest(method, host, path, "")
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusUnauthorized {
		return resp, nil
	}

	challenge := resp.Header.Get("WWW-Authenticate")
	resp.Body.Close()
	params, ok := parseBearerChallenge(challenge)
	if !ok {
		return nil, fmt.Errorf("registry authentication required for %s", host)
	}
	token, err := r.registryToken(host, params)
	if err != nil {
		return nil, fmt.Errorf("obtain registry token: %w", err)
	}
	retry, err := r.doRequest(method, host, path, token)
	if err != nil {
		return nil, err
	}
	return retry, nil
}

func (r *HTTPRegistry) doRequest(method, host, path, bearerToken string) (*http.Response, error) {
	req, err := http.NewRequest(method, "https://"+host+path, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", strings.Join([]string{
		"application/vnd.oci.image.manifest.v1+json",
		"application/vnd.oci.image.index.v1+json",
		"application/vnd.docker.distribution.manifest.v2+json",
		"application/vnd.oci.artifact.manifest.v1+json",
	}, ", "))
	if bearerToken != "" {
		req.Header.Set("Authorization", "Bearer "+bearerToken)
	} else if auth := r.auth[host]; auth != "" {
		req.Header.Set("Authorization", "Basic "+auth)
	}
	resp, err := r.client.Do(req)
	if err != nil {
		return nil, err
	}
	return resp, nil
}

func parseBearerChallenge(challenge string) (map[string]string, bool) {
	if !strings.HasPrefix(strings.ToLower(challenge), "bearer ") {
		return nil, false
	}
	params := make(map[string]string)
	for _, part := range strings.Split(challenge[len("Bearer "):], ",") {
		key, value, ok := strings.Cut(strings.TrimSpace(part), "=")
		if !ok {
			continue
		}
		params[strings.ToLower(strings.TrimSpace(key))] = strings.Trim(strings.TrimSpace(value), `"`)
	}
	_, hasRealm := params["realm"]
	_, hasService := params["service"]
	_, hasScope := params["scope"]
	return params, hasRealm && hasService && hasScope
}

func (r *HTTPRegistry) registryToken(host string, challenge map[string]string) (string, error) {
	tokenURL, err := url.Parse(challenge["realm"])
	if err != nil {
		return "", err
	}
	query := tokenURL.Query()
	query.Set("service", challenge["service"])
	query.Set("scope", challenge["scope"])
	tokenURL.RawQuery = query.Encode()
	req, err := http.NewRequest(http.MethodGet, tokenURL.String(), nil)
	if err != nil {
		return "", err
	}
	if auth := r.auth[host]; auth != "" {
		req.Header.Set("Authorization", "Basic "+auth)
	}
	resp, err := r.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("token endpoint returned %s", resp.Status)
	}
	var result struct {
		Token       string `json:"token"`
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", err
	}
	if result.Token != "" {
		return result.Token, nil
	}
	if result.AccessToken != "" {
		return result.AccessToken, nil
	}
	return "", fmt.Errorf("token endpoint returned no token")
}

func (r *HTTPRegistry) Resolve(ref string) (string, error) {
	host, repository, tag := splitRef(ref)
	resp, err := r.request(http.MethodGet, host, "/v2/"+repository+"/manifests/"+tag)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("registry returned %s while resolving image", resp.Status)
	}
	digest := resp.Header.Get("Docker-Content-Digest")
	if digest != "" {
		return digest, nil
	}
	var manifest struct {
		Config struct {
			Digest string `json:"digest"`
		} `json:"config"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&manifest); err != nil {
		return "", err
	}
	if manifest.Config.Digest == "" {
		return "", fmt.Errorf("registry response has no digest")
	}
	return manifest.Config.Digest, nil
}

func (r *HTTPRegistry) Bundle(ref, digest string) ([]byte, error) {
	host, repository, _ := splitRef(ref)
	resp, err := r.request(http.MethodGet, host, "/v2/"+repository+"/referrers/"+digest)
	if err == nil && resp.StatusCode == http.StatusOK {
		defer resp.Body.Close()
		var index struct {
			Manifests []struct {
				Digest       string `json:"digest"`
				ArtifactType string `json:"artifactType"`
				MediaType    string `json:"mediaType"`
			} `json:"manifests"`
		}
		if json.NewDecoder(resp.Body).Decode(&index) == nil {
			for _, m := range index.Manifests {
				if isSignatureDescriptor(m.ArtifactType, m.MediaType) {
					return r.bundleFromManifest(host, repository, m.Digest)
				}
			}
			if len(index.Manifests) == 1 {
				return r.bundleFromManifest(host, repository, index.Manifests[0].Digest)
			}
		}
	} else if resp != nil {
		resp.Body.Close()
	}
	imageDigest := strings.TrimPrefix(digest, "sha256:")
	if imageDigest == digest || imageDigest == "" {
		return nil, fmt.Errorf("invalid image digest %q", digest)
	}
	return r.bundleFromReferrerTag(host, repository, "sha256-"+imageDigest)
}

func (r *HTTPRegistry) bundleFromReferrerTag(host, repository, tag string) ([]byte, error) {
	resp, err := r.request(http.MethodGet, host, "/v2/"+repository+"/manifests/"+tag)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("signature referrer not found")
	}
	var index struct {
		Manifests []struct {
			Digest       string `json:"digest"`
			ArtifactType string `json:"artifactType"`
			MediaType    string `json:"mediaType"`
		} `json:"manifests"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&index); err != nil {
		return nil, err
	}
	for _, m := range index.Manifests {
		if isSignatureDescriptor(m.ArtifactType, m.MediaType) {
			return r.bundleFromManifest(host, repository, m.Digest)
		}
	}
	// GHCR's fallback index can omit artifactType on the descriptor even though
	// the referenced manifest identifies itself as a Sigstore bundle.
	if len(index.Manifests) == 1 {
		return r.bundleFromManifest(host, repository, index.Manifests[0].Digest)
	}
	return nil, fmt.Errorf("signature referrer not found")
}

func (r *HTTPRegistry) bundleFromManifest(host, repository, digest string) ([]byte, error) {
	resp, err := r.request(http.MethodGet, host, "/v2/"+repository+"/manifests/"+digest)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var manifest struct {
		Layers []struct {
			Digest string `json:"digest"`
		} `json:"layers"`
		Blobs []struct {
			Digest string `json:"digest"`
		} `json:"blobs"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&manifest); err != nil {
		return nil, err
	}
	digests := manifest.Layers
	if len(digests) == 0 {
		digests = manifest.Blobs
	}
	if len(digests) == 0 {
		return nil, fmt.Errorf("signature manifest has no bundle layer")
	}
	layer, err := r.request(http.MethodGet, host, "/v2/"+repository+"/blobs/"+digests[0].Digest)
	if err != nil {
		return nil, err
	}
	defer layer.Body.Close()
	if layer.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("cannot download signature bundle: %s", layer.Status)
	}
	var bundle json.RawMessage
	if err := json.NewDecoder(layer.Body).Decode(&bundle); err != nil {
		return nil, err
	}
	return bundle, nil
}

func isSignatureDescriptor(artifactType, mediaType string) bool {
	return artifactType == sigstoreBundleMediaType ||
		mediaType == sigstoreBundleMediaType ||
		strings.Contains(artifactType, "cosign/sign")
}
