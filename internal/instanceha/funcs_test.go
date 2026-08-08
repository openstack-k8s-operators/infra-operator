package instanceha

import (
	"testing"

	. "github.com/onsi/gomega" //revive:disable:dot-imports

	instancehav1 "github.com/openstack-k8s-operators/infra-operator/apis/instanceha/v1beta1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestDeploymentDisabledEnvVar(t *testing.T) {

	tests := []struct {
		name     string
		disabled string
		want     string
	}{
		{
			name:     "Disabled is False",
			disabled: "False",
			want:     "False",
		},
		{
			name:     "Disabled is True",
			disabled: "True",
			want:     "True",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := NewWithT(t)

			instance := &instancehav1.InstanceHa{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-instanceha",
					Namespace: "test-namespace",
				},
				Spec: instancehav1.InstanceHaSpec{
					ContainerImage:        "test-image:latest",
					OpenStackCloud:        "default",
					OpenStackConfigMap:    "openstack-config",
					OpenStackConfigSecret: "openstack-config-secret",
					FencingSecret:         "fencing-secret",
					InstanceHaConfigMap:   "instanceha-config",
					InstanceHaKdumpPort:   7410,
					Disabled:              tt.disabled,
				},
			}

			labels := map[string]string{"app": "instanceha"}
			annotations := map[string]string{}

			dep := Deployment(instance, labels, annotations, "default", "hash123", "test-image:latest", nil, "", "", nil)

			// Find the INSTANCEHA_DISABLED env var
			var found bool
			var value string
			for _, envVar := range dep.Spec.Template.Spec.Containers[0].Env {
				if envVar.Name == "INSTANCEHA_DISABLED" {
					found = true
					value = envVar.Value
					break
				}
			}

			g.Expect(found).To(BeTrue(), "INSTANCEHA_DISABLED env var should be set")
			g.Expect(value).To(Equal(tt.want))
		})
	}
}

func TestDeploymentSecurityContext(t *testing.T) {
	g := NewWithT(t)

	instance := &instancehav1.InstanceHa{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-instanceha",
			Namespace: "test-namespace",
		},
		Spec: instancehav1.InstanceHaSpec{
			ContainerImage:        "test-image:latest",
			OpenStackCloud:        "default",
			OpenStackConfigMap:    "openstack-config",
			OpenStackConfigSecret: "openstack-config-secret",
			FencingSecret:         "fencing-secret",
			InstanceHaConfigMap:   "instanceha-config",
			InstanceHaKdumpPort:   7410,
			Disabled:              "False",
		},
	}

	labels := map[string]string{"app": "instanceha"}
	annotations := map[string]string{}

	dep := Deployment(instance, labels, annotations, "default", "hash123", "test-image:latest", nil, "", "", nil)

	container := dep.Spec.Template.Spec.Containers[0]

	g.Expect(container.SecurityContext.ReadOnlyRootFilesystem).NotTo(BeNil())
	g.Expect(*container.SecurityContext.ReadOnlyRootFilesystem).To(BeTrue())

	g.Expect(container.SecurityContext.RunAsNonRoot).NotTo(BeNil())
	g.Expect(*container.SecurityContext.RunAsNonRoot).To(BeTrue())

	g.Expect(container.SecurityContext.RunAsUser).NotTo(BeNil())
	g.Expect(*container.SecurityContext.RunAsUser).To(Equal(int64(42401)))

	g.Expect(container.SecurityContext.RunAsGroup).NotTo(BeNil())
	g.Expect(*container.SecurityContext.RunAsGroup).To(Equal(int64(42401)))

	g.Expect(container.SecurityContext.AllowPrivilegeEscalation).NotTo(BeNil())
	g.Expect(*container.SecurityContext.AllowPrivilegeEscalation).To(BeFalse())

	g.Expect(container.SecurityContext.Capabilities).NotTo(BeNil())
	g.Expect(container.SecurityContext.Capabilities.Drop).To(ContainElement(corev1.Capability("ALL")))

	g.Expect(container.SecurityContext.SeccompProfile).NotTo(BeNil())
	g.Expect(container.SecurityContext.SeccompProfile.Type).To(Equal(corev1.SeccompProfileTypeRuntimeDefault))

	// Verify AutomountServiceAccountToken is disabled
	g.Expect(dep.Spec.Template.Spec.AutomountServiceAccountToken).NotTo(BeNil())
	g.Expect(*dep.Spec.Template.Spec.AutomountServiceAccountToken).To(BeFalse())

	// Verify kube-api-access projected volume exists
	var kubeAPIVolumeFound bool
	for _, vol := range dep.Spec.Template.Spec.Volumes {
		if vol.Name == "kube-api-access" {
			kubeAPIVolumeFound = true
			g.Expect(vol.VolumeSource.Projected).NotTo(BeNil())
			g.Expect(vol.VolumeSource.Projected.Sources).To(HaveLen(3))
			break
		}
	}
	g.Expect(kubeAPIVolumeFound).To(BeTrue(), "kube-api-access projected volume should exist")

	// Verify kube-api-access mount exists
	var kubeAPIMountFound bool
	for _, mount := range container.VolumeMounts {
		if mount.Name == "kube-api-access" && mount.MountPath == "/var/run/secrets/kubernetes.io/serviceaccount" {
			kubeAPIMountFound = true
			g.Expect(mount.ReadOnly).To(BeTrue())
			break
		}
	}
	g.Expect(kubeAPIMountFound).To(BeTrue(), "kube-api-access volume mount should exist")

	// Verify /tmp emptyDir volume exists
	var tmpVolumeFound bool
	for _, vol := range dep.Spec.Template.Spec.Volumes {
		if vol.Name == "tmp" {
			tmpVolumeFound = true
			g.Expect(vol.VolumeSource.EmptyDir).NotTo(BeNil())
			break
		}
	}
	g.Expect(tmpVolumeFound).To(BeTrue(), "/tmp emptyDir volume should exist")

	// Verify /tmp mount exists
	var tmpMountFound bool
	for _, mount := range container.VolumeMounts {
		if mount.Name == "tmp" && mount.MountPath == "/tmp" {
			tmpMountFound = true
			break
		}
	}
	g.Expect(tmpMountFound).To(BeTrue(), "/tmp volume mount should exist")
}
