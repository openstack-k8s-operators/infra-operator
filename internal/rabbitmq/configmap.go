package rabbitmq

import (
	"fmt"
	"strings"

	rabbitmqv1 "github.com/openstack-k8s-operators/infra-operator/apis/rabbitmq/v1beta1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// GeneratePluginsConfigMap generates the plugins ConfigMap (rabbitmq-plugins-conf)
func GeneratePluginsConfigMap(r *rabbitmqv1.RabbitMq) *corev1.ConfigMap {
	plugins := []string{
		"rabbitmq_peer_discovery_k8s",
		"rabbitmq_prometheus",
		"rabbitmq_management",
	}
	for _, p := range r.Spec.Rabbitmq.AdditionalPlugins {
		plugins = append(plugins, string(p))
	}

	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("%s-plugins-conf", r.Name),
			Namespace: r.Namespace,
		},
		Data: map[string]string{
			"enabled_plugins": fmt.Sprintf("[%s].", strings.Join(plugins, ",")),
		},
	}
}

// GenerateServerConfigMap generates the server configuration ConfigMap (rabbitmq-server-conf).
// configVersion determines version-specific settings (TLS, quorum defaults).
func GenerateServerConfigMap(
	r *rabbitmqv1.RabbitMq,
	IPv6Enabled bool,
	fipsEnabled bool,
	configVersion string,
	proxyEnabled bool,
) *corev1.ConfigMap {
	operatorDefaults := buildOperatorDefaults(r, IPv6Enabled, configVersion, proxyEnabled)
	userConfig := r.Spec.Rabbitmq.AdditionalConfig
	advancedConfig := buildAdvancedConfig(r, IPv6Enabled, fipsEnabled, configVersion)

	data := map[string]string{
		"operatorDefaults.conf":         operatorDefaults,
		"userDefinedConfiguration.conf": userConfig,
		"advanced.config":               advancedConfig,
	}

	// Include rabbitmq-env.conf if user provided EnvConfig
	if r.Spec.Rabbitmq.EnvConfig != "" {
		data["rabbitmq-env.conf"] = r.Spec.Rabbitmq.EnvConfig
	}

	// Include erl_inetrc: user-provided ErlangInetConfig takes precedence,
	// otherwise generate default IPv6 config when IPv6 is enabled.
	// The -kernel inetrc flag (set in cluster.go) overrides Erlang's default
	// DNS loading, so the inetrc must explicitly include {resolv_conf,...}
	// and {hosts_file,...} to restore system DNS resolution.
	if r.Spec.Rabbitmq.ErlangInetConfig != "" {
		data["erl_inetrc"] = r.Spec.Rabbitmq.ErlangInetConfig
	} else if IPv6Enabled {
		data["erl_inetrc"] = "{inet6,true}.\n{resolv_conf, \"/etc/resolv.conf\"}.\n{hosts_file, \"/etc/hosts\"}.\n"
	}

	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("%s-server-conf", r.Name),
			Namespace: r.Namespace,
		},
		Data: data,
	}
}

// GenerateConfigDataConfigMap generates the config-data ConfigMap for inter-node TLS.
// configVersion determines version-specific TLS settings.
func GenerateConfigDataConfigMap(
	r *rabbitmqv1.RabbitMq,
	fipsEnabled bool,
	configVersion string,
) *corev1.ConfigMap {
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("%s-config-data", r.Name),
			Namespace: r.Namespace,
		},
		Data: map[string]string{},
	}

	if r.Spec.TLS.SecretName != "" {
		cm.Data["inter_node_tls.config"] = buildInterNodeTLSConfig(fipsEnabled, configVersion)
	}

	return cm
}

func buildOperatorDefaults(r *rabbitmqv1.RabbitMq, IPv6Enabled bool, configVersion string, proxyEnabled bool) string {
	var config []string

	config = append(config, "disk_free_limit.absolute                   = 2GB")
	config = append(config, "cluster_partition_handling                 = pause_minority")
	config = append(config, "cluster_formation.peer_discovery_backend   = rabbit_peer_discovery_k8s")

	// RabbitMQ 4.1+ rewrote rabbit_peer_discovery_k8s to use a seed-node
	// approach (parsing the StatefulSet node name) instead of querying the
	// K8s API for endpoints. The k8s.host, k8s.address_type, and
	// k8s.service_name config options are silently ignored in 4.x.
	// We include them for all versions to match the current cluster-operator
	// behavior (see rabbitmq/cluster-operator#1865) and because they are
	// harmless on 4.x (just emit deprecation warnings).
	config = append(config, "cluster_formation.k8s.host                 = kubernetes.default")
	config = append(config, "cluster_formation.k8s.address_type         = hostname")
	config = append(config, fmt.Sprintf("cluster_formation.k8s.service_name         = %s-nodes", r.Name))

	// RabbitMQ 4.x renamed queue_master_locator to queue_leader_locator.
	if IsVersion4OrLater(configVersion) {
		config = append(config, "queue_leader_locator                       = balanced")
	} else {
		config = append(config, "queue_master_locator                       = min-masters")
	}

	config = append(config, fmt.Sprintf("cluster_formation.target_cluster_size_hint = %d", getReplicaCount(r)))
	config = append(config, fmt.Sprintf("cluster_name                               = %s", r.Name))
	config = append(config, "auth_mechanisms.1                          = PLAIN")
	config = append(config, "auth_mechanisms.2                          = AMQPLAIN")
	config = append(config, "log.console                                = true")
	config = append(config, "log.console.level                          = info")

	// During migration (proxy active), relax quorum queue redeclaration checks so
	// clients that redeclare queues with classic-queue properties don't get errors.
	if proxyEnabled && IsVersion4OrLater(configVersion) {
		config = append(config,
			"quorum_queue.property_equivalence.relaxed_checks_on_redeclaration = true",
		)
	}

	// Enable Continuous Membership Reconciliation (CMR) for multi-node clusters
	// on RabbitMQ 4.x. CMR automatically grows quorum queues onto nodes that are
	// missing from the membership, replacing the need for manual
	// `rabbitmq-queues grow` calls after upgrades or rolling restarts.
	// trigger_interval (10s) fires when a node joins, covering rolling restarts.
	// interval (60min default) is a background safety net.
	if IsVersion4OrLater(configVersion) && getReplicaCount(r) > 1 {
		config = append(config,
			"quorum_queue.continuous_membership_reconciliation.enabled            = true",
			fmt.Sprintf("quorum_queue.continuous_membership_reconciliation.target_group_size = %d", getReplicaCount(r)),
			"quorum_queue.continuous_membership_reconciliation.auto_remove        = false",
		)
	}

	// Prometheus and management bind address
	config = append(config, "prometheus.tcp.ip                          = ::")
	config = append(config, "management.tcp.ip                          = ::")
	config = append(config, "vm_memory_high_watermark.relative           = 0.6")

	// Proxy mode: RabbitMQ listens on localhost only, proxy handles client connections.
	// The plain TCP backend listener on localhost is always required so the proxy
	// can reach RabbitMQ, even when DisableNonTLSListeners is true (which only
	// affects client-facing listeners).
	if proxyEnabled {
		loopbackAddr := "127.0.0.1"
		if IPv6Enabled {
			loopbackAddr = "::1"
		}
		config = append(config, fmt.Sprintf("listeners.tcp.1                            = %s:%d", loopbackAddr, BackendPort))
		if r.Spec.TLS.SecretName != "" {
			config = append(config, fmt.Sprintf("listeners.ssl.1                            = %s:%d", loopbackAddr, BackendTLSPort))
			config = append(config, fmt.Sprintf("listeners.ssl.default                      = %s:%d", loopbackAddr, BackendTLSPort))
		}
	} else if r.Spec.TLS.SecretName != "" {
		// TLS listener configuration (normal mode)
		config = append(config, "listeners.ssl.default                      = 5671")
		config = append(config, "management.ssl.port                        = 15671")
		config = append(config, "prometheus.ssl.port                        = 15691")
		if r.Spec.TLS.DisableNonTLSListeners {
			config = append(config, "listeners.tcp                              = none")
		} else {
			// When TLS is enabled but non-TLS listeners are not disabled,
			// explicitly set management/prometheus TCP ports so they remain
			// accessible alongside the TLS ports (matches cluster-operator).
			config = append(config, "management.tcp.port                        = 15672")
			config = append(config, "prometheus.tcp.port                        = 15692")
		}
	}

	// Management/Prometheus TLS ports (needed in both proxy and non-proxy modes)
	if proxyEnabled && r.Spec.TLS.SecretName != "" {
		config = append(config, "management.ssl.port                        = 15671")
		config = append(config, "prometheus.ssl.port                        = 15691")
	}

	return strings.Join(config, "\n") + "\n"
}

func getReplicaCount(r *rabbitmqv1.RabbitMq) int32 {
	if r.Spec.Replicas != nil {
		return *r.Spec.Replicas
	}
	return 1
}

func buildAdvancedConfig(r *rabbitmqv1.RabbitMq, _ bool, fipsEnabled bool, configVersion string) string {
	// If user provided advanced config, use it
	if r.Spec.Rabbitmq.AdvancedConfig != "" {
		return r.Spec.Rabbitmq.AdvancedConfig
	}

	// If TLS is not enabled, return valid empty Erlang config
	// (an empty string causes RabbitMQ to fail parsing the file)
	if r.Spec.TLS.SecretName == "" {
		return "[].\n"
	}

	tlsVersions := TLSVersionsForRabbitMQ(configVersion, fipsEnabled)

	return fmt.Sprintf(`[
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
}

func buildInterNodeTLSConfig(fipsEnabled bool, configVersion string) string {
	tlsVersions := TLSVersionsForRabbitMQ(configVersion, fipsEnabled)

	// Use verify_none for inter-node TLS distribution. OTP 26's default
	// pkix_verify_hostname does not support wildcard certificate matching,
	// and the HTTPS match function (public_key:pkix_verify_hostname_match_fun)
	// cannot be used in a static -ssl_dist_optfile because function calls
	// are not valid Erlang terms. Without wildcard support, verify_peer
	// rejects connections since pod hostnames don't have exact SAN matches.
	//
	// With verify_none the CA certificate chain is still validated;
	// only SNI-based hostname matching is skipped.
	return fmt.Sprintf(`[
  {server, [
    {cacertfile, "/etc/rabbitmq-tls/ca.crt"},
    {certfile, "/etc/rabbitmq-tls/tls.crt"},
    {keyfile, "/etc/rabbitmq-tls/tls.key"},
    {secure_renegotiate, true},
    {verify, verify_none},
    {fail_if_no_peer_cert, false},
    {versions, %s}
  ]},
  {client, [
    {cacertfile, "/etc/rabbitmq-tls/ca.crt"},
    {certfile, "/etc/rabbitmq-tls/tls.crt"},
    {keyfile, "/etc/rabbitmq-tls/tls.key"},
    {secure_renegotiate, true},
    {verify, verify_none},
    {versions, %s}
  ]}
].
`, tlsVersions, tlsVersions)
}
