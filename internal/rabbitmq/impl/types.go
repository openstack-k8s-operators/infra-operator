package impl

import (
	"time"

	rabbitmqv2 "github.com/rabbitmq/cluster-operator/v2/api/v1beta1"
)

// RabbitMqCluster -
type RabbitMqCluster struct {
	rabbitmqCluster *rabbitmqv2.RabbitmqCluster
	timeout         time.Duration
}
