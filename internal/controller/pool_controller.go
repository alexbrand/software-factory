package controller

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	factoryv1alpha1 "github.com/alexbrand/software-factory/api/v1alpha1"
)

// PoolReconciler reconciles a Pool object.
type PoolReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=factory.example.com,resources=pools,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=factory.example.com,resources=pools/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=factory.example.com,resources=sandboxes,verbs=get;list;watch;create;update;patch;delete

func (r *PoolReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	// Fetch the Pool
	var pool factoryv1alpha1.Pool
	if err := r.Get(ctx, req.NamespacedName, &pool); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// List all sandboxes owned by this pool
	var sandboxList factoryv1alpha1.SandboxList
	if err := r.List(ctx, &sandboxList,
		client.InNamespace(pool.Namespace),
		client.MatchingFields{"spec.poolRef.name": pool.Name},
	); err != nil {
		return ctrl.Result{}, fmt.Errorf("listing sandboxes: %w", err)
	}

	// Categorize sandboxes by phase
	var ready, active, idle, creating int32
	var idleSandboxes []factoryv1alpha1.Sandbox
	for i := range sandboxList.Items {
		sb := &sandboxList.Items[i]
		if sb.Status.Phase == factoryv1alpha1.SandboxPhaseTerminating {
			continue
		}
		switch sb.Status.Phase {
		case factoryv1alpha1.SandboxPhaseReady:
			ready++
		case factoryv1alpha1.SandboxPhaseActive, factoryv1alpha1.SandboxPhaseAssigned:
			active++
		case factoryv1alpha1.SandboxPhaseIdle:
			idle++
			idleSandboxes = append(idleSandboxes, sandboxList.Items[i])
		case factoryv1alpha1.SandboxPhaseCreating, "":
			creating++
		}
	}

	totalNonTerminating := ready + active + idle + creating

	// Ensure minimum replicas: if total non-terminating sandboxes < min, create new ones
	deficit := pool.Spec.Replicas.Min - totalNonTerminating
	if deficit > 0 {
		// Don't exceed max
		canCreate := pool.Spec.Replicas.Max - totalNonTerminating
		if canCreate < deficit {
			deficit = canCreate
		}
		for i := int32(0); i < deficit; i++ {
			if err := r.createSandbox(ctx, &pool); err != nil {
				return ctrl.Result{}, fmt.Errorf("creating sandbox for min replicas: %w", err)
			}
			creating++
			totalNonTerminating++
		}
		logger.Info("ensured minimum replicas", "created", deficit)
	}

	// Scale up: when active/(active+ready) exceeds scaleUpThreshold
	if pool.Spec.Replicas.ScaleUpThreshold != nil && (active+ready) > 0 {
		threshold, err := strconv.ParseFloat(*pool.Spec.Replicas.ScaleUpThreshold, 64)
		if err == nil {
			ratio := float64(active) / float64(active+ready)
			if ratio > threshold {
				// Create one sandbox per reconciliation to avoid over-provisioning
				if totalNonTerminating < pool.Spec.Replicas.Max {
					if err := r.createSandbox(ctx, &pool); err != nil {
						return ctrl.Result{}, fmt.Errorf("creating sandbox for scale-up: %w", err)
					}
					creating++
					totalNonTerminating++
					logger.Info("scaled up", "ratio", ratio, "threshold", threshold)
				}
			}
		}
	}

	// Scale down: terminate idle sandboxes beyond idleTimeout when count > min
	if pool.Spec.Replicas.IdleTimeout != nil && totalNonTerminating > pool.Spec.Replicas.Min {
		timeout := pool.Spec.Replicas.IdleTimeout.Duration
		// Sort idle sandboxes oldest first by creation timestamp
		sort.Slice(idleSandboxes, func(i, j int) bool {
			return idleSandboxes[i].CreationTimestamp.Before(&idleSandboxes[j].CreationTimestamp)
		})
		now := time.Now()
		for i := range idleSandboxes {
			if totalNonTerminating <= pool.Spec.Replicas.Min {
				break
			}
			sb := &idleSandboxes[i]
			idleSince := sb.Status.LastActivityAt
			if idleSince == nil {
				idleSince = &sb.CreationTimestamp
			}
			if now.Sub(idleSince.Time) > timeout {
				sb.Status.Phase = factoryv1alpha1.SandboxPhaseTerminating
				if err := r.Status().Update(ctx, sb); err != nil {
					return ctrl.Result{}, fmt.Errorf("setting sandbox to terminating: %w", err)
				}
				totalNonTerminating--
				idle--
				logger.Info("terminated idle sandbox", "sandbox", sb.Name)
			}
		}
	}

	// Update Pool status
	pool.Status.Ready = ready
	pool.Status.Active = active
	pool.Status.Idle = idle
	pool.Status.Creating = creating
	if err := r.Status().Update(ctx, &pool); err != nil {
		return ctrl.Result{}, fmt.Errorf("updating pool status: %w", err)
	}

	// Requeue periodically to handle idle timeouts and scaling
	return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
}

func (r *PoolReconciler) createSandbox(ctx context.Context, pool *factoryv1alpha1.Pool) error {
	sandbox := &factoryv1alpha1.Sandbox{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: pool.Name + "-",
			Namespace:    pool.Namespace,
			Labels: map[string]string{
				"factory.example.com/pool": pool.Name,
			},
		},
		Spec: factoryv1alpha1.SandboxSpec{
			PoolRef:        factoryv1alpha1.LocalObjectReference{Name: pool.Name},
			AgentConfigRef: pool.Spec.AgentConfigRef,
			Resources:      pool.Spec.SandboxTemplate.Resources,
		},
	}

	if err := ctrl.SetControllerReference(pool, sandbox, r.Scheme); err != nil {
		return fmt.Errorf("setting controller reference: %w", err)
	}

	if err := r.Create(ctx, sandbox); err != nil {
		return fmt.Errorf("creating sandbox: %w", err)
	}

	return nil
}

func (r *PoolReconciler) SetupWithManager(mgr ctrl.Manager) error {
	// Index sandboxes by pool name for efficient listing
	if err := mgr.GetFieldIndexer().IndexField(context.Background(), &factoryv1alpha1.Sandbox{}, "spec.poolRef.name", func(obj client.Object) []string {
		sandbox, ok := obj.(*factoryv1alpha1.Sandbox)
		if !ok {
			return nil
		}
		return []string{sandbox.Spec.PoolRef.Name}
	}); err != nil {
		return fmt.Errorf("setting up field indexer: %w", err)
	}

	return ctrl.NewControllerManagedBy(mgr).
		For(&factoryv1alpha1.Pool{}).
		Owns(&factoryv1alpha1.Sandbox{}).
		Complete(r)
}
