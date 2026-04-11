package testharness

import (
	"context"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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

// CreateFakePod creates a minimal Pod object with a PodIP so that the session
// controller can resolve the bridge endpoint. In envtest there is no kubelet,
// so pods never get IPs naturally.
func (h *Harness) CreateFakePod(ctx context.Context, namespace, name, podIP string) {
	h.t.Helper()
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{Name: "sdk", Image: "fake:latest"},
			},
		},
	}
	if err := h.k8sClient.Create(ctx, pod); err != nil {
		h.t.Fatalf("creating fake pod %s/%s: %v", namespace, name, err)
	}

	// Set pod IP in status.
	pod.Status.PodIP = podIP
	pod.Status.Phase = corev1.PodRunning
	if err := h.k8sClient.Status().Update(ctx, pod); err != nil {
		h.t.Fatalf("updating fake pod status %s/%s: %v", namespace, name, err)
	}
}
