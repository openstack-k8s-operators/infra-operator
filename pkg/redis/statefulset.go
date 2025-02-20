package redis

import (
	"strconv"

	redisv1 "github.com/openstack-k8s-operators/infra-operator/apis/redis/v1beta1"
	topologyv1 "github.com/openstack-k8s-operators/infra-operator/apis/topology/v1beta1"
	common "github.com/openstack-k8s-operators/lib-common/modules/common"
	"github.com/openstack-k8s-operators/lib-common/modules/common/affinity"
	"github.com/openstack-k8s-operators/lib-common/modules/common/clusterdns"
	labels "github.com/openstack-k8s-operators/lib-common/modules/common/labels"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
)

// Deployment returns a Deployment resource for the Redis CR
func StatefulSet(
	r *redisv1.Redis,
	configHash string,
	topology *topologyv1.Topology,
) *appsv1.StatefulSet {
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
		Port: intstr.IntOrString{Type: intstr.Int, IntVal: int32(6379)},
	}
	readinessProbe.TCPSocket = &corev1.TCPSocketAction{
		Port: intstr.IntOrString{Type: intstr.Int, IntVal: int32(6379)},
	}
	sentinelLivenessProbe.TCPSocket = &corev1.TCPSocketAction{
		Port: intstr.IntOrString{Type: intstr.Int, IntVal: int32(26379)},
	}
	sentinelReadinessProbe.TCPSocket = &corev1.TCPSocketAction{
		Port: intstr.IntOrString{Type: intstr.Int, IntVal: int32(26379)},
	}
	name := r.Name + "-" + "redis"
	clusterDomain := clusterdns.GetDNSClusterDomain()

	commonEnvVars := []corev1.EnvVar{{
		Name:  "KOLLA_CONFIG_STRATEGY",
		Value: "COPY_ALWAYS",
	}, {
		Name: "SVC_FQDN",
		// https://github.com/kubernetes/dns/blob/master/docs/specification.md
		// Headless services only publish dns entries that include cluster domain.
		Value: name + "." + r.GetNamespace() + ".svc." + clusterDomain,
	}, {
		Name:  "CONFIG_HASH",
		Value: configHash,
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
							ContainerPort: 6379,
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
							ContainerPort: 26379,
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

	if r.Spec.NodeSelector != nil {
		sts.Spec.Template.Spec.NodeSelector = *r.Spec.NodeSelector
	}

	if topology != nil {
		topology.ApplyTo(&sts.Spec.Template)
	} else {
		// If possible two pods of the same service should not
		// run on the same worker node. If this is not possible
		// the get still created on the same worker node.
		sts.Spec.Template.Spec.Affinity = affinity.DistributePods(
			common.AppSelector,
			[]string{
				r.Name,
			},
			corev1.LabelHostname,
		)
	}
	return sts
}
