package testharness

import (
	"context"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// WaitFor polls until condition returns true or times out.
func WaitFor(t *testing.T, timeout, interval time.Duration, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(interval)
	}
	t.Fatal("timed out waiting for condition")
}

// CreateNamespace creates a namespace for test isolation.
func (h *Harness) CreateNamespace(ctx context.Context, name string) {
	h.t.Helper()
	ns := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: name},
	}
	if err := h.k8sClient.Create(ctx, ns); err != nil {
		h.t.Fatalf("creating namespace %s: %v", name, err)
	}
}

// SetPodIP waits for a pod to exist and sets its PodIP status.
// In envtest there is no kubelet, so pods never get IPs naturally.
// The sandbox controller creates pods, so we just need to set the IP.
// Retries to handle conflicts from concurrent controller reconciliation.
func (h *Harness) SetPodIP(ctx context.Context, namespace, name, podIP string) {
	h.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		var pod corev1.Pod
		if err := h.k8sClient.Get(ctx, client.ObjectKey{Namespace: namespace, Name: name}, &pod); err != nil {
			time.Sleep(200 * time.Millisecond)
			continue
		}
		pod.Status.PodIP = podIP
		pod.Status.Phase = corev1.PodRunning
		if err := h.k8sClient.Status().Update(ctx, &pod); err == nil {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	h.t.Fatalf("timed out setting pod IP for %s/%s", namespace, name)
}
