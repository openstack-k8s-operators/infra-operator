/*

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package dnsmasq

import (
	"fmt"
	"strconv"
	"strings"

	networkv1 "github.com/openstack-k8s-operators/infra-operator/apis/network/v1beta1"
	topologyv1 "github.com/openstack-k8s-operators/infra-operator/apis/topology/v1beta1"

	common "github.com/openstack-k8s-operators/lib-common/modules/common"
	"github.com/openstack-k8s-operators/lib-common/modules/common/affinity"
	"github.com/openstack-k8s-operators/lib-common/modules/common/env"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/utils/ptr"
)

const (
	// ServiceCommand -
	ServiceCommand = "dnsmasq"
)

// dnsmasqSecurityContext returns the hardened container SecurityContext for
// dnsmasq. It pins no UID/GID so the workload runs under the restricted-v2 SCC
// with a random, admission-assigned UID. ReadOnlyRootFilesystem stays disabled
// to match the existing behavior.
func dnsmasqSecurityContext() *corev1.SecurityContext {
	return &corev1.SecurityContext{
		RunAsNonRoot:             ptr.To(true),
		ReadOnlyRootFilesystem:   ptr.To(false),
		AllowPrivilegeEscalation: ptr.To(false),
		Capabilities: &corev1.Capabilities{
			Drop: []corev1.Capability{"ALL"},
		},
		SeccompProfile: &corev1.SeccompProfile{
			Type: corev1.SeccompProfileTypeRuntimeDefault,
		},
	}
}

// Deployment func
func Deployment(
	instance *networkv1.DNSMasq,
	configHash string,
	labels map[string]string,
	annotations map[string]string,
	cms *corev1.ConfigMapList,
	topology *topologyv1.Topology,
) *appsv1.Deployment {
	terminationGracePeriodSeconds := int64(10)

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

	command := []string{"/bin/bash"}
	args := []string{"-c"}
	initArgs := []string{"-c"}
	dnsmasqCmd := []string{ServiceCommand}
	dnsmasqCmd = append(dnsmasqCmd, "--interface=*")
	dnsmasqCmd = append(dnsmasqCmd, "--conf-dir=/etc/dnsmasq.d")
	dnsmasqCmd = append(dnsmasqCmd, "--hostsdir=/etc/dnsmasq.d/hosts")
	dnsmasqCmd = append(dnsmasqCmd, "--keep-in-foreground")
	// Disable the pidfile: k8s manages the process lifecycle, and writing the
	// default pidfile under /run would fail under the restricted-v2 SCC's
	// random, admission-assigned UID (which doesn't own that image path).
	dnsmasqCmd = append(dnsmasqCmd, "--pid-file=")
	dnsmasqCmd = append(dnsmasqCmd, "--log-debug")
	dnsmasqCmd = append(dnsmasqCmd, "--bind-interfaces")
	dnsmasqCmd = append(dnsmasqCmd, "--listen-address=$(POD_IP)")
	dnsmasqCmd = append(dnsmasqCmd, "--port "+strconv.Itoa(int(DNSTargetPort)))
	// log to stdout
	dnsmasqCmd = append(dnsmasqCmd, "--log-facility=-")
	// dns
	dnsmasqCmd = append(dnsmasqCmd, "--no-hosts")
	dnsmasqCmd = append(dnsmasqCmd, "--domain-needed")
	dnsmasqCmd = append(dnsmasqCmd, "--no-resolv")
	dnsmasqCmd = append(dnsmasqCmd, "--bogus-priv")
	dnsmasqCmd = append(dnsmasqCmd, "--log-queries")

	// append dnsmasqCmd for service container
	args = append(args, strings.Join(dnsmasqCmd, " "))

	// append --test for initcontainer check config syntax
	dnsmasqCmd = append(dnsmasqCmd, "--test")
	initArgs = append(initArgs, strings.Join(dnsmasqCmd, " "))

	//
	// https://kubernetes.io/docs/tasks/configure-pod-container/configure-liveness-readiness-startup-probes/
	//
	livenessProbe.TCPSocket = &corev1.TCPSocketAction{
		Port: intstr.IntOrString{Type: intstr.Int, IntVal: DNSTargetPort},
	}
	readinessProbe.TCPSocket = &corev1.TCPSocketAction{
		Port: intstr.IntOrString{Type: intstr.Int, IntVal: DNSTargetPort},
	}

	envVars := map[string]env.Setter{}
	envVars["POD_IP"] = env.DownwardAPI("status.podIP")
	envVars["CONFIG_HASH"] = env.SetValue(configHash)

	deployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("%s-%s", ServiceName, instance.Name),
			Namespace: instance.Namespace,
		},
		Spec: appsv1.DeploymentSpec{
			Selector: &metav1.LabelSelector{
				MatchLabels: labels,
			},
			Replicas: instance.Spec.Replicas,
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: annotations,
					Labels:      labels,
				},
				Spec: corev1.PodSpec{
					ServiceAccountName:           instance.RbacResourceName(),
					AutomountServiceAccountToken: ptr.To(false),
					SecurityContext: &corev1.PodSecurityContext{
						// No RunAsUser/RunAsGroup/FSGroup pinning: dnsmasq runs
						// under the restricted-v2 SCC, which assigns a random UID
						// and matching fsGroup from the namespace's allotted range.
						RunAsNonRoot: ptr.To(true),
						SeccompProfile: &corev1.SeccompProfile{
							Type: corev1.SeccompProfileTypeRuntimeDefault,
						},
					},
					Volumes: getVolumes(instance.Name, cms),
					InitContainers: []corev1.Container{
						{
							Name:            "init",
							Command:         command,
							Args:            initArgs,
							Image:           instance.Spec.ContainerImage,
							SecurityContext: dnsmasqSecurityContext(),
							Env:             env.MergeEnvs([]corev1.EnvVar{}, envVars),
							VolumeMounts:    getVolumeMounts(instance.Name, cms),
						},
					},
					Containers: []corev1.Container{
						{
							Name:            ServiceName + "-dns",
							Command:         command,
							Args:            args,
							Image:           instance.Spec.ContainerImage,
							SecurityContext: dnsmasqSecurityContext(),
							Env:             env.MergeEnvs([]corev1.EnvVar{}, envVars),
							VolumeMounts:    getVolumeMounts(instance.Name, cms),
							ReadinessProbe:  readinessProbe,
							LivenessProbe:   livenessProbe,
						},
					},
					TerminationGracePeriodSeconds: &terminationGracePeriodSeconds,
				},
			},
		},
	}
	if instance.Spec.NodeSelector != nil {
		deployment.Spec.Template.Spec.NodeSelector = *instance.Spec.NodeSelector
	}

	if topology != nil {
		topology.ApplyTo(&deployment.Spec.Template)
	} else {
		// If possible two pods of the same service should not
		// run on the same worker node. If this is not possible
		// the get still created on the same worker node.
		deployment.Spec.Template.Spec.Affinity = affinity.DistributePods(
			common.AppSelector,
			[]string{
				ServiceName,
			},
			corev1.LabelHostname,
		)
	}
	return deployment
}
