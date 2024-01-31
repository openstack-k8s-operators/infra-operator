package redis

import (
	"fmt"

	redisv1beta1 "github.com/openstack-k8s-operators/infra-operator/apis/redis/v1beta1"
	corev1 "k8s.io/api/core/v1"
)

func getVolumes(r *redisv1beta1.Redis) []corev1.Volume {
	scriptsPerms := int32(0755)
	vols := []corev1.Volume{
		{
			Name: "kolla-config",
			VolumeSource: corev1.VolumeSource{
				ConfigMap: &corev1.ConfigMapVolumeSource{
					LocalObjectReference: corev1.LocalObjectReference{
						Name: fmt.Sprintf("%s-config-data", r.Name),
					},
					Items: []corev1.KeyToPath{
						{
							Key:  "config.json",
							Path: "config.json",
						},
					},
				},
			},
		},
		{
			Name: "kolla-config-sentinel",
			VolumeSource: corev1.VolumeSource{
				ConfigMap: &corev1.ConfigMapVolumeSource{
					LocalObjectReference: corev1.LocalObjectReference{
						Name: fmt.Sprintf("%s-config-data", r.Name),
					},
					Items: []corev1.KeyToPath{
						{
							Key:  "config-sentinel.json",
							Path: "config.json",
						},
					},
				},
			},
		},
		{
			Name: "generated-config-data",
			VolumeSource: corev1.VolumeSource{
				EmptyDir: &corev1.EmptyDirVolumeSource{},
			},
		},
		{
			Name: "config-data",
			VolumeSource: corev1.VolumeSource{
				ConfigMap: &corev1.ConfigMapVolumeSource{
					LocalObjectReference: corev1.LocalObjectReference{
						Name: fmt.Sprintf("%s-config-data", r.Name),
					},
					Items: []corev1.KeyToPath{
						{
							Key:  "sentinel.conf.in",
							Path: "var/lib/redis/sentinel.conf.in",
						},
						{
							Key:  "redis.conf.in",
							Path: "var/lib/redis/redis.conf.in",
						},
					},
				},
			},
		},
		{
			Name: "operator-scripts",
			VolumeSource: corev1.VolumeSource{
				ConfigMap: &corev1.ConfigMapVolumeSource{
					LocalObjectReference: corev1.LocalObjectReference{
						Name: r.Name + "-scripts",
					},
					Items: []corev1.KeyToPath{
						{
							Key:  "start_redis_replication.sh",
							Path: "start_redis_replication.sh",
						},
						{
							Key:  "start_sentinel.sh",
							Path: "start_sentinel.sh",
						},
						{
							Key:  "redis_probe.sh",
							Path: "redis_probe.sh",
						},
						{
							Key:  "check_redis_endpoints.sh",
							Path: "check_redis_endpoints.sh",
						},
						{
							Key:  "common.sh",
							Path: "common.sh",
						},
					},
					DefaultMode: &scriptsPerms,
				},
			},
		},
	}
	return vols
}

func getRedisVolumeMounts() []corev1.VolumeMount {
	vm := []corev1.VolumeMount{{
		MountPath: "/var/lib/config-data/default",
		ReadOnly:  true,
		Name:      "config-data",
	}, {
		MountPath: "/var/lib/config-data/generated",
		Name:      "generated-config-data",
	}, {
		MountPath: "/var/lib/operator-scripts",
		ReadOnly:  true,
		Name:      "operator-scripts",
	}, {
		MountPath: "/var/lib/kolla/config_files",
		ReadOnly:  true,
		Name:      "kolla-config",
	}}
	return vm
}

func getSentinelVolumeMounts() []corev1.VolumeMount {
	vm := []corev1.VolumeMount{{
		MountPath: "/var/lib/config-data/default",
		ReadOnly:  true,
		Name:      "config-data",
	}, {
		MountPath: "/var/lib/config-data/generated",
		Name:      "generated-config-data",
	}, {
		MountPath: "/var/lib/operator-scripts",
		ReadOnly:  true,
		Name:      "operator-scripts",
	}, {
		MountPath: "/var/lib/kolla/config_files",
		ReadOnly:  true,
		Name:      "kolla-config-sentinel",
	}}
	return vm
}
