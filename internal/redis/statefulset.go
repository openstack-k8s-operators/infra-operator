package redis

import (
	"strconv"

	redisv1 "github.com/openstack-k8s-operators/infra-operator/apis/redis/v1beta1"
	topologyv1 "github.com/openstack-k8s-operators/infra-operator/apis/topology/v1beta1"
	common "github.com/openstack-k8s-operators/lib-common/modules/common"
	"github.com/openstack-k8s-operators/lib-common/modules/common/affinity"
	"github.com/openstack-k8s-operators/lib-common/modules/common/clusterdns"
	labels "github.com/openstack-k8s-operators/lib-common/modules/common/labels"
	"github.com/openstack-k8s-operators/lib-common/modules/common/pod"
	"github.com/openstack-k8s-operators/lib-common/modules/common/serviceaccount"
	"github.com/openstack-k8s-operators/lib-common/modules/users"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/utils/ptr"
)

// StatefulSet returns a StatefulSet resource for the Redis CR
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

	replicas := int32(1)
	if r.Spec.Replicas != nil {
		replicas = *r.Spec.Replicas
	}

	// Both the redis and sentinel containers run wait_for_master() before they
	// start listening, so the startup-probe window must cover the worst-case
	// master-discovery time. That time grows with the number of peers, so the
	// failure threshold is derived from the replica count instead of a fixed
	// value that only fits a 3-replica cluster. Once startup succeeds,
	// liveness/readiness use aggressive timing.
	startupPeriod := int32(3)
	startupFailureThreshold := discoveryStartupFailureThreshold(replicas, startupPeriod)

	startupProbe := &corev1.Probe{
		TimeoutSeconds:   5,
		PeriodSeconds:    startupPeriod,
		FailureThreshold: startupFailureThreshold,
		ProbeHandler: corev1.ProbeHandler{
			Exec: &corev1.ExecAction{
				Command: []string{"/var/lib/operator-scripts/redis_probe.sh", "liveness"},
			},
		},
	}
	livenessProbe := &corev1.Probe{
		TimeoutSeconds:      5,
		PeriodSeconds:       3,
		InitialDelaySeconds: 3,
		ProbeHandler: corev1.ProbeHandler{
			Exec: &corev1.ExecAction{
				Command: []string{"/var/lib/operator-scripts/redis_probe.sh", "liveness"},
			},
		},
	}
	readinessProbe := &corev1.Probe{
		TimeoutSeconds:      5,
		PeriodSeconds:       5,
		InitialDelaySeconds: 5,
		ProbeHandler: corev1.ProbeHandler{
			Exec: &corev1.ExecAction{
				Command: []string{"/var/lib/operator-scripts/redis_probe.sh", "readiness"},
			},
		},
	}
	sentinelStartupProbe := &corev1.Probe{
		TimeoutSeconds:   5,
		PeriodSeconds:    startupPeriod,
		FailureThreshold: startupFailureThreshold, // same window as the redis container
		ProbeHandler: corev1.ProbeHandler{
			TCPSocket: &corev1.TCPSocketAction{
				Port: intstr.IntOrString{Type: intstr.Int, IntVal: int32(26379)},
			},
		},
	}
	sentinelLivenessProbe := &corev1.Probe{
		TimeoutSeconds:      5,
		PeriodSeconds:       3,
		InitialDelaySeconds: 5,
		ProbeHandler: corev1.ProbeHandler{
			TCPSocket: &corev1.TCPSocketAction{
				Port: intstr.IntOrString{Type: intstr.Int, IntVal: int32(26379)},
			},
		},
	}
	sentinelReadinessProbe := &corev1.Probe{
		TimeoutSeconds:      5,
		PeriodSeconds:       5,
		InitialDelaySeconds: 5,
		ProbeHandler: corev1.ProbeHandler{
			TCPSocket: &corev1.TCPSocketAction{
				Port: intstr.IntOrString{Type: intstr.Int, IntVal: int32(26379)},
			},
		},
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
	}, {
		Name:  "REPLICAS",
		Value: strconv.Itoa(int(*r.Spec.Replicas)),
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
					ServiceAccountName:           r.RbacResourceName(),
					AutomountServiceAccountToken: ptr.To(false),
					SecurityContext: &corev1.PodSecurityContext{
						FSGroup: ptr.To(users.RedisGID),
					},
					Containers: []corev1.Container{
						{
							Image:   r.Spec.ContainerImage,
							Command: []string{"/usr/bin/dumb-init", "--", "/var/lib/operator-scripts/start_redis_replication.sh"},
							Name:    "redis",
							SecurityContext: func() *corev1.SecurityContext {
								sc := pod.RestrictiveSecurityContext(users.RedisUID, users.RedisGID)
								sc.ReadOnlyRootFilesystem = ptr.To(false)
								return sc
							}(),
							Env:          commonEnvVars,
							Resources:    r.Spec.Resources,
							VolumeMounts: append(getRedisVolumeMounts(r), serviceaccount.KubeAPIAccessVolumeMount()),
							Ports: []corev1.ContainerPort{{
								ContainerPort: 6379,
								Name:          "redis",
							}},
							StartupProbe:   startupProbe,
							LivenessProbe:  livenessProbe,
							ReadinessProbe: readinessProbe,
						}, {
							Image:   r.Spec.ContainerImage,
							Command: []string{"/usr/bin/dumb-init", "--", "/var/lib/operator-scripts/start_sentinel.sh"},
							Name:    "sentinel",
							SecurityContext: func() *corev1.SecurityContext {
								sc := pod.RestrictiveSecurityContext(users.RedisUID, users.RedisGID)
								sc.ReadOnlyRootFilesystem = ptr.To(false)
								return sc
							}(),
							Env: append(commonEnvVars, corev1.EnvVar{
								Name:  "SENTINEL_QUORUM",
								Value: strconv.Itoa((int(*r.Spec.Replicas) / 2) + 1),
							}),
							Resources:    r.Spec.SentinelResources,
							VolumeMounts: append(getSentinelVolumeMounts(r), serviceaccount.KubeAPIAccessVolumeMount()),
							Ports: []corev1.ContainerPort{{
								ContainerPort: 26379,
								Name:          "sentinel",
							}},
							StartupProbe:   sentinelStartupProbe,
							ReadinessProbe: sentinelReadinessProbe,
							LivenessProbe:  sentinelLivenessProbe,
						},
					},
					Volumes: append(getVolumes(r), serviceaccount.KubeAPIAccessVolume()),
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

// discoveryStartupFailureThreshold returns a startup-probe FailureThreshold
// large enough to cover the worst-case wait_for_master() duration in
// common.sh for the given replica count. wait_for_master checks each peer
// sequentially and, per peer, can spend one TIMEOUT (3s) on the Sentinel
// query plus one TIMEOUT (3s) on the redis ROLE fallback, then sleeps
// SENTINEL_RETRY_DELAY (3s) between the SENTINEL_RETRIES (10) attempts.
// These mirror the defaults in templates/redis/bin/common.sh.
func discoveryStartupFailureThreshold(replicas int32, periodSeconds int32) int32 {
	const (
		sentinelRetries = 10 // SENTINEL_RETRIES
		retryDelay      = 3  // SENTINEL_RETRY_DELAY (s)
		perPeerSeconds  = 6  // TIMEOUT sentinel query + TIMEOUT redis ROLE fallback
		marginSeconds   = 30 // headroom for scheduling/config generation
		minThreshold    = 60 // never shorter than the historical 180s window
	)

	peers := replicas - 1
	if peers < 0 {
		peers = 0
	}
	discoverySeconds := sentinelRetries * (int(peers)*perPeerSeconds + retryDelay)

	if periodSeconds < 1 {
		periodSeconds = 1
	}
	threshold := int32((discoverySeconds+marginSeconds)/int(periodSeconds)) + 1
	if threshold < minThreshold {
		threshold = minThreshold
	}
	return threshold
}
