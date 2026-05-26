package instanceha

import (
	"testing"

	. "github.com/onsi/gomega" //revive:disable:dot-imports

	instancehav1 "github.com/openstack-k8s-operators/infra-operator/apis/instanceha/v1beta1"
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

			dep := Deployment(instance, labels, annotations, "default", "hash123", "test-image:latest", nil, "")

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
