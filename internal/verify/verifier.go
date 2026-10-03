package verify

import (
	"crypto/ecdsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"strings"
)

type Result struct {
	Digest string
	Reason string
}

type Verifier interface {
	Verify(image string, publicKeyPEM []byte) (Result, error)
}

type BundleVerifier struct{ registry Registry }

func NewBundleVerifier(registry Registry) *BundleVerifier { return &BundleVerifier{registry: registry} }

type bundle struct {
	DSSE struct {
		PayloadType string `json:"payloadType"`
		Payload     string `json:"payload"`
		Signatures  []struct {
			Sig string `json:"sig"`
		} `json:"signatures"`
	} `json:"dsseEnvelope"`
}

type statement struct {
	Subject []struct {
		Digest map[string]string `json:"digest"`
	} `json:"subject"`
	PredicateType string `json:"predicateType"`
}

func (v *BundleVerifier) Verify(image string, publicKeyPEM []byte) (Result, error) {
	digest, err := v.registry.Resolve(image)
	if err != nil {
		return Result{}, fmt.Errorf("registry: %w", err)
	}
	if len(publicKeyPEM) == 0 {
		return Result{Digest: digest}, fmt.Errorf("key: public key is missing")
	}
	raw, err := v.registry.Bundle(image, digest)
	if err != nil {
		return Result{Digest: digest}, fmt.Errorf("signature: %w", err)
	}
	var b bundle
	if err := json.Unmarshal(raw, &b); err != nil {
		return Result{Digest: digest}, fmt.Errorf("signature: invalid bundle: %w", err)
	}
	if b.DSSE.PayloadType != "application/vnd.in-toto+json" || len(b.DSSE.Signatures) == 0 {
		return Result{Digest: digest}, fmt.Errorf("signature: no signatures found")
	}
	payload, err := base64.StdEncoding.DecodeString(b.DSSE.Payload)
	if err != nil {
		return Result{Digest: digest}, fmt.Errorf("signature: invalid payload: %w", err)
	}
	var st statement
	if err := json.Unmarshal(payload, &st); err != nil {
		return Result{Digest: digest}, fmt.Errorf("signature: invalid statement: %w", err)
	}
	if st.PredicateType != "https://sigstore.dev/cosign/sign/v1" {
		return Result{Digest: digest}, fmt.Errorf("signature: invalid predicate type")
	}
	matches := false
	for _, subject := range st.Subject {
		if subject.Digest["sha256"] == strings.TrimPrefix(digest, "sha256:") {
			matches = true
		}
	}
	if !matches {
		return Result{Digest: digest}, fmt.Errorf("signature: subject digest does not match image")
	}
	pub, err := parsePublicKey(publicKeyPEM)
	if err != nil {
		return Result{Digest: digest}, fmt.Errorf("key: %w", err)
	}
	pae := []byte("DSSEv1 " + encodeLen([]byte(b.DSSE.PayloadType)) + " " + encodeLen(payload))
	digestHash := sha256.Sum256(pae)
	for _, sig := range b.DSSE.Signatures {
		decoded, err := base64.StdEncoding.DecodeString(sig.Sig)
		if err == nil && ecdsa.VerifyASN1(pub, digestHash[:], decoded) {
			return Result{Digest: digest}, nil
		}
	}
	return Result{Digest: digest}, fmt.Errorf("signature: no matching attestations for public key")
}

func parsePublicKey(data []byte) (*ecdsa.PublicKey, error) {
	block, _ := pem.Decode(data)
	if block == nil {
		return nil, fmt.Errorf("invalid PEM")
	}
	key, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return nil, err
	}
	pub, ok := key.(*ecdsa.PublicKey)
	if !ok {
		return nil, fmt.Errorf("unsupported public key type %T", key)
	}
	return pub, nil
}

func encodeLen(data []byte) string {
	return fmt.Sprintf("%d %s", len(data), string(data))
}
