// Package bgp provides BGP configuration utilities for network management
package bgp

import (
	"net"

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
