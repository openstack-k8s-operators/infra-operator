package bgp

import (
	"testing"

	. "github.com/onsi/gomega" //revive:disable:dot-imports

	k8s_networkv1 "github.com/k8snetworkplumbingwg/network-attachment-definition-client/pkg/apis/k8s.cni.cncf.io/v1"
	frrk8sv1 "github.com/metallb/frr-k8s/api/v1beta1"
)

func TestGetNodesRunningPods(t *testing.T) {

	tests := []struct {
		name       string
		podDatails []PodDetail
		want       []string
	}{
		{
			name:       "No network",
			podDatails: []PodDetail{},
			want:       []string{},
		},
		{
			name: "single pod",
			podDatails: []PodDetail{
				{
					Name:          "foo",
					Namespace:     "bar",
					Node:          "node1",
					NetworkStatus: []k8s_networkv1.NetworkStatus{},
				},
			},
			want: []string{"node1"},
		},
		{
			name: "multiple pods different nodes",
			podDatails: []PodDetail{
				{
					Name:          "foo1",
					Namespace:     "bar",
					Node:          "node1",
					NetworkStatus: []k8s_networkv1.NetworkStatus{},
				},
				{
					Name:          "foo2",
					Namespace:     "bar",
					Node:          "node2",
					NetworkStatus: []k8s_networkv1.NetworkStatus{},
				},
			},
			want: []string{"node1", "node2"},
		},
		{
			name: "multiple pods same node",
			podDatails: []PodDetail{
				{
					Name:          "foo1",
					Namespace:     "bar",
					Node:          "node1",
					NetworkStatus: []k8s_networkv1.NetworkStatus{},
				},
				{
					Name:          "foo2",
					Namespace:     "bar",
					Node:          "node1",
					NetworkStatus: []k8s_networkv1.NetworkStatus{},
				},
			},
			want: []string{"node1"},
		},
		{
			name: "multiple pods mix of node assignment",
			podDatails: []PodDetail{
				{
					Name:          "foo1",
					Namespace:     "bar",
					Node:          "node1",
					NetworkStatus: []k8s_networkv1.NetworkStatus{},
				},
				{
					Name:          "foo2",
					Namespace:     "bar",
					Node:          "node2",
					NetworkStatus: []k8s_networkv1.NetworkStatus{},
				},
				{
					Name:          "foo3",
					Namespace:     "bar",
					Node:          "node2",
					NetworkStatus: []k8s_networkv1.NetworkStatus{},
				},
				{
					Name:          "foo4",
					Namespace:     "bar",
					Node:          "node3",
					NetworkStatus: []k8s_networkv1.NetworkStatus{},
				},
			},
			want: []string{"node1", "node2", "node3"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := NewWithT(t)

			nodes := GetNodesRunningPods(tt.podDatails)
			g.Expect(nodes).To(HaveLen(len(tt.want)))
			g.Expect(nodes).To(BeEquivalentTo(tt.want))
		})
	}
}

func TestGetFRRPodPrefixes(t *testing.T) {

	tests := []struct {
		name      string
		netStatus []k8s_networkv1.NetworkStatus
		want      []string
	}{
		{
			name:      "No status",
			netStatus: []k8s_networkv1.NetworkStatus{},
			want:      []string{},
		},
		{
			name: "only pod network",
			netStatus: []k8s_networkv1.NetworkStatus{
				{
					Name:      "ovn-kubernetes",
					Interface: "eth0",
					IPs:       []string{"192.168.56.59"},
					Mac:       "0a:58:c0:a8:38:3b",
					Default:   true,
				},
			},
			want: []string{},
		},
		{
			name: "pod and additional network",
			netStatus: []k8s_networkv1.NetworkStatus{
				{
					Name:      "ovn-kubernetes",
					Interface: "eth0",
					IPs:       []string{"192.168.56.59"},
					Mac:       "0a:58:c0:a8:38:3b",
					Default:   true,
				},
				{
					Name:      "foo/bar",
					Interface: "bar",
					IPs:       []string{"172.17.0.40"},
					Mac:       "de:39:07:a1:b5:6b",
					Default:   false,
				},
			},
			want: []string{"172.17.0.40/32"},
		},
		{
			name: "pod and multiple additional networks",
			netStatus: []k8s_networkv1.NetworkStatus{
				{
					Name:      "ovn-kubernetes",
					Interface: "eth0",
					IPs:       []string{"192.168.56.59"},
					Mac:       "0a:58:c0:a8:38:3b",
					Default:   true,
				},
				{
					Name:      "foo/bar",
					Interface: "bar",
					IPs:       []string{"172.17.0.40"},
					Mac:       "de:39:07:a1:b5:6b",
					Default:   false,
				},
				{
					Name:      "foo/dog",
					Interface: "dog",
					IPs:       []string{"172.18.0.40"},
					Mac:       "de:39:07:a1:b5:6c",
					Default:   false,
				},
			},
			want: []string{"172.17.0.40/32", "172.18.0.40/32"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := NewWithT(t)

			nodes := GetFRRPodPrefixes(tt.netStatus)
			g.Expect(nodes).To(HaveLen(len(tt.want)))
			g.Expect(nodes).To(BeEquivalentTo(tt.want))
		})
	}
}

func TestGetFRRNeighbors(t *testing.T) {

	tests := []struct {
		name          string
		nodeNeighbors []frrk8sv1.Neighbor
		podPrefixes   []string
		want          []frrk8sv1.Neighbor
	}{
		{
			name:          "No nodeNeighbors, no podPrefixes",
			nodeNeighbors: []frrk8sv1.Neighbor{},
			podPrefixes:   []string{},
			want:          []frrk8sv1.Neighbor{},
		},
		{
			name: "single nodeNeighbor",
			nodeNeighbors: []frrk8sv1.Neighbor{
				{
					Address: "10.10.10.10",
					ASN:     64999,
					ToAdvertise: frrk8sv1.Advertise{
						Allowed: frrk8sv1.AllowedOutPrefixes{
							Mode: frrk8sv1.AllowRestricted,
							Prefixes: []string{
								"10.10.10.11/32",
								"10.10.10.12/32",
							},
						},
					},
				},
			},
			podPrefixes: []string{"172.17.0.40/32", "172.18.0.40/32"},
			want: []frrk8sv1.Neighbor{
				{
					Address: "10.10.10.10",
					ASN:     64999,
					ToAdvertise: frrk8sv1.Advertise{
						Allowed: frrk8sv1.AllowedOutPrefixes{
							Mode: frrk8sv1.AllowRestricted,
							Prefixes: []string{
								"172.17.0.40/32",
								"172.18.0.40/32",
							},
						},
					},
				},
			},
		},
		{
			name: "multiple nodeNeighbors",
			nodeNeighbors: []frrk8sv1.Neighbor{
				{
					Address: "10.10.10.10",
					ASN:     64999,
					ToAdvertise: frrk8sv1.Advertise{
						Allowed: frrk8sv1.AllowedOutPrefixes{
							Mode: frrk8sv1.AllowRestricted,
							Prefixes: []string{
								"10.10.10.11/32",
								"10.10.10.12/32",
							},
						},
					},
				},
				{
					Address: "10.10.11.10",
					ASN:     64999,
					ToAdvertise: frrk8sv1.Advertise{
						Allowed: frrk8sv1.AllowedOutPrefixes{
							Mode: frrk8sv1.AllowRestricted,
							Prefixes: []string{
								"10.10.10.11/32",
								"10.10.10.12/32",
							},
						},
					},
				},
			},
			podPrefixes: []string{"172.17.0.40/32", "172.18.0.40/32"},
			want: []frrk8sv1.Neighbor{
				{
					Address: "10.10.10.10",
					ASN:     64999,
					ToAdvertise: frrk8sv1.Advertise{
						Allowed: frrk8sv1.AllowedOutPrefixes{
							Mode: frrk8sv1.AllowRestricted,
							Prefixes: []string{
								"172.17.0.40/32",
								"172.18.0.40/32",
							},
						},
					},
				},
				{
					Address: "10.10.11.10",
					ASN:     64999,
					ToAdvertise: frrk8sv1.Advertise{
						Allowed: frrk8sv1.AllowedOutPrefixes{
							Mode: frrk8sv1.AllowRestricted,
							Prefixes: []string{
								"172.17.0.40/32",
								"172.18.0.40/32",
							},
						},
					},
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := NewWithT(t)

			nodes := GetFRRNeighbors(tt.nodeNeighbors, tt.podPrefixes)
			g.Expect(nodes).To(HaveLen(len(tt.want)))
			g.Expect(nodes).To(BeEquivalentTo(tt.want))
		})
	}
}
