/*
Copyright 2023.

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

package v1beta1

import (
	"context"
	"fmt"
	"strings"

	condition "github.com/openstack-k8s-operators/lib-common/modules/common/condition"
	"github.com/openstack-k8s-operators/lib-common/modules/common/helper"
	"github.com/openstack-k8s-operators/lib-common/modules/common/util"
	"k8s.io/apimachinery/pkg/types"
)

// IsReady - returns true if service is ready to serve requests
func (instance Redis) IsReady() bool {
	return instance.Status.Conditions.IsTrue(condition.ReadyCondition)
}

// RbacConditionsSet - set the conditions for the rbac object
func (instance Redis) RbacConditionsSet(c *condition.Condition) {
	instance.Status.Conditions.Set(c)
}

// RbacNamespace - return the namespace
func (instance Redis) RbacNamespace() string {
	return instance.Namespace
}

// RbacResourceName - return the name to be used for rbac objects (serviceaccount, role, rolebinding)
func (instance Redis) RbacResourceName() string {
	return "redis-" + instance.Name
}

// SetupDefaults - initializes any CRD field defaults based on environment variables (the defaulting mechanism itself is implemented via webhooks)
func SetupDefaults() {
	// Acquire environmental defaults and initialize Redis defaults with them
	redisDefaults := RedisDefaults{
		ContainerImageURL: util.GetEnvVar("RELATED_IMAGE_INFRA_REDIS_IMAGE_URL_DEFAULT", RedisContainerImage),
	}

	SetupRedisDefaults(redisDefaults)
}

// GetRedisServerListString - return the redis servers as comma separated list
// to be used in OpenStack config.
func (instance *Redis) GetRedisServerListString() string {
	return strings.Join(instance.Status.ServerList, ",")
}

// GetRedisTLSSupport - return the TLS support of the redis instance
func (instance *Redis) GetRedisTLSSupport() bool {
	return instance.Status.TLSSupport
}

// GetRedisPortInt32 - return the port of the redis instance
func (instance *Redis) GetRedisPortInt32() int32 {
	return instance.Status.RedisPort
}

// GetRedisPortString - return the port of the redis instance
func (instance *Redis) GetRedisPortString() string {
	return fmt.Sprintf("%d", instance.Status.RedisPort)
}

// GetRedisByName - gets the Redis instance
func GetRedisByName(
	ctx context.Context,
	h *helper.Helper,
	name string,
	namespace string,
) (*Redis, error) {
	redis := &Redis{}
	err := h.GetClient().Get(
		ctx,
		types.NamespacedName{
			Name:      name,
			Namespace: namespace,
		},
		redis)
	if err != nil {
		return nil, err
	}
	return redis, err
}
