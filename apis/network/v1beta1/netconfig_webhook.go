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
	"bytes"
	"fmt"
	"net"
	"regexp"
	"slices"
	"strconv"

	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/validation/field"
	k8snet "k8s.io/utils/net"
	ctrl "sigs.k8s.io/controller-runtime"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

// log is for logging in this package.
var netconfiglog = logf.Log.WithName("netconfig-resource")

// SetupWebhookWithManager -
func (r *NetConfig) SetupWebhookWithManager(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr).
		For(r).
		Complete()
}

//+kubebuilder:webhook:path=/mutate-network-openstack-org-v1beta1-netconfig,mutating=true,failurePolicy=fail,sideEffects=None,groups=network.openstack.org,resources=netconfigs,verbs=create;update,versions=v1beta1,name=mnetconfig.kb.io,admissionReviewVersions=v1

var _ webhook.Defaulter = &NetConfig{}

// Default implements webhook.Defaulter so a webhook will be registered for the type
func (r *NetConfig) Default() {
	for idx, net := range r.Spec.Networks {
		if net.ServiceNetwork == "" {
			r.Spec.Networks[idx].ServiceNetwork = ToDefaultServiceNetwork(net.Name)
		}
	}
}

//+kubebuilder:webhook:path=/validate-network-openstack-org-v1beta1-netconfig,mutating=false,failurePolicy=fail,sideEffects=None,groups=network.openstack.org,resources=netconfigs,verbs=create;update;delete,versions=v1beta1,name=vnetconfig.kb.io,admissionReviewVersions=v1

var _ webhook.Validator = &NetConfig{}

// ValidateCreate implements webhook.Validator so a webhook will be registered for the type
func (r *NetConfig) ValidateCreate() (admission.Warnings, error) {
	netconfiglog.Info("validate create", "name", r.Name)

	// check if there is already a NetConfig in the namespace.
	netcfg, err := getNetConfig(webhookClient, r)
	if err != nil {
		return nil, err
	}
	// stop if there is already a NetConfig in the namespace.
	if netcfg != nil {
		return nil, fmt.Errorf("there is already NetConfig %s in namespace %s. There can only be one.", netcfg.GetName(), r.GetNamespace())
	}

	allErrs := field.ErrorList{}
	basePath := field.NewPath("spec")

	// common network validation
	allErrs = append(allErrs, valiateNetworks(r.Spec.Networks, basePath)...)

	if len(allErrs) == 0 {
		return nil, nil
	}

	return nil, apierrors.NewInvalid(GroupVersion.WithKind("NetConfig").GroupKind(), r.Name, allErrs)
}

// ValidateUpdate implements webhook.Validator so a webhook will be registered for the type
func (r *NetConfig) ValidateUpdate(old runtime.Object) (admission.Warnings, error) {
	netconfiglog.Info("validate update", "name", r.Name)

	oldNetConfig, ok := old.(*NetConfig)
	if !ok || oldNetConfig == nil {
		return nil, apierrors.NewInternalError(fmt.Errorf("unable to convert existing object"))
	}

	allErrs := field.ErrorList{}
	basePath := field.NewPath("spec")

	// common network validation
	allErrs = append(allErrs, valiateNetworks(r.Spec.Networks, basePath)...)

	// validate against the previous object only _if_ there are IPSets in the namespace.
	// If there are none, the NetConfig could be updated to the needs without checking old <-> new.
	ipsets, err := getIPSets(webhookClient, r)
	if err != nil {
		return nil, err
	}
	if len(ipsets.Items) > 0 {
		allErrs = append(allErrs, valiateNetworksChanged(r.Spec.Networks, oldNetConfig.Spec.Networks, basePath)...)
	}

	if len(allErrs) == 0 {
		return nil, nil
	}

	return nil, apierrors.NewInvalid(GroupVersion.WithKind("NetConfig").GroupKind(), r.Name, allErrs)
}

// ValidateDelete implements webhook.Validator so a webhook will be registered for the type
func (r *NetConfig) ValidateDelete() (admission.Warnings, error) {
	netconfiglog.Info("validate delete", "name", r.Name)

	ipsets, err := getIPSets(webhookClient, r)
	if err != nil {
		return nil, err
	}
	if len(ipsets.Items) > 0 {
		return nil, apierrors.NewBadRequest(fmt.Sprintf("unable to delete NetConfig while there are still %d IPSets in the namespace", len(ipsets.Items)))
	}

	return nil, nil
}

// valiateNetworks
// - networks have uniq names
// - subnets within a network have uniq names
// - common subnet validation
// - validate uniq CIDRs on all subnets. While it would be possible to have same CIDR on different VLANs, we exlude this config
func valiateNetworks(
	networks []Network,
	path *field.Path,
) field.ErrorList {
	allErrs := field.ErrorList{}
	netNames := map[string]field.Path{}
	netCIDR := map[string]field.Path{}
	for netIdx, _net := range networks {
		path := path.Child("networks").Index(netIdx)

		// validate uniqe network names
		allErrs = append(allErrs, valiateUniqElement(netNames, string(_net.Name), path, "name", errDupeNetworkName)...)

		// validate DNSDomain format is correct
		if !validateDNSDomain(_net.DNSDomain) {
			path := path.Child("dnsDomain")
			allErrs = append(allErrs, field.Invalid(path.Child("dnsDomain"), _net.DNSDomain, fmt.Sprintf(errInvalidDNSDomain, _net.DNSDomain)))
		}
		// validate DNSDomain is uniq across networks
		allErrs = append(allErrs, valiateUniqElement(netNames, string(_net.DNSDomain), path, "dnsDomain", errDupeDNSDomain)...)

		path = path.Child("subnets")
		subnetNames := map[string]field.Path{}
		for subnetIdx, _subnet := range _net.Subnets {
			path := path.Index(subnetIdx)

			// subnet validation
			if err := valiateSubnet(_subnet, subnetNames, netCIDR, path); err != nil {
				allErrs = append(allErrs, err...)
			}
		}
	}
	return allErrs
}

// valiateNetworksChanged
// - validate if a network was removed
// - subnet is still there
// - cidr changed
// - Vlan changed
// - gateway changed
func valiateNetworksChanged(
	networks []Network,
	oldNetworks []Network,
	path *field.Path,
) field.ErrorList {
	allErrs := field.ErrorList{}

	for oldNetIdx, _net := range oldNetworks {
		path := path.Child("networks").Index(oldNetIdx)

		// validate if networks are still there
		f := func(n Network) bool {
			return equality.Semantic.DeepEqual(n.Name, _net.Name)
		}
		netIdx := slices.IndexFunc(networks, f)
		if netIdx < 0 {
			// the network was removed
			allErrs = append(allErrs, field.Invalid(path.Child("name"), _net.Name, fmt.Sprintf(errNetworkChanged, _net.Name)))
			return allErrs
		}

		path = path.Child("subnets")
		for oldSubnetIdx, _subnet := range _net.Subnets {
			path := path.Index(oldSubnetIdx)

			// validate if subnet is still there
			f := func(n Subnet) bool {
				return equality.Semantic.DeepEqual(n.Name, _subnet.Name)
			}
			subnetIdx := slices.IndexFunc(networks[netIdx].Subnets, f)
			if subnetIdx < 0 {
				allErrs = append(allErrs, field.Invalid(path.Child("name"), _net.Name, fmt.Sprintf(errSubnetParameterChanged, "name", _subnet.Name)))
				return allErrs
			}

			// validate if cidr changed
			if !equality.Semantic.DeepEqual(_subnet.Cidr, networks[netIdx].Subnets[subnetIdx].Cidr) {
				allErrs = append(allErrs, field.Invalid(path.Child("cidr"), _net.Name, fmt.Sprintf(errSubnetParameterChanged, "cidr", _subnet.Cidr)))
			}

			// validate if Vlan changed
			if !equality.Semantic.DeepEqual(_subnet.Vlan, networks[netIdx].Subnets[subnetIdx].Vlan) {
				vlan := ""
				if _subnet.Vlan != nil {
					vlan = strconv.Itoa(*_subnet.Vlan)
				}
				allErrs = append(allErrs, field.Invalid(path.Child("vlan"), _net.Name, fmt.Sprintf(errSubnetParameterChanged, "vlan", vlan)))
			}

			// validate if gateway changed
			if !equality.Semantic.DeepEqual(_subnet.Gateway, networks[netIdx].Subnets[subnetIdx].Gateway) {
				gateway := ""
				if _subnet.Gateway != nil {
					gateway = *_subnet.Gateway
				}
				allErrs = append(allErrs, field.Invalid(path.Child("vlan"), _net.Name, fmt.Sprintf(errSubnetParameterChanged, "vlan", gateway)))
			}

		}
	}

	return allErrs
}

func valiateUniqElement(
	elements map[string]field.Path,
	name string,
	path *field.Path,
	childName string,
	errTemplate string,
) field.ErrorList {
	allErrs := field.ErrorList{}

	if existPath, ok := elements[name]; !ok {
		elements[name] = *path.Child(childName)
	} else {
		allErrs = append(allErrs, field.Invalid(path.Child(childName), name, fmt.Sprintf(errTemplate, name, existPath.String())))
	}

	return allErrs
}

// CIDRs must be uniq, while its possible to have same CIDR on different VLANs, we exlude this config
//
//lint:ignore U1000 valiateUniqCIDR
func valiateUniqCIDR(
	netCIDRs map[int]map[string]field.Path,
	vlan *int,
	cidr string,
	path *field.Path,
) field.ErrorList {
	allErrs := field.ErrorList{}

	// use 0 for non vlan id configured for a subnet
	vlanID := 0
	if vlan != nil {
		vlanID = *vlan
	}

	if netCIDRs[vlanID] == nil {
		netCIDRs[vlanID] = map[string]field.Path{}
	}

	vlanCIRDs := netCIDRs[vlanID]

	if existPath, ok := vlanCIRDs[cidr]; !ok {
		vlanCIRDs[cidr] = *path.Child("cidr")
	} else {
		allErrs = append(allErrs, field.Invalid(path.Child("cidr"), cidr, fmt.Sprintf(errDupeCIDR, cidr, existPath.String())))
	}

	return allErrs
}

// valiateSubnet
// - CIDR is correct
// - gateway is correct
// - common subnet validation
func valiateSubnet(
	subnet Subnet,
	subnetNames map[string]field.Path,
	netCIDR map[string]field.Path,
	path *field.Path,
) field.ErrorList {
	allErrs := field.ErrorList{}

	cidr := subnet.Cidr
	gateway := subnet.Gateway
	dnsDomain := subnet.DNSDomain

	// validate uniqe subnet names
	allErrs = append(allErrs, valiateUniqElement(subnetNames, string(subnet.Name), path, "name", errDupeNetworkName)...)

	// validate CIDR
	// validate uniq CIDRs on all subnets. While it would be possible to have same CIDR on different VLANs, we exlude this config
	allErrs = append(allErrs, valiateUniqElement(netCIDR, subnet.Cidr, path, "name", errDupeCIDR)...)

	_, ipPrefix, ipPrefixErr := net.ParseCIDR(cidr)
	if ipPrefixErr != nil {
		allErrs = append(allErrs, field.Invalid(path.Child("cidr"), cidr, errInvalidCidr))
		return allErrs
	}

	// validate gateway
	if gateway != nil {
		path := path.Child("gateway")
		if err := valiateAddress(*gateway, ipPrefix, path); err != nil {
			allErrs = append(allErrs, err...)
		}
	}

	// validate allocationRanges
	for idx, allocRange := range subnet.AllocationRanges {
		path := path.Child("allocationRanges").Index(idx)

		if err := valiateAllocationRange(allocRange, ipPrefix, path); err != nil {
			allErrs = append(allErrs, err...)
		}
	}

	// validate excludeAddresses
	for idx, exclAddress := range subnet.ExcludeAddresses {
		path := path.Child("excludeAddresses").Index(idx)

		if err := valiateAddress(exclAddress, ipPrefix, path); err != nil {
			allErrs = append(allErrs, err...)
		}
	}

	// validate routes
	for idx, route := range subnet.Routes {
		path := path.Child("routes").Index(idx)

		// validate destination
		_, _, ipPrefixErr := net.ParseCIDR(route.Destination)
		if ipPrefixErr != nil {
			allErrs = append(allErrs, field.Invalid(path.Child("destination"), route.Destination, errInvalidCidr))
			return allErrs
		}

		// validate nexthop
		pathNexthop := path.Child("nexthop")
		if err := valiateAddress(route.Nexthop, ipPrefix, pathNexthop); err != nil {
			allErrs = append(allErrs, err...)
		}
	}
	// validate DNSDomain
	if dnsDomain != nil {
		if !validateDNSDomain(*dnsDomain) {
			path := path.Child("dnsDomain")
			allErrs = append(allErrs, field.Invalid(path.Child("dnsDomain"), *dnsDomain, fmt.Sprintf(errInvalidDNSDomain, *dnsDomain)))
		}
		// validate DNSDomain is uniq across networks
		allErrs = append(allErrs, valiateUniqElement(subnetNames, string(*dnsDomain), path, "dnsDomain", errDupeDNSDomain)...)
	}

	return allErrs
}

func validateDNSDomain(dnsName string) bool {
	pattern := `^([a-zA-Z0-9]([a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?\.)+[a-zA-Z]{2,}$`
	regex := regexp.MustCompile(pattern)
	return regex.MatchString(dnsName)
}

// valiateAddress
// - address is correct
// - has the same IP version of the subnet cidr
// - matches the CIDR of the subnet
func valiateAddress(
	addrStr string,
	ipPrefix *net.IPNet,
	path *field.Path,
) field.ErrorList {
	allErrs := field.ErrorList{}

	addr := net.ParseIP(addrStr)
	if addr == nil {
		allErrs = append(allErrs, field.Invalid(path, addrStr, errNotIPAddr))
		return allErrs
	}

	// Validate IP Family for IPv4
	if k8snet.IsIPv4CIDR(ipPrefix) {
		if !k8snet.IsIPv4(addr) {
			allErrs = append(allErrs, field.Invalid(path, addrStr, errMixedAddressFamily))
		}
	}

	// Validate IP Family for IPv6
	if k8snet.IsIPv6CIDR(ipPrefix) {
		if !k8snet.IsIPv6(addr) {
			allErrs = append(allErrs, field.Invalid(path, addrStr, errMixedAddressFamily))
		}
	}

	// Validate addr in cidr
	if !ipPrefix.Contains(addr) {
		allErrs = append(allErrs, field.Invalid(path, addrStr, fmt.Sprintf(errNotInCidr, ipPrefix.String())))
	}

	return allErrs
}

// valiateAllocationRange
// - start/end address is correct
// - start/end address have the same IP version of the subnet cidr
// - start/end address matches the CIDR of the subnet
// - start address is < end address
func valiateAllocationRange(
	allocRange AllocationRange,
	ipPrefix *net.IPNet,
	path *field.Path,
) field.ErrorList {
	allErrs := field.ErrorList{}

	startAddr := net.ParseIP(allocRange.Start)
	if startAddr == nil {
		allErrs = append(allErrs, field.Invalid(path.Child("start"), allocRange.Start, errNotIPAddr))
	}
	endAddr := net.ParseIP(allocRange.End)
	if endAddr == nil {
		allErrs = append(allErrs, field.Invalid(path.Child("end"), allocRange.End, errNotIPAddr))
	}

	if len(allErrs) != 0 {
		return allErrs
	}

	// Validate IP Family for IPv4
	if k8snet.IsIPv4CIDR(ipPrefix) {
		if !(k8snet.IsIPv4(startAddr) && k8snet.IsIPv4(endAddr)) {
			allErrs = append(allErrs, field.Invalid(path, allocRange, errMixedAddressFamily))
		}
	}

	// Validate IP Family for IPv6
	if k8snet.IsIPv6CIDR(ipPrefix) {
		if !(k8snet.IsIPv6(startAddr) && k8snet.IsIPv6(endAddr)) {
			allErrs = append(allErrs, field.Invalid(path, allocRange, errMixedAddressFamily))
		}
	}

	// Validate start and end in cidr
	if !ipPrefix.Contains(startAddr) {
		allErrs = append(allErrs, field.Invalid(path.Child("start"), allocRange.Start, fmt.Sprintf(errNotInCidr, ipPrefix.String())))
	}
	if !ipPrefix.Contains(endAddr) {
		allErrs = append(allErrs, field.Invalid(path.Child("end"), allocRange.End, fmt.Sprintf(errNotInCidr, ipPrefix.String())))
	}

	// Start address should be < End address
	// The result will be 0 if a == b, -1 if a < b, and +1 if a > b.
	if bytes.Compare(endAddr, startAddr) != 1 {
		allErrs = append(allErrs, field.Invalid(path.Child("start"), allocRange.Start, fmt.Sprintf(errInvalidRange, allocRange.Start, allocRange.End)))
		allErrs = append(allErrs, field.Invalid(path.Child("end"), allocRange.End, fmt.Sprintf(errInvalidRange, allocRange.Start, allocRange.End)))
	}

	return allErrs
}
