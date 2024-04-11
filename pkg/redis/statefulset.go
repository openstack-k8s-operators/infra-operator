package redis

import (
	"strconv"

	redisv1 "github.com/openstack-k8s-operators/infra-operator/apis/redis/v1beta1"
	common "github.com/openstack-k8s-operators/lib-common/modules/common"
	labels "github.com/openstack-k8s-operators/lib-common/modules/common/labels"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
)

// Deployment returns a Deployment resource for the Redis CR
func StatefulSet(r *redisv1.Redis) *appsv1.StatefulSet {
	matchls := map[string]string{
		common.AppSelector:   "redis",
		common.OwnerSelector: r.Name,
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
	sentinelLivenessProbe := &corev1.Probe{
		// TODO might need tuning
		TimeoutSeconds:      5,
		PeriodSeconds:       3,
		InitialDelaySeconds: 3,
	}
	sentinelReadinessProbe := &corev1.Probe{
		// TODO might need tuning
		TimeoutSeconds:      5,
		PeriodSeconds:       5,
		InitialDelaySeconds: 5,
	}

	livenessProbe.TCPSocket = &corev1.TCPSocketAction{
		Port: intstr.IntOrString{Type: intstr.Int, IntVal: RedisPort},
	}
	readinessProbe.TCPSocket = &corev1.TCPSocketAction{
		Port: intstr.IntOrString{Type: intstr.Int, IntVal: RedisPort},
	}
	sentinelLivenessProbe.TCPSocket = &corev1.TCPSocketAction{
		Port: intstr.IntOrString{Type: intstr.Int, IntVal: SentinelPort},
	}
	sentinelReadinessProbe.TCPSocket = &corev1.TCPSocketAction{
		Port: intstr.IntOrString{Type: intstr.Int, IntVal: SentinelPort},
	}
	name := r.Name + "-" + "redis"

	commonEnvVars := []corev1.EnvVar{{
		Name:  "KOLLA_CONFIG_STRATEGY",
		Value: "COPY_ALWAYS",
	}, {
		Name: "SVC_FQDN",
		// https://github.com/kubernetes/dns/blob/master/docs/specification.md
		// Headless services only publish dns entries that include cluster domain.
		// For the time being, assume this is .cluster.local
		Value: name + "." + r.GetNamespace() + ".svc." + ClusterInternalDomain,
	}}

	sts := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: r.Namespace,
		},
		Spec: appsv1.StatefulSetSpec{
			ServiceName: name,
			Replicas:    r.Spec.Replicas,
			Selector: &metav1.LabelSelector{
				MatchLabels: ls,
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: ls,
				},
				Spec: corev1.PodSpec{
					ServiceAccountName: r.RbacResourceName(),
					Containers: []corev1.Container{{
						Image:        r.Spec.ContainerImage,
						Command:      []string{"/var/lib/operator-scripts/start_redis_replication.sh"},
						Name:         "redis",
						Env:          commonEnvVars,
						VolumeMounts: getRedisVolumeMounts(r),
						Ports: []corev1.ContainerPort{{
							ContainerPort: RedisPort,
							Name:          "redis",
						}},
						LivenessProbe: &corev1.Probe{
							ProbeHandler: corev1.ProbeHandler{
								Exec: &corev1.ExecAction{
									Command: []string{"/var/lib/operator-scripts/redis_probe.sh", "liveness"},
								},
							},
						},
						ReadinessProbe: &corev1.Probe{
							ProbeHandler: corev1.ProbeHandler{
								Exec: &corev1.ExecAction{
									Command: []string{"/var/lib/operator-scripts/redis_probe.sh", "readiness"},
								},
							},
						},
					}, {
						Image:   r.Spec.ContainerImage,
						Command: []string{"/var/lib/operator-scripts/start_sentinel.sh"},

						Name: "sentinel",
						Env: append(commonEnvVars, corev1.EnvVar{
							Name:  "SENTINEL_QUORUM",
							Value: strconv.Itoa((int(*r.Spec.Replicas) / 2) + 1),
						}),
						VolumeMounts: getSentinelVolumeMounts(r),
						Ports: []corev1.ContainerPort{{
							ContainerPort: SentinelPort,
							Name:          "sentinel",
						}},
						ReadinessProbe: sentinelReadinessProbe,
						LivenessProbe:  sentinelLivenessProbe,
					},
					},
					Volumes: getVolumes(r),
				},
			},
		},
	}

	return sts
}
