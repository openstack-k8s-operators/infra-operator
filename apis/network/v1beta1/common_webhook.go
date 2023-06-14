package v1beta1

// Because webhooks *MUST* exist within the api/<version> package, we need to place common
// functions here that might be used across different Kinds' webhooks

import (
	"context"
	"fmt"

	"sigs.k8s.io/controller-runtime/pkg/client"
	goClient "sigs.k8s.io/controller-runtime/pkg/client"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Client needed for API calls (manager's client, set by first SetupWebhookWithManager() call
// to any particular webhook)
var webhookClient goClient.Client

const (
	errNotIPAddr              = "not an IP address"
	errInvalidCidr            = "IP address prefix (CIDR) %s"
	errNotInCidr              = "address not in IP address prefix (CIDR) %s"
	errMixedAddressFamily     = "cannot mix IPv4 and IPv6"
	errInvalidRange           = "Start address: %s > End address %s"
	errDupeNetworkName        = "network name %s already in use at %s, must be uniq"
	errDupeCIDR               = "CIDR %s already in use at %s"
	errInvalidDNSDomain       = "DNSDoman name %s is not valid"
	errDupeDNSDomain          = "DNSDoman name %s already in use at %s, must be uniq"
	errNetworkNotFound        = "network %s not in NetConfig"
	errSubnetNotInNetwork     = "subnet %s not in network %s of the NetConfig"
	errNetworkChanged         = "network %s removed"
	errSubnetChanged          = "subnet within a network must not change (%s/%s)"
	errSubnetParameterChanged = "%s of a subnet must not change - %v"
	errFixedIPChanged         = "fixedIP must not change"
	errDefaultRouteChanged    = "defaultRoute must not change"
	errMultiDefaultRoute      = "%s defaultRoute can only be requested on a singe network"
)

func getNetConfig(
	c client.Client,
	obj metav1.Object,
) (*NetConfig, error) {
	// check if NetConfig is available
	opts := &client.ListOptions{
		Namespace: obj.GetNamespace(),
	}

	netcfgs := &NetConfigList{}
	err := webhookClient.List(context.TODO(), netcfgs, opts)
	if err != nil {
		return nil, err
	}

	if len(netcfgs.Items) == 0 {
		return nil, nil
	} else if len(netcfgs.Items) > 1 {
		return nil, fmt.Errorf("more then one NetConfig found in namespace %s, there must be only one", obj.GetNamespace())
	}

	return &netcfgs.Items[0], nil
}

func getIPSets(
	c client.Client,
	obj metav1.Object,
) (*IPSetList, error) {
	// check if IPSet is available
	opts := &client.ListOptions{
		Namespace: obj.GetNamespace(),
	}

	ipsets := &IPSetList{}
	err := webhookClient.List(context.TODO(), ipsets, opts)
	if err != nil {
		return nil, err
	}

	return ipsets, nil
}
