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
	"strings"

	condition "github.com/openstack-k8s-operators/lib-common/modules/common/condition"
	"github.com/openstack-k8s-operators/lib-common/modules/common/helper"
	"github.com/openstack-k8s-operators/lib-common/modules/common/util"
	"k8s.io/apimachinery/pkg/types"
)

// IsReady - returns true if Memcached is reconciled successfully
func (instance Memcached) IsReady() bool {
	return instance.Status.Conditions.IsTrue(condition.ReadyCondition)
}

// RbacConditionsSet - set the conditions for the rbac object
func (instance Memcached) RbacConditionsSet(c *condition.Condition) {
	instance.Status.Conditions.Set(c)
}

// RbacNamespace - return the namespace
func (instance Memcached) RbacNamespace() string {
	return instance.Namespace
}

// RbacResourceName - return the name to be used for rbac objects (serviceaccount, role, rolebinding)
func (instance Memcached) RbacResourceName() string {
	return "memcached-" + instance.Name
}

// SetupDefaults - initializes any CRD field defaults based on environment variables (the defaulting mechanism itself is implemented via webhooks)
func SetupDefaults() {
	// Acquire environmental defaults and initialize Memcached defaults with them
	memcachedDefaults := MemcachedDefaults{
		ContainerImageURL: util.GetEnvVar("RELATED_IMAGE_INFRA_MEMCACHED_IMAGE_URL_DEFAULT", MemcachedContainerImage),
	}

	SetupMemcachedDefaults(memcachedDefaults)
}

// GetMemcachedServerListString - return the memcached servers as comma separated list
// to be used in OpenStack config.
func (instance *Memcached) GetMemcachedServerListString() string {
	return strings.Join(instance.Status.ServerList, ",")
}

// GetMemcachedServerListQuotedString - return the memcached servers, each quoted, as comma separated list
// to be used in OpenStack config.
func (instance *Memcached) GetMemcachedServerListQuotedString() string {
	return "'" + strings.Join(instance.Status.ServerList, "','") + "'"
}

// GetMemcachedServerListWithInetString - return the memcached servers as comma separated list
// to be used in OpenStack config.
func (instance *Memcached) GetMemcachedServerListWithInetString() string {
	return strings.Join(instance.Status.ServerListWithInet, ",")
}

// GetMemcachedServerListWithInetQuotedString - return the memcached servers, each quoted, as comma separated list
// to be used in OpenStack config.
func (instance *Memcached) GetMemcachedServerListWithInetQuotedString() string {
	return "'" + strings.Join(instance.Status.ServerListWithInet, "','") + "'"
}

// GetMemcachedTLSSupport - return the TLS support of the memcached instance
func (instance *Memcached) GetMemcachedTLSSupport() bool {
	return instance.Status.TLSSupport
}

// GetMemcachedByName - gets the Memcached instance
func GetMemcachedByName(
	ctx context.Context,
	h *helper.Helper,
	name string,
	namespace string,
) (*Memcached, error) {
	memcached := &Memcached{}
	err := h.GetClient().Get(
		ctx,
		types.NamespacedName{
			Name:      name,
			Namespace: namespace,
		},
		memcached)
	if err != nil {
		return nil, err
	}
	return memcached, err
}
