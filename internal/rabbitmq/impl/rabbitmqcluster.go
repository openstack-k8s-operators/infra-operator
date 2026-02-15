// Package impl provides implementation utilities for RabbitMQ cluster operations
package impl

import (
	"context"
	"fmt"
	"time"

	"github.com/openstack-k8s-operators/lib-common/modules/common/helper"
	rabbitmqv2 "github.com/rabbitmq/cluster-operator/v2/api/v1beta1"

	k8s_errors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

// NewRabbitMqCluster returns an initialized RabbitmqCluster.
func NewRabbitMqCluster(
	rabbit *rabbitmqv2.RabbitmqCluster,
	timeout time.Duration,
) *RabbitMqCluster {
	return &RabbitMqCluster{
		rabbitmqCluster: rabbit,
		timeout:         timeout,
	}
}

// CreateOrPatch creates or updates a RabbitMQ cluster resource
func (r *RabbitMqCluster) CreateOrPatch(
	ctx context.Context,
	h *helper.Helper,
) (ctrl.Result, error) {
	rabbitmq := &rabbitmqv2.RabbitmqCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      r.rabbitmqCluster.Name,
			Namespace: r.rabbitmqCluster.Namespace,
		},
	}

	op, err := controllerutil.CreateOrPatch(ctx, h.GetClient(), rabbitmq, func() error {
		// Sync annotations to match desired state (add new ones, remove unwanted ones)
		// This ensures temporary annotations like storage-wipe-needed are removed when no longer needed
		if r.rabbitmqCluster.Annotations != nil {
			if rabbitmq.Annotations == nil {
				rabbitmq.Annotations = make(map[string]string)
			}
			// Add/update annotations from desired spec
			for k, v := range r.rabbitmqCluster.Annotations {
				rabbitmq.Annotations[k] = v
			}
		}
		// Remove annotations that exist on the cluster but not in desired spec
		if rabbitmq.Annotations != nil {
			for k := range rabbitmq.Annotations {
				// Check if this annotation is in the desired spec
				_, existsInDesired := r.rabbitmqCluster.Annotations[k]
				if !existsInDesired {
					delete(rabbitmq.Annotations, k)
				}
			}
		}

		rabbitmq.Spec.Image = r.rabbitmqCluster.Spec.Image
		rabbitmq.Spec.Replicas = r.rabbitmqCluster.Spec.Replicas
		rabbitmq.Spec.Tolerations = r.rabbitmqCluster.Spec.Tolerations
		rabbitmq.Spec.SkipPostDeploySteps = r.rabbitmqCluster.Spec.SkipPostDeploySteps
		rabbitmq.Spec.TerminationGracePeriodSeconds = r.rabbitmqCluster.Spec.TerminationGracePeriodSeconds
		rabbitmq.Spec.DelayStartSeconds = r.rabbitmqCluster.Spec.DelayStartSeconds
		r.rabbitmqCluster.Spec.Service.DeepCopyInto(&rabbitmq.Spec.Service)
		r.rabbitmqCluster.Spec.Persistence.DeepCopyInto(&rabbitmq.Spec.Persistence)
		r.rabbitmqCluster.Spec.Override.DeepCopyInto(&rabbitmq.Spec.Override)
		r.rabbitmqCluster.Spec.SecretBackend.DeepCopyInto(&rabbitmq.Spec.SecretBackend)
		rabbitmq.Spec.Resources = r.rabbitmqCluster.Spec.Resources
		rabbitmq.Spec.Affinity = r.rabbitmqCluster.Spec.Affinity
		r.rabbitmqCluster.Spec.Rabbitmq.DeepCopyInto(&rabbitmq.Spec.Rabbitmq)
		r.rabbitmqCluster.Spec.TLS.DeepCopyInto(&rabbitmq.Spec.TLS)

		err := controllerutil.SetControllerReference(h.GetBeforeObject(), rabbitmq, h.GetScheme())
		if err != nil {
			return err
		}

		return nil
	})
	if err != nil {
		if k8s_errors.IsNotFound(err) {
			h.GetLogger().Info(fmt.Sprintf("RabbitmqCluster %s not found, reconcile in %s", rabbitmq.Name, r.timeout))
			return ctrl.Result{RequeueAfter: r.timeout}, nil
		}
		return ctrl.Result{}, err
	}
	if op != controllerutil.OperationResultNone {
		h.GetLogger().Info(fmt.Sprintf("RabbitmqCluster %s - %s", rabbitmq.Name, op))
	}

	// update the statefulset object of the statefulset type
	r.rabbitmqCluster, err = GetRabbitMqClusterWithName(ctx, h, rabbitmq.GetName(), rabbitmq.GetNamespace())
	if err != nil {
		if k8s_errors.IsNotFound(err) {
			h.GetLogger().Info(fmt.Sprintf("RabbitmqCluster %s not found, reconcile in %s", rabbitmq.Name, r.timeout))
			return ctrl.Result{RequeueAfter: r.timeout}, nil
		}
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

// GetRabbitMqCluster returns the RabbitMQ cluster object
func (r *RabbitMqCluster) GetRabbitMqCluster() rabbitmqv2.RabbitmqCluster {
	return *r.rabbitmqCluster
}

// GetRabbitMqClusterWithName func
func GetRabbitMqClusterWithName(
	ctx context.Context,
	h *helper.Helper,
	name string,
	namespace string,
) (*rabbitmqv2.RabbitmqCluster, error) {
	rabbitmq := &rabbitmqv2.RabbitmqCluster{}
	err := h.GetClient().Get(ctx, types.NamespacedName{Name: name, Namespace: namespace}, rabbitmq)
	if err != nil {
		return rabbitmq, err
	}

	return rabbitmq, nil
}

// Delete - delete a rabbitmqcluster.
func (r *RabbitMqCluster) Delete(
	ctx context.Context,
	h *helper.Helper,
) error {
	err := h.GetClient().Delete(ctx, r.rabbitmqCluster)
	if err != nil && !k8s_errors.IsNotFound(err) {
		err = fmt.Errorf("error deleting rabbitmqcluster %s: %w", r.rabbitmqCluster.Name, err)
		return err
	}

	return nil
}
