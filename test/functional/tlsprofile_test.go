/*
Copyright 2026 Red Hat

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

package functional_test

import (
	"fmt"

	. "github.com/onsi/ginkgo/v2" //revive:disable:dot-imports
	. "github.com/onsi/gomega"    //revive:disable:dot-imports

	"github.com/openstack-k8s-operators/lib-common/modules/common/util"
	"k8s.io/apimachinery/pkg/types"
)

// createTLSProfile - publish the cluster TLS profile ConfigMap the way the
// openstack-operator does, so the controllers under test pick it up
func createTLSProfile(data map[string]any) {
	name := types.NamespacedName{Name: util.TLSProfileConfigMap, Namespace: namespace}
	th.CreateConfigMap(name, data)
	DeferCleanup(th.DeleteConfigMap, name)
}

// postQuantumProfile - the profile a cluster set to the Modern TLS security
// profile produces: TLS 1.3 only, with the TLS 1.3 ciphersuites alongside a
// pre-1.3 cipher that only the non-1.3 knobs should ever see
var postQuantumProfile = map[string]any{
	"SSLProtocol":    "-all +TLSv1.3",
	"SSLCipherSuite": "TLS_AES_256_GCM_SHA384:TLS_CHACHA20_POLY1305_SHA256:ECDHE-RSA-AES128-GCM-SHA256",
}

var _ = Describe("Cluster TLS profile", func() {
	Describe("Redis", func() {
		var redisName types.NamespacedName

		// redisTLSConf - the rendered redis-tls.conf.in out of the config-data
		// ConfigMap the Redis controller generates
		redisTLSConf := func() string {
			cm := th.GetConfigMap(types.NamespacedName{
				Name:      fmt.Sprintf("%s-config-data", redisName.Name),
				Namespace: redisName.Namespace,
			})
			return cm.Data["redis-tls.conf.in"]
		}

		createRedis := func() {
			redis := CreateRedisConfig(namespace, GetDefaultRedisSpec())
			redisName.Name = redis.GetName()
			redisName.Namespace = redis.GetNamespace()
			DeferCleanup(th.DeleteInstance, redis)
		}

		When("no profile ConfigMap exists", func() {
			BeforeEach(createRedis)

			It("leaves Redis on its own TLS defaults", func() {
				Eventually(func(g Gomega) {
					conf := redisTLSConf()
					g.Expect(conf).To(ContainSubstring("tls-port 6379"))
					g.Expect(conf).NotTo(ContainSubstring("tls-protocols"))
					g.Expect(conf).NotTo(ContainSubstring("tls-ciphers"))
				}, timeout, interval).Should(Succeed())
			})
		})

		When("a TLS 1.3 only profile is published", func() {
			BeforeEach(func() {
				createTLSProfile(postQuantumProfile)
				createRedis()
			})

			It("pins Redis to TLS 1.3 and splits the ciphersuites out", func() {
				Eventually(func(g Gomega) {
					conf := redisTLSConf()
					g.Expect(conf).To(ContainSubstring("tls-protocols \"TLSv1.3\"\n"))
					g.Expect(conf).To(ContainSubstring(
						"tls-ciphersuites TLS_AES_256_GCM_SHA384:TLS_CHACHA20_POLY1305_SHA256\n"))
					g.Expect(conf).To(ContainSubstring("tls-ciphers ECDHE-RSA-AES128-GCM-SHA256\n"))
				}, timeout, interval).Should(Succeed())
			})
		})

		When("the profile only names protocol versions", func() {
			BeforeEach(func() {
				createTLSProfile(map[string]any{"SSLProtocol": "all -SSLv2 -SSLv3 -TLSv1 -TLSv1.1"})
				createRedis()
			})

			It("sets the protocols and leaves the cipher directives out", func() {
				Eventually(func(g Gomega) {
					conf := redisTLSConf()
					g.Expect(conf).To(ContainSubstring("tls-protocols \"TLSv1.2 TLSv1.3\"\n"))
					g.Expect(conf).NotTo(ContainSubstring("tls-ciphers"))
				}, timeout, interval).Should(Succeed())
			})
		})
	})

	Describe("Memcached", func() {
		var memcachedName types.NamespacedName

		// memcachedConf - the rendered memcached options file out of the
		// config-data ConfigMap the Memcached controller generates
		memcachedConf := func() string {
			cm := th.GetConfigMap(types.NamespacedName{
				Name:      fmt.Sprintf("%s-config-data", memcachedName.Name),
				Namespace: memcachedName.Namespace,
			})
			return cm.Data["memcached"]
		}

		createTLSMemcached := func() {
			certSecret := CreateCertSecret(types.NamespacedName{
				Name:      "cert-memcached-svc",
				Namespace: namespace,
			})
			DeferCleanup(k8sClient.Delete, ctx, certSecret)

			spec := GetDefaultMemcachedSpec()
			spec["tls"] = map[string]any{"secretName": certSecret.Name}

			memcached := CreateMemcachedConfig(namespace, spec)
			memcachedName.Name = memcached.GetName()
			memcachedName.Namespace = memcached.GetNamespace()
			DeferCleanup(th.DeleteInstance, memcached)
		}

		When("no profile ConfigMap exists", func() {
			BeforeEach(createTLSMemcached)

			It("leaves memcached on the OpenSSL defaults", func() {
				Eventually(func(g Gomega) {
					conf := memcachedConf()
					g.Expect(conf).To(ContainSubstring("-o ssl_key="))
					g.Expect(conf).NotTo(ContainSubstring("ssl_min_version"))
					g.Expect(conf).NotTo(ContainSubstring("ssl_ciphers"))
				}, timeout, interval).Should(Succeed())
			})
		})

		When("a TLS 1.3 only profile is published", func() {
			BeforeEach(func() {
				createTLSProfile(postQuantumProfile)
				createTLSMemcached()
			})

			It("raises the minimum version and passes only the pre-1.3 ciphers", func() {
				// memcached's ssl_ciphers maps onto SSL_CTX_set_cipher_list,
				// which TLS 1.3 ignores, so the TLS_ prefixed ciphersuites must
				// not end up on the command line
				Eventually(func(g Gomega) {
					conf := memcachedConf()
					g.Expect(conf).To(ContainSubstring("-o ssl_min_version=tlsv1.3"))
					g.Expect(conf).To(ContainSubstring("-o ssl_ciphers=ECDHE-RSA-AES128-GCM-SHA256"))
					g.Expect(conf).NotTo(ContainSubstring("TLS_AES_256_GCM_SHA384"))
				}, timeout, interval).Should(Succeed())
			})
		})

		When("TLS is not enabled on the CR", func() {
			BeforeEach(func() {
				createTLSProfile(postQuantumProfile)

				memcached := CreateMemcachedConfig(namespace, GetDefaultMemcachedSpec())
				memcachedName.Name = memcached.GetName()
				memcachedName.Namespace = memcached.GetNamespace()
				DeferCleanup(th.DeleteInstance, memcached)
			})

			It("does not configure TLS at all", func() {
				Eventually(func(g Gomega) {
					conf := memcachedConf()
					g.Expect(conf).NotTo(ContainSubstring("ssl_min_version"))
					g.Expect(conf).NotTo(ContainSubstring("ssl_ciphers"))
				}, timeout, interval).Should(Succeed())
			})
		})
	})
})
