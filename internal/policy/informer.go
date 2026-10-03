package policy

import (
	"context"
	"log/slog"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
)

var policyGVR = schema.GroupVersionResource{Group: "gerbangkube.example.io", Version: "v1alpha1", Resource: "imagesignaturepolicies"}

func Run(ctx context.Context, client kubernetes.Interface, dynamicClient dynamic.Interface, namespace string, store *Store, logger *slog.Logger) {
	refresh := func() {
		policies, err := dynamicClient.Resource(policyGVR).List(ctx, metav1.ListOptions{})
		if err != nil {
			logger.Error("list image signature policies", "error", err)
			return
		}
		secrets, err := client.CoreV1().Secrets(namespace).List(ctx, metav1.ListOptions{})
		if err != nil {
			logger.Error("list public key secrets", "error", err)
			return
		}
		secretData := make(map[string]map[string][]byte, len(secrets.Items))
		for _, secret := range secrets.Items {
			secretData[secret.Name] = secret.Data
		}
		next := make([]Policy, 0, len(policies.Items))
		for _, item := range policies.Items {
			refs, _, _ := unstructured.NestedStringSlice(item.Object, "spec", "imageReferences")
			secretName, _, _ := unstructured.NestedString(item.Object, "spec", "publicKeySecretRef", "name")
			secretKey, _, _ := unstructured.NestedString(item.Object, "spec", "publicKeySecretRef", "key")
			next = append(next, Policy{Name: item.GetName(), References: refs, SecretName: secretName, SecretKey: secretKey})
		}
		store.ReplacePolicies(next)
		store.ReplaceSecrets(secretData)
	}
	refresh()
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			refresh()
		}
	}
}
