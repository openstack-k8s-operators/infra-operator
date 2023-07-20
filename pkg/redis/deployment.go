package redis

import (
	redisv1beta1 "github.com/openstack-k8s-operators/infra-operator/apis/redis/v1beta1"
	labels "github.com/openstack-k8s-operators/lib-common/modules/common/labels"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
)

// Deployment returns a Deployment resource for the Redis CR
func Deployment(r *redisv1beta1.Redis) *appsv1.Deployment {
	matchls := map[string]string{
		"app":   "redis",
		"cr":    "redis-" + r.Name,
		"owner": "infra-operator",
	}
	ls := labels.GetLabels(r, "redis", matchls)

	livenessProbe := &corev1.Probe{
		// TODO might need tuning
		TimeoutSeconds:      5,
		PeriodSeconds:       3,
		InitialDelaySeconds: 3,
	}
	readinessProbe := &corev1.Probe{
		// TODO might need tuning
		TimeoutSeconds:      5,
		PeriodSeconds:       5,
		InitialDelaySeconds: 5,
	}

	// TODO might want to disable probes in 'Debug' mode
	livenessProbe.TCPSocket = &corev1.TCPSocketAction{
		Port: intstr.IntOrString{Type: intstr.Int, IntVal: int32(6379)},
	}
	readinessProbe.TCPSocket = &corev1.TCPSocketAction{
		Port: intstr.IntOrString{Type: intstr.Int, IntVal: int32(6379)},
	}

	deployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      r.Name,
			Namespace: r.Namespace,
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: r.Spec.Replicas,
			Selector: &metav1.LabelSelector{
				MatchLabels: matchls,
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: ls,
				},
				Spec: corev1.PodSpec{
					ServiceAccountName: r.RbacResourceName(),
					Containers: []corev1.Container{{
						Image: r.Spec.ContainerImage,
						Name:  "redis",
						Ports: []corev1.ContainerPort{{
							ContainerPort: 6379,
							Name:          "redis",
						}},
						ReadinessProbe: readinessProbe,
						LivenessProbe:  livenessProbe,
					}},
				},
			},
		},
	}

	return deployment
}
