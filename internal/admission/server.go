package admission

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gerbangkube/gerbangkube/internal/policy"
	"github.com/gerbangkube/gerbangkube/internal/verify"
	admissionv1 "k8s.io/api/admission/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type cacheEntry struct{ expires time.Time }
type Server struct {
	store    *policy.Store
	verifier verify.Verifier
	logger   *slog.Logger
	mu       sync.Mutex
	cache    map[string]cacheEntry
	ttl      time.Duration
}

func NewServer(store *policy.Store, verifier verify.Verifier, logger *slog.Logger) *Server {
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	return &Server{store: store, verifier: verifier, logger: logger, cache: make(map[string]cacheEntry), ttl: 10 * time.Minute}
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var review admissionv1.AdmissionReview
	if err := json.NewDecoder(r.Body).Decode(&review); err != nil || review.Request == nil {
		http.Error(w, "invalid AdmissionReview", http.StatusBadRequest)
		return
	}
	response := s.decide(review.Request)
	response.UID = review.Request.UID
	_ = json.NewEncoder(w).Encode(admissionv1.AdmissionReview{
		TypeMeta: metav1.TypeMeta{APIVersion: "admission.k8s.io/v1", Kind: "AdmissionReview"},
		Response: response,
	})
}

func (s *Server) decide(req *admissionv1.AdmissionRequest) *admissionv1.AdmissionResponse {
	result := &admissionv1.AdmissionResponse{Allowed: true}
	if req.Resource.Group != "" || req.Resource.Resource != "pods" || req.Operation != admissionv1.Create {
		return result
	}
	var pod corev1.Pod
	if err := json.Unmarshal(req.Object.Raw, &pod); err != nil {
		return deny(req, "invalid pod object")
	}
	images := make([]string, 0, len(pod.Spec.InitContainers)+len(pod.Spec.Containers)+len(pod.Spec.EphemeralContainers))
	for _, c := range pod.Spec.InitContainers {
		images = append(images, c.Image)
	}
	for _, c := range pod.Spec.Containers {
		images = append(images, c.Image)
	}
	for _, c := range pod.Spec.EphemeralContainers {
		images = append(images, c.Image)
	}
	for _, image := range images {
		for _, p := range s.store.Matching(policy.NormalizeImage(image)) {
			digest, reason, err := s.verifyOne(image, p)
			s.logger.Info("admission decision", "namespace", req.Namespace, "pod", pod.Name, "image", image, "digest", digest, "policy", p.Name, "allowed", err == nil, "reason", reason)
			if err != nil {
				return deny(req, fmt.Sprintf("image %s ditolak: %s", image, reason))
			}
		}
	}
	return result
}

func (s *Server) verifyOne(image string, p policy.Policy) (string, string, error) {
	keyID := string(p.PublicKeyPEM)
	cacheKey := image + "|" + p.Name + "|" + keyID
	s.mu.Lock()
	entry, ok := s.cache[cacheKey]
	if ok && time.Now().Before(entry.expires) {
		s.mu.Unlock()
		return "", "signature valid (cache)", nil
	}
	delete(s.cache, cacheKey)
	s.mu.Unlock()
	result, err := s.verifier.Verify(image, p.PublicKeyPEM)
	if err != nil {
		return result.Digest, classify(err), err
	}
	s.mu.Lock()
	s.cache[cacheKey] = cacheEntry{expires: time.Now().Add(s.ttl)}
	s.mu.Unlock()
	return result.Digest, "signature valid", nil
}

func classify(err error) string {
	msg := err.Error()
	switch {
	case strings.Contains(msg, "registry"):
		return "gagal mengakses registry"
	case strings.Contains(msg, "key"):
		return "public key tidak dapat dimuat"
	case strings.Contains(msg, "no signatures") || strings.Contains(msg, "referrer not found"):
		return "signature tidak ditemukan"
	default:
		return "signature tidak valid terhadap public key policy"
	}
}

func deny(req *admissionv1.AdmissionRequest, message string) *admissionv1.AdmissionResponse {
	return &admissionv1.AdmissionResponse{UID: req.UID, Allowed: false, Result: &metav1.Status{Message: message}}
}
