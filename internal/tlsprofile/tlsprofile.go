// Package tlsprofile reads the cluster-wide TLS security profile published by
// the openstack-operator and translates it into the native configuration
// syntax of the services infra-operator manages.
//
// The profile is published as the well-known ConfigMap named by
// util.TLSProfileConfigMap, whose keys use Apache mod_ssl vocabulary
// (SSLProtocol, SSLCipherSuite) because that is what lib-common's shared
// ssl.conf template consumes. None of memcached, Redis or RabbitMQ speak
// Apache, so lib-common's automatic merge into util.Template.ConfigOptions
// does not help them on its own: the values have to be parsed and re-rendered
// per daemon. That is what this package does.
//
// A missing ConfigMap means "no cluster-wide policy" and is not an error: every
// renderer returns the empty string for the zero Profile, so callers keep the
// defaults they had before. A ConfigMap that exists but holds a value we cannot
// parse *is* an error, because quietly falling back would downgrade a setting a
// cluster administrator deliberately mandated.
package tlsprofile

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/openstack-k8s-operators/lib-common/modules/common/helper"
	"github.com/openstack-k8s-operators/lib-common/modules/common/util"
	corev1 "k8s.io/api/core/v1"
	k8s_errors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
)

// Keys read out of the profile ConfigMap. They are named for the Apache
// directives lib-common's ssl.conf renders them into.
const (
	sslProtocolKey    = "SSLProtocol"
	sslCipherSuiteKey = "SSLCipherSuite"
)

// Version identifies a TLS protocol version. The values are ordered, so they
// can be compared to find the lowest version a profile allows.
type Version int

// Supported TLS versions, lowest first. SSLv2 and SSLv3 are deliberately not
// represented: they are recognised when parsing so a profile mentioning them
// is not rejected, but we never enable them in a rendered config.
const (
	// VersionTLS10 - TLS 1.0
	VersionTLS10 Version = iota
	// VersionTLS11 - TLS 1.1
	VersionTLS11
	// VersionTLS12 - TLS 1.2
	VersionTLS12
	// VersionTLS13 - TLS 1.3
	VersionTLS13
)

// apacheName maps the tokens accepted in an Apache SSLProtocol directive to the
// versions we model. Entries mapping to nothing are versions we refuse to
// enable; they are listed so that parsing recognises rather than rejects them.
var apacheNames = map[string]Version{
	"tlsv1":   VersionTLS10,
	"tlsv1.0": VersionTLS10,
	"tlsv1.1": VersionTLS11,
	"tlsv1.2": VersionTLS12,
	"tlsv1.3": VersionTLS13,
}

// deadProtocols are SSLProtocol tokens we parse but never act on.
var deadProtocols = map[string]bool{"sslv2": true, "sslv3": true}

// redisNames are the spellings Redis expects in tls-protocols.
var redisNames = map[Version]string{
	VersionTLS10: "TLSv1",
	VersionTLS11: "TLSv1.1",
	VersionTLS12: "TLSv1.2",
	VersionTLS13: "TLSv1.3",
}

// memcachedNames are the spellings memcached expects in ssl_min_version.
var memcachedNames = map[Version]string{
	VersionTLS10: "tlsv1.0",
	VersionTLS11: "tlsv1.1",
	VersionTLS12: "tlsv1.2",
	VersionTLS13: "tlsv1.3",
}

// cipherToken matches the characters OpenSSL cipher names and the operators of
// a cipher list are built from. Rendered values end up inside a shell-sourced
// file (memcached) and a Redis config directive, so anything outside this set
// is rejected rather than escaped: the profile is written by a cluster
// administrator, not a user, and a value needing escaping is a sign the
// ConfigMap holds something other than a cipher list.
var cipherToken = regexp.MustCompile(`^[A-Za-z0-9_.+:!@=-]+$`)

// Profile is the cluster TLS policy reduced to the pieces our services can act
// on. The zero value means no policy was published.
type Profile struct {
	// Versions are the TLS versions the profile enables, ascending. Nil when
	// the profile said nothing about protocol versions.
	Versions []Version
	// Ciphers is the TLS 1.2-and-below cipher list, in OpenSSL names.
	Ciphers []string
	// CipherSuites is the TLS 1.3 ciphersuite list, in OpenSSL names. TLS 1.3
	// is configured through a separate knob from earlier versions in both
	// OpenSSL and Redis, so the two are kept apart here too.
	CipherSuites []string
}

// Get reads the cluster TLS profile from the well-known ConfigMap in namespace.
//
// A missing ConfigMap yields the zero Profile and no error. Any other read
// failure, or a ConfigMap holding a value we cannot parse, is returned as an
// error so the caller surfaces it rather than silently rendering a weaker
// config than the cluster mandates.
func Get(ctx context.Context, h *helper.Helper, namespace string) (Profile, error) {
	cm := &corev1.ConfigMap{}
	err := h.GetClient().Get(ctx, types.NamespacedName{
		Name:      util.TLSProfileConfigMap,
		Namespace: namespace,
	}, cm)
	if err != nil {
		if k8s_errors.IsNotFound(err) {
			return Profile{}, nil
		}
		return Profile{}, fmt.Errorf("error reading TLS profile ConfigMap %s/%s: %w",
			namespace, util.TLSProfileConfigMap, err)
	}

	return parse(cm.Data)
}

// Predicate narrows a ConfigMap watch down to the profile ConfigMap, so that a
// controller reconciles when the cluster TLS policy changes without waking up
// for every other ConfigMap in the namespace.
func Predicate() predicate.Predicate {
	return predicate.And(
		predicate.NewPredicateFuncs(func(o client.Object) bool {
			return o.GetName() == util.TLSProfileConfigMap
		}),
		predicate.ResourceVersionChangedPredicate{},
	)
}

// parse turns the raw ConfigMap data into a Profile.
func parse(data map[string]string) (Profile, error) {
	var p Profile
	var err error

	if raw, ok := data[sslProtocolKey]; ok {
		p.Versions, err = parseProtocols(raw)
		if err != nil {
			return Profile{}, fmt.Errorf("invalid %s in TLS profile ConfigMap %s: %w",
				sslProtocolKey, util.TLSProfileConfigMap, err)
		}
	}

	if raw, ok := data[sslCipherSuiteKey]; ok {
		p.Ciphers, p.CipherSuites, err = parseCiphers(raw)
		if err != nil {
			return Profile{}, fmt.Errorf("invalid %s in TLS profile ConfigMap %s: %w",
				sslCipherSuiteKey, util.TLSProfileConfigMap, err)
		}
	}

	return p, nil
}

// parseProtocols evaluates an Apache SSLProtocol directive, e.g. "-all +TLSv1.3"
// or "all -SSLv2 -SSLv3 -TLSv1 -TLSv1.1", and returns the enabled versions in
// ascending order.
//
// Tokens are applied left to right, as Apache does: "all" turns everything on,
// "-all" turns everything off, and "+X"/"X"/"-X" toggle a single version.
func parseProtocols(raw string) ([]Version, error) {
	enabled := map[Version]bool{}

	for _, token := range strings.Fields(raw) {
		on := true
		switch token[0] {
		case '-':
			on, token = false, token[1:]
		case '+':
			token = token[1:]
		}
		name := strings.ToLower(token)

		if name == "all" {
			for _, v := range apacheNames {
				enabled[v] = on
			}
			continue
		}
		if deadProtocols[name] {
			continue
		}

		v, ok := apacheNames[name]
		if !ok {
			return nil, fmt.Errorf("unknown protocol %q", token)
		}
		enabled[v] = on
	}

	versions := []Version{}
	for v, on := range enabled {
		if on {
			versions = append(versions, v)
		}
	}
	if len(versions) == 0 {
		return nil, fmt.Errorf("%q enables no TLS version we support", raw)
	}
	sort.Slice(versions, func(i, j int) bool { return versions[i] < versions[j] })

	return versions, nil
}

// parseCiphers splits a colon-separated OpenSSL cipher list into the TLS 1.3
// ciphersuites and everything else. The two are told apart by name: TLS 1.3
// ciphersuites are the only ones OpenSSL spells with a "TLS_" prefix.
func parseCiphers(raw string) (ciphers []string, cipherSuites []string, err error) {
	for _, token := range strings.Split(raw, ":") {
		token = strings.TrimSpace(token)
		if token == "" {
			continue
		}
		if !cipherToken.MatchString(token) {
			return nil, nil, fmt.Errorf("unexpected characters in cipher %q", token)
		}
		if strings.HasPrefix(token, "TLS_") {
			cipherSuites = append(cipherSuites, token)
		} else {
			ciphers = append(ciphers, token)
		}
	}

	return ciphers, cipherSuites, nil
}

// RedisProtocols renders the profile's versions for the Redis tls-protocols
// directive, e.g. "TLSv1.2 TLSv1.3". Empty when the profile has no opinion.
func (p Profile) RedisProtocols() string {
	names := make([]string, 0, len(p.Versions))
	for _, v := range p.Versions {
		names = append(names, redisNames[v])
	}

	return strings.Join(names, " ")
}

// RedisCiphers renders the TLS 1.2-and-below cipher list for the Redis
// tls-ciphers directive. Empty when the profile has no opinion.
func (p Profile) RedisCiphers() string {
	return strings.Join(p.Ciphers, ":")
}

// RedisCipherSuites renders the TLS 1.3 ciphersuites for the Redis
// tls-ciphersuites directive. Empty when the profile has no opinion.
func (p Profile) RedisCipherSuites() string {
	return strings.Join(p.CipherSuites, ":")
}

// MemcachedMinVersion renders the lowest version the profile allows, for
// memcached's ssl_min_version option. Empty when the profile has no opinion.
//
// memcached can only express a floor, so a profile that enables a
// non-contiguous set of versions is flattened to its lowest member. That errs
// towards accepting a connection the profile would have refused rather than
// refusing one it would have accepted, but no such profile exists in practice:
// the OpenShift TLSSecurityProfile types only ever produce a floor.
func (p Profile) MemcachedMinVersion() string {
	if len(p.Versions) == 0 {
		return ""
	}

	return memcachedNames[p.Versions[0]]
}

// MemcachedCiphers renders the cipher list for memcached's ssl_ciphers option.
//
// Only the TLS 1.2-and-below ciphers are included: ssl_ciphers maps onto
// SSL_CTX_set_cipher_list, which TLS 1.3 ignores. memcached exposes no
// equivalent of SSL_CTX_set_ciphersuites, so on a TLS 1.3 connection it keeps
// the OpenSSL defaults and the profile's ciphersuite list has no effect.
func (p Profile) MemcachedCiphers() string {
	return strings.Join(p.Ciphers, ":")
}
