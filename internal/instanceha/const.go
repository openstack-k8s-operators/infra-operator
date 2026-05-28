package instanceha

const (
	// MetricsCertPath is the path to the metrics certificate file
	MetricsCertPath = "/etc/pki/tls/certs/metrics.crt"
	// MetricsKeyPath is the path to the metrics private key file
	MetricsKeyPath = "/etc/pki/tls/private/metrics.key"
	// DefaultMetricsCertSecret is the default secret name for the metrics TLS certificate
	DefaultMetricsCertSecret = "cert-instanceha-metrics" //nolint:gosec
)
