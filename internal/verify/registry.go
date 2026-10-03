package verify

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

type Registry interface {
	Resolve(ref string) (string, error)
	Bundle(ref, digest string) ([]byte, error)
}

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
	if auth := r.auth[host]; auth != "" {
		req.Header.Set("Authorization", "Basic "+auth)
	}
	resp, err := r.client.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusUnauthorized && resp.Header.Get("WWW-Authenticate") != "" {
		resp.Body.Close()
		return nil, fmt.Errorf("registry authentication required for %s", host)
	}
	return resp, nil
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
				if strings.Contains(m.ArtifactType, "cosign/sign") || strings.Contains(m.MediaType, "artifact.manifest") {
					return r.bundleFromManifest(host, repository, m.Digest)
				}
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
		} `json:"manifests"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&index); err != nil {
		return nil, err
	}
	for _, m := range index.Manifests {
		if strings.Contains(m.ArtifactType, "cosign/sign") {
			return r.bundleFromManifest(host, repository, m.Digest)
		}
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
	}
	if err := json.NewDecoder(resp.Body).Decode(&manifest); err != nil {
		return nil, err
	}
	if len(manifest.Layers) == 0 {
		return nil, fmt.Errorf("signature manifest has no bundle layer")
	}
	layer, err := r.request(http.MethodGet, host, "/v2/"+repository+"/blobs/"+manifest.Layers[0].Digest)
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
