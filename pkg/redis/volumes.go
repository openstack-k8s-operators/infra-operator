package redis

import (
	"fmt"

	redisv1 "github.com/openstack-k8s-operators/infra-operator/apis/redis/v1beta1"
	"github.com/openstack-k8s-operators/lib-common/modules/common/tls"
	corev1 "k8s.io/api/core/v1"
)

const (
	RedisCertPrefix = "redis"
)

func getVolumes(r *redisv1.Redis) []corev1.Volume {
	scriptsPerms := int32(0755)
	configDataFiles := []corev1.KeyToPath{
		{
			Key:  "sentinel.conf.in",
			Path: "var/lib/redis/sentinel.conf.in",
		},
		{
			Key:  "redis.conf.in",
			Path: "var/lib/redis/redis.conf.in",
		},
	}
	if r.Spec.TLS.Enabled() {
		configDataFiles = append(configDataFiles, []corev1.KeyToPath{
			{
				Key:  "redis-tls.conf.in",
				Path: "var/lib/redis/redis-tls.conf.in",
			},
			{
				Key:  "sentinel-tls.conf.in",
				Path: "var/lib/redis/sentinel-tls.conf.in",
			},
		}...)
	}

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
					Items: configDataFiles,
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

	if r.Spec.TLS.Enabled() {
		svc := tls.Service{
			SecretName: *r.Spec.TLS.GenericService.SecretName,
			CertMount:  nil,
			KeyMount:   nil,
			CaMount:    nil,
		}
		serviceVolume := svc.CreateVolume(RedisCertPrefix)
		vols = append(vols, serviceVolume)
		if r.Spec.TLS.Ca.CaBundleSecretName != "" {
			caVolume := r.Spec.TLS.Ca.CreateVolume()
			vols = append(vols, caVolume)
		}
	}

	return vols
}

func getTLSVolumeMounts(r *redisv1.Redis) []corev1.VolumeMount {
	var vols []corev1.VolumeMount
	if r.Spec.TLS.Enabled() {
		svc := tls.Service{
			SecretName: *r.Spec.TLS.GenericService.SecretName,
			CertMount:  nil,
			KeyMount:   nil,
			CaMount:    nil,
		}
		serviceVolumeMounts := svc.CreateVolumeMounts(RedisCertPrefix)
		vols = serviceVolumeMounts
		if r.Spec.TLS.Ca.CaBundleSecretName != "" {
			caVolumeMounts := r.Spec.TLS.Ca.CreateVolumeMounts(nil)
			vols = append(vols, caVolumeMounts...)
		}
	}
	return vols
}

func getRedisVolumeMounts(r *redisv1.Redis) []corev1.VolumeMount {
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
	vm = append(vm, getTLSVolumeMounts(r)...)
	return vm
}

func getSentinelVolumeMounts(r *redisv1.Redis) []corev1.VolumeMount {
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
	vm = append(vm, getTLSVolumeMounts(r)...)
	return vm
}
