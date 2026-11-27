package rabbitmq

import (
	"fmt"

	rabbitmqv1 "github.com/openstack-k8s-operators/infra-operator/apis/rabbitmq/v1beta1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
)

func getVolumes(r *rabbitmqv1.RabbitMq) []corev1.Volume {
	vols := []corev1.Volume{
		{
			Name: "rabbitmq-erlang-cookie",
			VolumeSource: corev1.VolumeSource{
				EmptyDir: &corev1.EmptyDirVolumeSource{},
			},
		},
		{
			Name: "erlang-cookie-secret",
			VolumeSource: corev1.VolumeSource{
				Secret: &corev1.SecretVolumeSource{
					SecretName:  fmt.Sprintf("%s-erlang-cookie", r.Name),
					DefaultMode: ptr.To[int32](0o644),
				},
			},
		},
		{
			Name: "rabbitmq-plugins",
			VolumeSource: corev1.VolumeSource{
				EmptyDir: &corev1.EmptyDirVolumeSource{},
			},
		},
		{
			Name: "plugins-conf",
			VolumeSource: corev1.VolumeSource{
				ConfigMap: &corev1.ConfigMapVolumeSource{
					LocalObjectReference: corev1.LocalObjectReference{
						Name: fmt.Sprintf("%s-plugins-conf", r.Name),
					},
					DefaultMode: ptr.To[int32](0o644),
				},
			},
		},
		{
			Name: "rabbitmq-confd",
			VolumeSource: corev1.VolumeSource{
				Projected: &corev1.ProjectedVolumeSource{
					DefaultMode: ptr.To[int32](0o644),
					Sources: []corev1.VolumeProjection{
						{
							ConfigMap: &corev1.ConfigMapProjection{
								LocalObjectReference: corev1.LocalObjectReference{
									Name: fmt.Sprintf("%s-server-conf", r.Name),
								},
								Items: []corev1.KeyToPath{
									{Key: "operatorDefaults.conf", Path: "operatorDefaults.conf"},
									{Key: "userDefinedConfiguration.conf", Path: "userDefinedConfiguration.conf"},
								},
							},
						},
						{
							Secret: &corev1.SecretProjection{
								LocalObjectReference: corev1.LocalObjectReference{
									Name: fmt.Sprintf("%s-default-user", r.Name),
								},
								Items: []corev1.KeyToPath{
									{Key: "default_user.conf", Path: "default_user.conf"},
								},
							},
						},
					},
				},
			},
		},
		{
			Name: "pod-info",
			VolumeSource: corev1.VolumeSource{
				DownwardAPI: &corev1.DownwardAPIVolumeSource{
					DefaultMode: ptr.To[int32](0o644),
					Items: []corev1.DownwardAPIVolumeFile{
						{
							Path: "startupApproved",
							FieldRef: &corev1.ObjectFieldSelector{
								APIVersion: "v1",
								FieldPath:  "metadata.annotations['" + rabbitmqv1.AnnotationStartupApproved + "']",
							},
						},
						{
							Path: "skipPreStopChecks",
							FieldRef: &corev1.ObjectFieldSelector{
								APIVersion: "v1",
								FieldPath:  "metadata.labels['skipPreStopChecks']",
							},
						},
					},
				},
			},
		},
		{
			Name: "server-conf",
			VolumeSource: corev1.VolumeSource{
				ConfigMap: &corev1.ConfigMapVolumeSource{
					LocalObjectReference: corev1.LocalObjectReference{
						Name: fmt.Sprintf("%s-server-conf", r.Name),
					},
					DefaultMode: ptr.To[int32](0o644),
				},
			},
		},
	}

	// Add TLS volumes if TLS is enabled
	if r.Spec.TLS.SecretName != "" {
		// config-data volume for inter-node TLS config
		vols = append(vols, corev1.Volume{
			Name: "config-data",
			VolumeSource: corev1.VolumeSource{
				ConfigMap: &corev1.ConfigMapVolumeSource{
					LocalObjectReference: corev1.LocalObjectReference{
						Name: fmt.Sprintf("%s-config-data", r.Name),
					},
					DefaultMode: ptr.To[int32](0o644),
					Items: []corev1.KeyToPath{
						{Key: "inter_node_tls.config", Path: "inter_node_tls.config"},
					},
				},
			},
		})

		// rabbitmq-tls projected volume
		vols = append(vols, corev1.Volume{
			Name: "rabbitmq-tls",
			VolumeSource: corev1.VolumeSource{
				Projected: &corev1.ProjectedVolumeSource{
					DefaultMode: ptr.To[int32](0o400),
					Sources: []corev1.VolumeProjection{
						{
							Secret: &corev1.SecretProjection{
								LocalObjectReference: corev1.LocalObjectReference{
									Name: r.Spec.TLS.SecretName,
								},
								Items: []corev1.KeyToPath{
									{Key: "tls.crt", Path: "tls.crt"},
									{Key: "tls.key", Path: "tls.key"},
								},
								Optional: ptr.To(true),
							},
						},
						{
							Secret: &corev1.SecretProjection{
								LocalObjectReference: corev1.LocalObjectReference{
									Name: r.Spec.TLS.CaSecretName,
								},
								Items: []corev1.KeyToPath{
									{Key: "ca.crt", Path: "ca.crt"},
								},
								Optional: ptr.To(true),
							},
						},
					},
				},
			},
		})
	}

	return vols
}

func getVolumeMounts(r *rabbitmqv1.RabbitMq, IPv6Enabled bool) []corev1.VolumeMount {
	vm := []corev1.VolumeMount{
		{
			Name:      "rabbitmq-erlang-cookie",
			MountPath: "/var/lib/rabbitmq/",
		},
		{
			Name:      "persistence",
			MountPath: "/var/lib/rabbitmq/mnesia/",
		},
		{
			Name:      "rabbitmq-plugins",
			MountPath: "/operator",
		},
		{
			Name:      "rabbitmq-confd",
			MountPath: "/etc/rabbitmq/conf.d/10-operatorDefaults.conf",
			SubPath:   "operatorDefaults.conf",
		},
		{
			Name:      "rabbitmq-confd",
			MountPath: "/etc/rabbitmq/conf.d/90-userDefinedConfiguration.conf",
			SubPath:   "userDefinedConfiguration.conf",
		},
		{
			Name:      "pod-info",
			MountPath: "/etc/pod-info/",
		},
		{
			Name:      "rabbitmq-confd",
			MountPath: "/etc/rabbitmq/conf.d/11-default_user.conf",
			SubPath:   "default_user.conf",
		},
	}

	// Only mount advanced.config when it has meaningful content (TLS enabled
	// or user provided advanced config), matching cluster-operator behavior.
	// An always-present file (even with valid empty content "[].\n") can
	// interfere with the image's default configuration handling.
	if r.Spec.TLS.SecretName != "" || r.Spec.Rabbitmq.AdvancedConfig != "" {
		vm = append(vm, corev1.VolumeMount{
			Name:      "server-conf",
			MountPath: "/etc/rabbitmq/advanced.config",
			SubPath:   "advanced.config",
		})
	}

	// Mount rabbitmq-env.conf when user provided EnvConfig
	if r.Spec.Rabbitmq.EnvConfig != "" {
		vm = append(vm, corev1.VolumeMount{
			Name:      "server-conf",
			MountPath: "/etc/rabbitmq/rabbitmq-env.conf",
			SubPath:   "rabbitmq-env.conf",
		})
	}

	// Mount erl_inetrc when user provided ErlangInetConfig or IPv6 is enabled.
	// The -kernel inetrc flag overrides Erlang's default DNS loading,
	// so we only use it when we need custom inet config or {inet6,true}.
	if r.Spec.Rabbitmq.ErlangInetConfig != "" || IPv6Enabled {
		vm = append(vm, corev1.VolumeMount{
			Name:      "server-conf",
			MountPath: "/etc/rabbitmq/erl_inetrc",
			SubPath:   "erl_inetrc",
		})
	}

	// Add TLS volume mounts if TLS is enabled
	if r.Spec.TLS.SecretName != "" {
		vm = append(vm, corev1.VolumeMount{
			Name:      "config-data",
			MountPath: "/etc/rabbitmq/inter-node-tls.config",
			SubPath:   "inter_node_tls.config",
			ReadOnly:  true,
		})
		vm = append(vm, corev1.VolumeMount{
			Name:      "rabbitmq-tls",
			MountPath: "/etc/rabbitmq-tls/",
			ReadOnly:  true,
		})
	}

	return vm
}

func getInitContainerVolumeMounts(_ *rabbitmqv1.RabbitMq) []corev1.VolumeMount {
	return []corev1.VolumeMount{
		{
			Name:      "plugins-conf",
			MountPath: "/tmp/rabbitmq-plugins/",
		},
		{
			Name:      "rabbitmq-erlang-cookie",
			MountPath: "/var/lib/rabbitmq/",
		},
		{
			Name:      "erlang-cookie-secret",
			MountPath: "/tmp/erlang-cookie-secret/",
		},
		{
			Name:      "rabbitmq-plugins",
			MountPath: "/operator",
		},
		{
			Name:      "persistence",
			MountPath: "/var/lib/rabbitmq/mnesia/",
		},
		{
			Name:      "rabbitmq-confd",
			MountPath: "/tmp/default_user.conf",
			SubPath:   "default_user.conf",
		},
	}
}

func getVolumeClaimTemplates(r *rabbitmqv1.RabbitMq) []corev1.PersistentVolumeClaim {
	// Default storage size if not specified
	storageSize := resource.MustParse("10Gi")
	if r.Spec.Persistence.Storage != nil {
		storageSize = *r.Spec.Persistence.Storage
	}

	pvc := corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name: "persistence",
		},
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes: []corev1.PersistentVolumeAccessMode{
				corev1.ReadWriteOnce,
			},
			Resources: corev1.VolumeResourceRequirements{
				Requests: corev1.ResourceList{
					corev1.ResourceStorage: storageSize,
				},
			},
		},
	}

	// Set storage class: prefer explicit spec, fall back to annotation from adoption
	if r.Spec.Persistence.StorageClassName != nil {
		pvc.Spec.StorageClassName = r.Spec.Persistence.StorageClassName
	} else if sc, ok := r.Annotations["rabbitmq.openstack.org/adopted-storage-class"]; ok {
		pvc.Spec.StorageClassName = &sc
	}

	return []corev1.PersistentVolumeClaim{pvc}
}
