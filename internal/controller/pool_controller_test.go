package controller

import (
	"context"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	factoryv1alpha1 "github.com/alexbrand/software-factory/api/v1alpha1"
)

func newScheme() *runtime.Scheme {
	s := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(s)
	_ = factoryv1alpha1.AddToScheme(s)
	return s
}

func strPtr(s string) *string { return &s }

func newPool(name, namespace string, min, max int32, threshold *string, idleTimeout *metav1.Duration) *factoryv1alpha1.Pool {
	return &factoryv1alpha1.Pool{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			UID:       types.UID("pool-uid-" + name),
		},
		Spec: factoryv1alpha1.PoolSpec{
			AgentConfigRef: factoryv1alpha1.LocalObjectReference{Name: "test-agent"},
			Replicas: factoryv1alpha1.ReplicasConfig{
				Min:              min,
				Max:              max,
				ScaleUpThreshold: threshold,
				IdleTimeout:      idleTimeout,
			},
		},
	}
}

func newSandbox(name, namespace, poolName string, phase factoryv1alpha1.SandboxPhase, ownerUID types.UID) *factoryv1alpha1.Sandbox {
	return &factoryv1alpha1.Sandbox{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			Labels:    map[string]string{"factory.example.com/pool": poolName},
			OwnerReferences: []metav1.OwnerReference{
				{
					APIVersion: "factory.example.com/v1alpha1",
					Kind:       "Pool",
					Name:       poolName,
					UID:        ownerUID,
					Controller: boolPtr(true),
				},
			},
		},
		Spec: factoryv1alpha1.SandboxSpec{
			PoolRef:        factoryv1alpha1.LocalObjectReference{Name: poolName},
			AgentConfigRef: factoryv1alpha1.LocalObjectReference{Name: "test-agent"},
		},
		Status: factoryv1alpha1.SandboxStatus{
			Phase: phase,
		},
	}
}

func boolPtr(b bool) *bool { return &b }

func TestPoolReconciler_Reconcile(t *testing.T) {
	tests := []struct {
		name            string
		pool            *factoryv1alpha1.Pool
		sandboxes       []*factoryv1alpha1.Sandbox
		wantCreated     int
		wantTerminating int
		wantStatus      factoryv1alpha1.PoolStatus
		wantErr         bool
	}{
		{
			name: "create sandboxes to meet minimum replicas",
			pool: newPool("test-pool", "default", 3, 10, nil, nil),
			wantCreated: 3,
			wantStatus: factoryv1alpha1.PoolStatus{
				Creating: 0, // no existing sandboxes were in creating state before reconcile
			},
		},
		{
			name: "no action when minimum is met",
			pool: newPool("test-pool", "default", 2, 10, nil, nil),
			sandboxes: []*factoryv1alpha1.Sandbox{
				newSandbox("sb-1", "default", "test-pool", factoryv1alpha1.SandboxPhaseReady, "pool-uid-test-pool"),
				newSandbox("sb-2", "default", "test-pool", factoryv1alpha1.SandboxPhaseReady, "pool-uid-test-pool"),
			},
			wantCreated: 0,
			wantStatus: factoryv1alpha1.PoolStatus{
				Ready: 2,
			},
		},
		{
			name: "count creating sandboxes toward minimum",
			pool: newPool("test-pool", "default", 3, 10, nil, nil),
			sandboxes: []*factoryv1alpha1.Sandbox{
				newSandbox("sb-1", "default", "test-pool", factoryv1alpha1.SandboxPhaseReady, "pool-uid-test-pool"),
				newSandbox("sb-2", "default", "test-pool", factoryv1alpha1.SandboxPhaseCreating, "pool-uid-test-pool"),
			},
			wantCreated: 1, // ready(1) + creating(1) = 2 < min(3), need 1 more
			wantStatus: factoryv1alpha1.PoolStatus{
				Ready:    1,
				Creating: 1, // the existing creating sandbox
			},
		},
		{
			name: "scale up when threshold exceeded",
			pool: newPool("test-pool", "default", 1, 10, strPtr("0.5"), nil),
			sandboxes: []*factoryv1alpha1.Sandbox{
				newSandbox("sb-1", "default", "test-pool", factoryv1alpha1.SandboxPhaseActive, "pool-uid-test-pool"),
				newSandbox("sb-2", "default", "test-pool", factoryv1alpha1.SandboxPhaseReady, "pool-uid-test-pool"),
				newSandbox("sb-3", "default", "test-pool", factoryv1alpha1.SandboxPhaseActive, "pool-uid-test-pool"),
			},
			wantCreated: 1, // ratio = 2/3 = 0.67 > 0.5
			wantStatus: factoryv1alpha1.PoolStatus{
				Active: 2,
				Ready:  1,
			},
		},
		{
			name: "do not scale up when threshold not exceeded",
			pool: newPool("test-pool", "default", 1, 10, strPtr("0.8"), nil),
			sandboxes: []*factoryv1alpha1.Sandbox{
				newSandbox("sb-1", "default", "test-pool", factoryv1alpha1.SandboxPhaseActive, "pool-uid-test-pool"),
				newSandbox("sb-2", "default", "test-pool", factoryv1alpha1.SandboxPhaseReady, "pool-uid-test-pool"),
				newSandbox("sb-3", "default", "test-pool", factoryv1alpha1.SandboxPhaseReady, "pool-uid-test-pool"),
			},
			wantCreated: 0, // ratio = 1/3 = 0.33 < 0.8
			wantStatus: factoryv1alpha1.PoolStatus{
				Active: 1,
				Ready:  2,
			},
		},
		{
			name: "do not exceed max replicas",
			pool: newPool("test-pool", "default", 2, 3, nil, nil),
			sandboxes: []*factoryv1alpha1.Sandbox{
				newSandbox("sb-1", "default", "test-pool", factoryv1alpha1.SandboxPhaseActive, "pool-uid-test-pool"),
				newSandbox("sb-2", "default", "test-pool", factoryv1alpha1.SandboxPhaseActive, "pool-uid-test-pool"),
				newSandbox("sb-3", "default", "test-pool", factoryv1alpha1.SandboxPhaseActive, "pool-uid-test-pool"),
			},
			wantCreated: 0, // already at max(3)
			wantStatus: factoryv1alpha1.PoolStatus{
				Active: 3,
			},
		},
		{
			name: "scale down idle sandboxes past timeout",
			pool: newPool("test-pool", "default", 1, 10, nil, &metav1.Duration{Duration: 5 * time.Minute}),
			sandboxes: func() []*factoryv1alpha1.Sandbox {
				sb1 := newSandbox("sb-1", "default", "test-pool", factoryv1alpha1.SandboxPhaseReady, "pool-uid-test-pool")
				sb2 := newSandbox("sb-2", "default", "test-pool", factoryv1alpha1.SandboxPhaseIdle, "pool-uid-test-pool")
				oldTime := metav1.NewTime(time.Now().Add(-10 * time.Minute))
				sb2.Status.LastActivityAt = &oldTime
				return []*factoryv1alpha1.Sandbox{sb1, sb2}
			}(),
			wantTerminating: 1,
			wantStatus: factoryv1alpha1.PoolStatus{
				Ready: 1,
			},
		},
		{
			name: "do not scale below minimum on scale down",
			pool: newPool("test-pool", "default", 2, 10, nil, &metav1.Duration{Duration: 5 * time.Minute}),
			sandboxes: func() []*factoryv1alpha1.Sandbox {
				sb1 := newSandbox("sb-1", "default", "test-pool", factoryv1alpha1.SandboxPhaseIdle, "pool-uid-test-pool")
				oldTime := metav1.NewTime(time.Now().Add(-10 * time.Minute))
				sb1.Status.LastActivityAt = &oldTime
				sb2 := newSandbox("sb-2", "default", "test-pool", factoryv1alpha1.SandboxPhaseIdle, "pool-uid-test-pool")
				sb2.Status.LastActivityAt = &oldTime
				return []*factoryv1alpha1.Sandbox{sb1, sb2}
			}(),
			wantTerminating: 0, // total=2, min=2, cannot terminate
			wantStatus: factoryv1alpha1.PoolStatus{
				Idle: 2,
			},
		},
		{
			name: "pool not found returns no error",
			pool: nil,
		},
		{
			name: "ignore terminating sandboxes in counts",
			pool: newPool("test-pool", "default", 2, 10, nil, nil),
			sandboxes: []*factoryv1alpha1.Sandbox{
				newSandbox("sb-1", "default", "test-pool", factoryv1alpha1.SandboxPhaseReady, "pool-uid-test-pool"),
				newSandbox("sb-2", "default", "test-pool", factoryv1alpha1.SandboxPhaseTerminating, "pool-uid-test-pool"),
			},
			wantCreated: 1, // only sb-1 (ready) counts, need 1 more for min=2
			wantStatus: factoryv1alpha1.PoolStatus{
				Ready: 1,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			scheme := newScheme()

			// Build objects list
			var objs []client.Object
			if tt.pool != nil {
				objs = append(objs, tt.pool)
			}
			for _, sb := range tt.sandboxes {
				objs = append(objs, sb)
			}

			// Track creates
			createCount := 0
			fc := fake.NewClientBuilder().
				WithScheme(scheme).
				WithObjects(objs...).
				WithStatusSubresource(&factoryv1alpha1.Pool{}, &factoryv1alpha1.Sandbox{}).
				WithIndex(&factoryv1alpha1.Sandbox{}, "spec.poolRef.name", func(obj client.Object) []string {
					sb, ok := obj.(*factoryv1alpha1.Sandbox)
					if !ok {
						return nil
					}
					return []string{sb.Spec.PoolRef.Name}
				}).
				WithInterceptorFuncs(interceptor.Funcs{
					Create: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
						if _, ok := obj.(*factoryv1alpha1.Sandbox); ok {
							createCount++
						}
						return c.Create(ctx, obj, opts...)
					},
				}).
				Build()

			reconciler := &PoolReconciler{
				Client: fc,
				Scheme: scheme,
			}

			poolName := "test-pool"
			poolNamespace := "default"
			if tt.pool != nil {
				poolName = tt.pool.Name
				poolNamespace = tt.pool.Namespace
			}

			_, err := reconciler.Reconcile(context.Background(), ctrl.Request{
				NamespacedName: types.NamespacedName{
					Name:      poolName,
					Namespace: poolNamespace,
				},
			})

			if (err != nil) != tt.wantErr {
				t.Fatalf("Reconcile() error = %v, wantErr %v", err, tt.wantErr)
			}

			if tt.pool == nil {
				return // pool not found case
			}

			if createCount != tt.wantCreated {
				t.Errorf("expected %d sandbox creates, got %d", tt.wantCreated, createCount)
			}

			// Check terminating count
			if tt.wantTerminating > 0 {
				var sbList factoryv1alpha1.SandboxList
				if err := fc.List(context.Background(), &sbList); err != nil {
					t.Fatalf("listing sandboxes: %v", err)
				}
				terminatingCount := 0
				for _, sb := range sbList.Items {
					if sb.Status.Phase == factoryv1alpha1.SandboxPhaseTerminating {
						terminatingCount++
					}
				}
				if terminatingCount != tt.wantTerminating {
					t.Errorf("expected %d terminating sandboxes, got %d", tt.wantTerminating, terminatingCount)
				}
			}

			// Check pool status
			var updatedPool factoryv1alpha1.Pool
			if err := fc.Get(context.Background(), types.NamespacedName{
				Name: tt.pool.Name, Namespace: tt.pool.Namespace,
			}, &updatedPool); err != nil {
				t.Fatalf("getting pool: %v", err)
			}

			if updatedPool.Status.Ready != tt.wantStatus.Ready {
				t.Errorf("status.ready = %d, want %d", updatedPool.Status.Ready, tt.wantStatus.Ready)
			}
			if updatedPool.Status.Active != tt.wantStatus.Active {
				t.Errorf("status.active = %d, want %d", updatedPool.Status.Active, tt.wantStatus.Active)
			}
			if updatedPool.Status.Idle != tt.wantStatus.Idle {
				t.Errorf("status.idle = %d, want %d", updatedPool.Status.Idle, tt.wantStatus.Idle)
			}
		})
	}
}
