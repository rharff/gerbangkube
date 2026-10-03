package admission

import (
	"encoding/json"
	"testing"

	"github.com/gerbangkube/gerbangkube/internal/policy"
	"github.com/gerbangkube/gerbangkube/internal/verify"
	admissionv1 "k8s.io/api/admission/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

type fakeVerifier struct{ calls int }

func (f *fakeVerifier) Verify(string, []byte) (verify.Result, error) {
	f.calls++
	return verify.Result{Digest: "sha256:test"}, nil
}

func TestDecideSkipsImagesOutsidePolicy(t *testing.T) {
	store := policy.NewStore()
	store.ReplacePolicies([]policy.Policy{{Name: "p", References: []string{"ghcr.io/acme/*"}, SecretName: "s", SecretKey: "k"}})
	v := &fakeVerifier{}
	s := NewServer(store, v, nil)
	raw, _ := json.Marshal(map[string]any{"metadata": map[string]string{"name": "p"}, "spec": map[string]any{"containers": []any{map[string]string{"image": "docker.io/library/alpine:3"}}}})
	resp := s.decide(&admissionv1.AdmissionRequest{Operation: admissionv1.Create, Resource: metav1.GroupVersionResource{Resource: "pods"}, Object: runtime.RawExtension{Raw: raw}})
	if !resp.Allowed || v.calls != 0 {
		t.Fatalf("expected allowed without verification: %#v calls=%d", resp, v.calls)
	}
}
