// Package bgp provides BGP configuration utilities for network management
package bgp

import (
	"context"
	"net"

	corev1 "k8s.io/api/core/v1"
	k8s_errors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	k8s_networkv1 "github.com/k8snetworkplumbingwg/network-attachment-definition-client/pkg/apis/k8s.cni.cncf.io/v1"
	frrk8sv1 "github.com/metallb/frr-k8s/api/v1beta1"
	"github.com/openstack-k8s-operators/lib-common/modules/common/util"
)

// PodDetail -
type PodDetail struct {
	Name          string
	Namespace     string
	Node          string
	NetworkStatus []k8s_networkv1.NetworkStatus
}

// GetFRRPodPrefixes - returns the FRRConfiguration prefix entries for a pod
// secondary network interfaces in the format "10.10.10.10/32" for IPv4 or "::1/128" for IPv6
func GetFRRPodPrefixes(networkStatus []k8s_networkv1.NetworkStatus) []string {
	podPrefixes := []string{}
	for _, podNetStat := range networkStatus {
		if podNetStat.Name == "ovn-kubernetes" {
			continue
		}

		for _, ip := range podNetStat.IPs {
			subnet := "/32"
			if net.ParseIP(ip).To4() == nil {
				subnet = "/128"
			}
			ip = ip + subnet
			if !util.StringInSlice(ip, podPrefixes) {
				podPrefixes = append(podPrefixes, ip)
			}
		}
	}

	return podPrefixes
}

// GetFRRNeighbors - returs a list of  FRR Neighor for podPrefixes, using a copy of the
// nodeNeigbors and replacing its Prefixes with the podPrefixes.
func GetFRRNeighbors(nodeNeighbors []frrk8sv1.Neighbor, podPrefixes []string) []frrk8sv1.Neighbor {
	podNeighbors := []frrk8sv1.Neighbor{}

	for _, neighbor := range nodeNeighbors {
		neighbor.ToAdvertise.Allowed.Prefixes = podPrefixes
		podNeighbors = append(podNeighbors, neighbor)
	}

	return podNeighbors
}

// GetNodesRunningPods - get a uniq list of all nodes from all a PodDetail list
func GetNodesRunningPods(podNetworkDetailList []PodDetail) []string {
	nodes := []string{}
	for _, p := range podNetworkDetailList {
		if !util.StringInSlice(p.Node, nodes) {
			nodes = append(nodes, p.Node)
		}
	}

	return nodes
}

// OpenShiftFRRNamespace is the FRR namespace used in OpenShift 4.20+
const OpenShiftFRRNamespace = "openshift-frr-k8s"

// LegacyMetalLBNamespace is the FRR namespace used before OpenShift 4.20
const LegacyMetalLBNamespace = "metallb-system"

// GetFRRConfigurationNamespace checks whether migrationNamespace exists and returns it,
// otherwise falls back to defaultNamespace.
func GetFRRConfigurationNamespace(ctx context.Context, c client.Client, migrationNamespace, defaultNamespace string) (string, error) {
	ns := &corev1.Namespace{}
	err := c.Get(ctx, types.NamespacedName{Name: migrationNamespace}, ns)
	if err != nil {
		if k8s_errors.IsNotFound(err) {
			return defaultNamespace, nil
		}
		return "", err
	}

	return migrationNamespace, nil
}
