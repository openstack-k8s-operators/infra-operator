// Package rabbitmq provides utilities for configuring and managing RabbitMQ clusters
package rabbitmq

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	networkv1 "github.com/openstack-k8s-operators/infra-operator/apis/network/v1beta1"
	topologyv1 "github.com/openstack-k8s-operators/infra-operator/apis/topology/v1beta1"
	"github.com/openstack-k8s-operators/lib-common/modules/common/labels"
	"github.com/openstack-k8s-operators/lib-common/modules/common/util"
	rabbitmqv2 "github.com/rabbitmq/cluster-operator/v2/api/v1beta1"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
)

// Version represents a parsed RabbitMQ version
type Version struct {
	Major int
	Minor int
	Patch int
}

// ParseRabbitMQVersion parses a version string like "3.9", "3.13.1", "4.2" into components
func ParseRabbitMQVersion(versionStr string) (Version, error) {
	var v Version
	parts := strings.Split(versionStr, ".")

	if len(parts) < 2 {
		return v, fmt.Errorf("invalid version format: %s", versionStr)
	}

	var err error
	v.Major, err = strconv.Atoi(parts[0])
	if err != nil {
		return v, fmt.Errorf("invalid major version: %s", parts[0])
	}

	v.Minor, err = strconv.Atoi(parts[1])
	if err != nil {
		return v, fmt.Errorf("invalid minor version: %s", parts[1])
	}

	if len(parts) > 2 {
		v.Patch, err = strconv.Atoi(parts[2])
		if err != nil {
			return v, fmt.Errorf("invalid patch version: %s", parts[2])
		}
	}

	return v, nil
}

// ConfigureCluster configures a RabbitMQ cluster with the specified parameters
func ConfigureCluster(
	cluster *rabbitmqv2.RabbitmqCluster,
	IPv6Enabled bool,
	fipsEnabled bool,
	topology *topologyv1.Topology,
	nodeselector *map[string]string,
	override *rabbitmqv2.OverrideTrimmed,
	queueType *string,
	rabbitmqVersion string,
	needsDataWipe bool,
	targetVersion string,
) error {
	envVars := []corev1.EnvVar{
		{
			// The upstream rabbitmq image has /var/log/rabbitmq mode 777, so when
			// openshift runs the rabbitmq container as a random uid it can still write
			// the logs there.  The OSP image however has the directory more constrained,
			// so the random uid cannot write the logs there.  Force it into /var/lib
			// where it can create the file without crashing.
			Name:  "RABBITMQ_UPGRADE_LOG",
			Value: "/var/lib/rabbitmq/rabbitmq_upgrade.log",
		},
		{
			// For some reason HOME needs to be explicitly set here even though the entry
			// for the random user in /etc/passwd has the correct homedir set.
			Name:  "HOME",
			Value: "/var/lib/rabbitmq",
		},
		{
			// The various /usr/sbin/rabbitmq* scripts are really all the same
			// wrapper shell-script that performs some "sanity checks" and then
			// invokes the corresponding "real" program in
			// /usr/lib/rabbitmq/bin.  The main "sanity check" is to ensure that
			// the user running the command is either root or rabbitmq.  Inside
			// of an openshift pod, however, the user is neither of these, so
			// the wrapper script will always fail.

			// By putting the real programs ahead of the wrapper in PATH we can
			// avoid the unnecessary check and just run things directly as
			// whatever user the pod has graciously generated for us.
			Name:  "PATH",
			Value: "/usr/lib/rabbitmq/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
		},
	}

	inetFamily := "inet"
	inetProtocol := "tcp"
	tlsArgs := ""
	fipsArgs := ""
	if IPv6Enabled {
		inetFamily = "inet6"
	}
	erlangInetConfig := fmt.Sprintf("{%s,true}.\n", inetFamily)

	if cluster.Spec.TLS.SecretName != "" {
		inetProtocol = "tls"
		tlsArgs = "-ssl_dist_optfile /etc/rabbitmq/inter-node-tls.config"
		if fipsEnabled {
			fipsArgs = "-crypto fips_mode true"
		}
	}

	envVars = append(envVars, corev1.EnvVar{
		Name: "RABBITMQ_SERVER_ADDITIONAL_ERL_ARGS",
		Value: fmt.Sprintf(
			"-kernel inetrc '/etc/rabbitmq/erl_inetrc' -proto_dist %s_%s %s %s",
			inetFamily,
			inetProtocol,
			tlsArgs,
			fipsArgs,
		),
	}, corev1.EnvVar{
		Name:  "RABBITMQ_CTL_ERL_ARGS",
		Value: fmt.Sprintf("-proto_dist %s_%s %s", inetFamily, inetProtocol, tlsArgs),
	})

	// Build init containers list - add data-wipe container if needed
	initContainers := []corev1.Container{}

	// Add data-wipe init container for storage upgrades
	// This ensures clean state even if the same PV is reused (e.g., local-storage)
	// The init container runs BEFORE setup-container and wipes /var/lib/rabbitmq
	// Controlled by annotation on RabbitMQCluster to ensure it only runs once
	if needsDataWipe {
		// Use version-specific marker to ensure wipe happens only once per version
		// Marker file prevents re-wiping on pod restarts
		wipeScript := fmt.Sprintf(`set -ex
WIPE_DIR="/var/lib/rabbitmq"
MARKER="${WIPE_DIR}/.operator-wipe-%s"

if [ -f "$MARKER" ]; then
  echo "Data already wiped for version %s, skipping..."
  exit 0
fi

echo "Wiping RabbitMQ data in $WIPE_DIR for upgrade to version %s..."
# Remove all content from the volume, including hidden files
rm -rf "${WIPE_DIR:?}"/*
rm -rf "${WIPE_DIR:?}"/.[!.]*
# Create marker to prevent re-wipe
touch "$MARKER"
echo "Data wipe complete for version %s (marker: $MARKER)"
ls -la "$WIPE_DIR"
`, targetVersion, targetVersion, targetVersion, targetVersion)

		initContainers = append(initContainers, corev1.Container{
			Name:       "wipe-data",
			Image:      cluster.Spec.Image,
			WorkingDir: "/var/lib/rabbitmq",
			Command:    []string{"/bin/sh"},
			Args: []string{
				"-c",
				wipeScript,
			},
			VolumeMounts: []corev1.VolumeMount{
				{
					Name:      "persistence",
					MountPath: "/var/lib/rabbitmq",
				},
			},
		})
	}

	// Add the standard setup-container
	initContainers = append(initContainers, corev1.Container{
		Name:            "setup-container",
		SecurityContext: &corev1.SecurityContext{},
	})

	defaultStatefulSet := rabbitmqv2.StatefulSet{
		Spec: &rabbitmqv2.StatefulSetSpec{
			Template: &rabbitmqv2.PodTemplateSpec{
				EmbeddedObjectMeta: &rabbitmqv2.EmbeddedObjectMeta{},
				Spec: &corev1.PodSpec{
					SecurityContext: &corev1.PodSecurityContext{},
					Containers: []corev1.Container{
						{
							// NOTE(gibi): if this is set according to the
							// RabbitMQCluster name the Pod will crash
							Name:  "rabbitmq",
							Image: cluster.Spec.Image,
							Env:   envVars,
							Args: []string{
								// OSP17 runs kolla_start here, instead just run rabbitmq-server directly
								"/usr/lib/rabbitmq/bin/rabbitmq-server",
							},
							VolumeMounts: []corev1.VolumeMount{},
						},
					},
					InitContainers: initContainers,
					Volumes:        []corev1.Volume{},
				},
			},
		},
	}

	cluster.Spec.Override.StatefulSet = &defaultStatefulSet
	if override != nil && override.StatefulSet != nil {
		dec := json.NewDecoder(bytes.NewReader(override.StatefulSet.Raw))
		dec.DisallowUnknownFields()
		err := dec.Decode(&cluster.Spec.Override.StatefulSet)
		if err != nil {
			return err
		}
	}

	// Apply service override if provided
	if override != nil && override.Service != nil {
		cluster.Spec.Override.Service = override.Service
	}

	if cluster.Spec.Override.StatefulSet.Spec.Template.Spec.NodeSelector == nil {
		if nodeselector != nil {
			cluster.Spec.Override.StatefulSet.Spec.Template.Spec.NodeSelector = *nodeselector
		}
	}

	if topology != nil {
		// Get the Topology .Spec
		ts := topology.Spec
		// Process TopologySpreadConstraints if defined in the referenced Topology
		if ts.TopologySpreadConstraints != nil {
			cluster.Spec.Override.StatefulSet.Spec.Template.Spec.TopologySpreadConstraints = *topology.Spec.TopologySpreadConstraints
		}
		// Process Affinity if defined in the referenced Topology
		if ts.Affinity != nil {
			cluster.Spec.Override.StatefulSet.Spec.Template.Spec.Affinity = ts.Affinity
		}
	}
	if cluster.Spec.Affinity == nil {
		cluster.Spec.Affinity = &corev1.Affinity{
			PodAntiAffinity: &corev1.PodAntiAffinity{
				PreferredDuringSchedulingIgnoredDuringExecution: []corev1.WeightedPodAffinityTerm{
					{
						Weight: 100,
						PodAffinityTerm: corev1.PodAffinityTerm{
							LabelSelector: &metav1.LabelSelector{
								MatchExpressions: []metav1.LabelSelectorRequirement{
									{
										Key:      labels.K8sAppName,
										Operator: metav1.LabelSelectorOpIn,
										Values: []string{
											cluster.Name,
										},
									},
								},
							},
							TopologyKey: labels.K8sHostname,
						},
					},
				},
			},
		}
	}
	cluster.Spec.Rabbitmq.ErlangInetConfig = erlangInetConfig
	cluster.Spec.Rabbitmq.AdvancedConfig = ""

	if cluster.Spec.TLS.SecretName != "" {
		if cluster.Spec.TLS.CaSecretName == "" {
			cluster.Spec.TLS.CaSecretName = cluster.Spec.TLS.SecretName
		}
		// disable non tls listeners
		cluster.Spec.TLS.DisableNonTLSListeners = true
		// Determine TLS version configuration based on RabbitMQ version
		// NOTE(dciabrin) OSPRH-20331 reported RabbitMQ partitioning during
		// key update events, so revert to OSP17 configuration scheme (see OSPRH-13633)
		// For RabbitMQ 4.x and later, TLS 1.3 is enabled regardless of FIPS mode
		// as the partitioning issue is resolved in RabbitMQ 4.x
		var tlsVersions string
		parsedVersion, err := ParseRabbitMQVersion(rabbitmqVersion)
		if err == nil && parsedVersion.Major >= 4 {
			// RabbitMQ 4.x+: Enable TLS 1.2 and 1.3
			tlsVersions = "['tlsv1.2','tlsv1.3']"
		} else if fipsEnabled {
			// RabbitMQ 3.x with FIPS: Enable TLS 1.2 and 1.3
			tlsVersions = "['tlsv1.2','tlsv1.3']"
		} else {
			// RabbitMQ 3.x without FIPS: TLS 1.2 only (OSPRH-20331 workaround)
			tlsVersions = "['tlsv1.2']"
		}
		// NOTE(dciabrin) RabbitMQ/Erlang needs a specific TLS configuration ordering
		// in ssl_options.versions for TLS to work with FIPS. We cannot enforce the right
		// ordering with AdditionalConfig, we have to pass a specific Erlang value via
		// the AdvancedConfig field. We also add configuration flags which were known to
		// work with FIPS in previous version of Openstack.
		cluster.Spec.Rabbitmq.AdvancedConfig = fmt.Sprintf(`[
{ssl, [{protocol_version, %s}]},
{rabbit, [
{ssl_options, [
  {cacertfile,"/etc/rabbitmq-tls/ca.crt"},
  {certfile,"/etc/rabbitmq-tls/tls.crt"},
  {keyfile,"/etc/rabbitmq-tls/tls.key"},
  {depth,1},
  {secure_renegotiate,true},
  {reuse_sessions,true},
  {honor_cipher_order,false},
  {honor_ecc_order,false},
  {verify,verify_none},
  {fail_if_no_peer_cert,false},
  {versions, %s}
]}
]},
{rabbitmq_management, [
{ssl_config, [
  {ip,"::"},
  {cacertfile,"/etc/rabbitmq-tls/ca.crt"},
  {certfile,"/etc/rabbitmq-tls/tls.crt"},
  {keyfile,"/etc/rabbitmq-tls/tls.key"},
  {depth,1},
  {secure_renegotiate,true},
  {reuse_sessions,true},
  {honor_cipher_order,false},
  {honor_ecc_order,false},
  {verify,verify_none},
  {fail_if_no_peer_cert,false},
  {versions, %s}
]}
]},
{client, [
{cacertfile, "/etc/rabbitmq-tls/ca.crt"},
{verify,verify_peer},
{secure_renegotiate,true},
{versions, %s}
]}
].
`, tlsVersions, tlsVersions, tlsVersions, tlsVersions)

		cluster.Spec.Override.StatefulSet.Spec.Template.Spec.Volumes = append(
			cluster.Spec.Override.StatefulSet.Spec.Template.Spec.Volumes,
			corev1.Volume{
				Name: "config-data",
				VolumeSource: corev1.VolumeSource{
					ConfigMap: &corev1.ConfigMapVolumeSource{
						LocalObjectReference: corev1.LocalObjectReference{
							Name: fmt.Sprintf("%s-config-data", cluster.Name),
						},
						DefaultMode: ptr.To[int32](0o420),
						Items: []corev1.KeyToPath{
							{
								Key:  "inter_node_tls.config",
								Path: "inter_node_tls.config",
							},
						},
					},
				},
			},
		)
		cluster.Spec.Override.StatefulSet.Spec.Template.Spec.Containers[0].VolumeMounts = append(
			cluster.Spec.Override.StatefulSet.Spec.Template.Spec.Containers[0].VolumeMounts,
			corev1.VolumeMount{
				MountPath: "/etc/rabbitmq/inter-node-tls.config",
				ReadOnly:  true,
				Name:      "config-data",
				SubPath:   "inter_node_tls.config",
			},
		)
	}

	if cluster.Spec.Override.Service != nil &&
		cluster.Spec.Override.Service.Spec != nil &&
		cluster.Spec.Override.Service.Spec.Type == corev1.ServiceTypeLoadBalancer {
		if cluster.Spec.Override.Service.EmbeddedLabelsAnnotations == nil {
			cluster.Spec.Override.Service.EmbeddedLabelsAnnotations = &rabbitmqv2.EmbeddedLabelsAnnotations{}
		}

		// add annotation to register service name in dnsmasq
		hostname := fmt.Sprintf("%s.%s.svc", cluster.Name, cluster.Namespace)
		cluster.Spec.Override.Service.Annotations = util.MergeStringMaps(cluster.Spec.Override.Service.Annotations,
			map[string]string{networkv1.AnnotationHostnameKey: hostname})
	}

	// This is the same situation as RABBITMQ_UPGRADE_LOG above,
	// except for the "main" rabbitmq log we can just force it to use the console.

	// By default the prometheus and management endpoints always bind to ipv4.
	// We need to set the correct address based on the IP version in use.
	settings := []string{
		"log.console = true",
		"prometheus.tcp.ip = ::",
		"management.tcp.ip = ::",
	}
	if cluster.Spec.TLS.SecretName != "" {
		settings = append(settings, "ssl_options.verify = verify_none", "prometheus.ssl.ip = ::")
		// management ssl ip needs to be set in the AdvancedConfig
	}

	// Configure node-wide default queue type and migration settings (RabbitMQ 4.2+ only)
	// These configuration options are only available in RabbitMQ 4.2 and later
	// https://www.rabbitmq.com/docs/vhosts#node-wide-default-queue-type-node-wide-dqt
	// https://www.rabbitmq.com/docs/vhosts#migration-to-quorum-queues-a-way-to-relax-queue-property-equivalence-checks
	if queueType != nil && *queueType == "Quorum" && len(rabbitmqVersion) > 0 && rabbitmqVersion[0] >= '4' {
		settings = append(settings,
			// Set default queue type to quorum - all newly declared queues will be quorum type
			// This prevents services from accidentally creating classic queues
			"default_queue_type = quorum",
			// Disable classic queue mirroring to prevent accidental creation of mirrored queues
			// Mirrored queues are deprecated in RabbitMQ 4.2+
			"deprecated_features.permit.classic_queue_mirroring = false",
			// Relax queue property equivalence checks on redeclaration
			// This prevents channel exceptions when services redeclare queues after migration
			// from classic/mirrored to quorum queues with different properties
			"quorum_queue.property_equivalence.relaxed_checks_on_redeclaration = true",
		)
	}

	additionalDefaults := strings.Join(settings, "\n")

	// If additionalConfig is empty set let's our defaults, append otherwise.
	if cluster.Spec.Rabbitmq.AdditionalConfig == "" {
		cluster.Spec.Rabbitmq.AdditionalConfig = additionalDefaults
	} else {
		cluster.Spec.Rabbitmq.AdditionalConfig = additionalDefaults + "\n" + cluster.Spec.Rabbitmq.AdditionalConfig
	}

	return nil
}
