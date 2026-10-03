package main

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/gerbangkube/gerbangkube/internal/admission"
	"github.com/gerbangkube/gerbangkube/internal/policy"
	"github.com/gerbangkube/gerbangkube/internal/verify"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

func main() {
	var addr, certFile, keyFile, namespace, credentialDir string
	flag.StringVar(&addr, "listen-address", ":8443", "HTTPS listen address")
	flag.StringVar(&certFile, "tls-cert-file", "/tls/tls.crt", "TLS certificate path")
	flag.StringVar(&keyFile, "tls-key-file", "/tls/tls.key", "TLS private key path")
	flag.StringVar(&namespace, "system-namespace", "gerbangkube-system", "namespace containing public keys")
	flag.StringVar(&credentialDir, "docker-config-dir", "", "optional directory containing Docker config.json")
	flag.Parse()

	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	cfg, err := rest.InClusterConfig()
	if err != nil {
		logger.Error("create kubernetes config", "error", err)
		os.Exit(1)
	}
	clientset, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		logger.Error("create kubernetes client", "error", err)
		os.Exit(1)
	}
	dynamicClient, err := dynamic.NewForConfig(cfg)
	if err != nil {
		logger.Error("create dynamic kubernetes client", "error", err)
		os.Exit(1)
	}

	ctx := context.Background()
	store := policy.NewStore()
	go policy.Run(ctx, clientset, dynamicClient, namespace, store, logger)

	httpClient := &http.Client{Timeout: 10 * time.Second}
	registry := verify.NewRegistry(httpClient, credentialDir)
	server := admission.NewServer(store, verify.NewBundleVerifier(registry), logger)
	mux := http.NewServeMux()
	mux.Handle("/validate", server)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	})

	tlsConfig := &tls.Config{MinVersion: tls.VersionTLS12}
	httpServer := &http.Server{Addr: addr, Handler: mux, TLSConfig: tlsConfig, ReadHeaderTimeout: 5 * time.Second}
	logger.Info("starting webhook", "address", addr)
	if err := httpServer.ListenAndServeTLS(certFile, keyFile); err != nil && err != http.ErrServerClosed {
		logger.Error("webhook stopped", "error", err)
		os.Exit(1)
	}
}
