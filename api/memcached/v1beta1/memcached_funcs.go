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
	"github.com/openstack-k8s-operators/lib-common/modules/common/tls"
	"github.com/openstack-k8s-operators/lib-common/modules/common/util"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
)

const (
	// CertKey - mtls cert file name
	CertKey = "mtls.crt"
	// PrivateKey - mtls private key file name
	PrivateKey = "mtls.key"
	// CAKey - mtls cacert file name
	CAKey = "mtls-ca.crt"

	// CertPathDst - path to the client certificate
	CertPathDst = "/etc/pki/tls/certs"
	// KeyPathDst - path to the key
	KeyPathDst = "/etc/pki/tls/private"

	// DefaultCertMountDir - default path to mount cert files inside container
	DefaultCertMountDir = "/var/lib/config-data/mtls/certs"
	// DefaultKeyMountDir - default path to mount cert keys inside container
	DefaultKeyMountDir = "/var/lib/config-data/mtls/private"
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

// GetMemcachedMTLSSecret - return the secret containing the MTLS cert
func (instance *Memcached) GetMemcachedMTLSSecret() string {
	if instance.Spec.TLS.MTLS.SslVerifyMode == "Request" || instance.Spec.TLS.MTLS.SslVerifyMode == "Require" {
		secret := *instance.Spec.TLS.MTLS.AuthCertSecret.SecretName
		return secret
	}
	return ""
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

// CreateMTLSVolumeMounts - add volume mount for MTLS certificates and CA certificate
func (instance *Memcached) CreateMTLSVolumeMounts(CertMountPath *string, KeyMountPath *string) []corev1.VolumeMount {
	volumeMounts := []corev1.VolumeMount{}
	if instance.Spec.TLS.MTLS.AuthCertSecret.SecretName != nil {
		CertPath := DefaultCertMountDir + "/" + CertKey
		CaPath := DefaultCertMountDir + "/" + CAKey
		KeyPath := DefaultKeyMountDir + "/" + PrivateKey

		if CertMountPath != nil {
			CertPath = *CertMountPath + "/" + CertKey
			CaPath = *CertMountPath + "/" + CAKey
		}
		if KeyMountPath != nil {
			KeyPath = *KeyMountPath + "/" + PrivateKey
		}

		volumeMounts = append(volumeMounts, corev1.VolumeMount{
			Name:      *instance.Spec.TLS.MTLS.AuthCertSecret.SecretName,
			MountPath: CertPath,
			SubPath:   tls.CertKey,
			ReadOnly:  true,
		}, corev1.VolumeMount{
			Name:      *instance.Spec.TLS.MTLS.AuthCertSecret.SecretName,
			MountPath: KeyPath,
			SubPath:   tls.PrivateKey,
			ReadOnly:  true,
		}, corev1.VolumeMount{
			Name:      *instance.Spec.TLS.MTLS.AuthCertSecret.SecretName,
			MountPath: CaPath,
			SubPath:   tls.CAKey,
			ReadOnly:  true,
		})
	}

	return volumeMounts
}

// CreateMTLSVolume - add volume for MTLS certificates and CA certificate for the service
func (instance *Memcached) CreateMTLSVolume() corev1.Volume {
	volume := corev1.Volume{}
	if instance.Spec.TLS.MTLS.AuthCertSecret.SecretName != nil {
		volume = corev1.Volume{
			Name: *instance.Spec.TLS.MTLS.AuthCertSecret.SecretName,
			VolumeSource: corev1.VolumeSource{
				Secret: &corev1.SecretVolumeSource{
					SecretName:  *instance.Spec.TLS.MTLS.AuthCertSecret.SecretName,
					DefaultMode: ptr.To[int32](0400),
				},
			},
		}
	}

	return volume
}

// CaMountPath - returns path to the ca certificate
func CaMountPath() string {
	return CertPathDst + "/" + CAKey
}

// CertMountPath - returns path to the certificate
func CertMountPath() string {
	return CertPathDst + "/" + CertKey
}

// KeyMountPath - returns path to the key
func KeyMountPath() string {
	return KeyPathDst + "/" + PrivateKey
}
