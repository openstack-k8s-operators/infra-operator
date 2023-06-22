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

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/validation/field"
	k8snet "k8s.io/utils/net"
	ctrl "sigs.k8s.io/controller-runtime"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
)

// log is for logging in this package.
var netconfiglog = logf.Log.WithName("netconfig-resource")

const (
	errNotIPAddr          = "not an IP address"
	errInvalidCidr        = "IP address prefix (CIDR) %s"
	errNotInCidr          = "address not in IP address prefix (CIDR) %s"
	errMixedAddressFamily = "cannot mix IPv4 and IPv6"
	errInvalidRange       = "Start address: %s > End address %s"
	errDupeNetworkName    = "network name %s already in use at %s, must be uniq"
	errDupeCIDR           = "CIDR %s already in use at %s"
	errInvalidDNSDomain   = "DNSDoman name %s is not valid"
	errDupeDNSDomain      = "DNSDoman name %s already in use at %s, must be uniq"
)

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
	netconfiglog.Info("default", "name", r.Name)

	// TODO(user): fill in your defaulting logic.
}

//+kubebuilder:webhook:path=/validate-network-openstack-org-v1beta1-netconfig,mutating=false,failurePolicy=fail,sideEffects=None,groups=network.openstack.org,resources=netconfigs,verbs=create;update,versions=v1beta1,name=vnetconfig.kb.io,admissionReviewVersions=v1

var _ webhook.Validator = &NetConfig{}

// ValidateCreate implements webhook.Validator so a webhook will be registered for the type
func (r *NetConfig) ValidateCreate() error {
	netconfiglog.Info("validate create", "name", r.Name)

	allErrs := field.ErrorList{}
	basePath := field.NewPath("spec")

	// common network validation
	allErrs = append(allErrs, valiateNetworks(r.Spec.Networks, basePath)...)

	if len(allErrs) == 0 {
		return nil
	}

	return apierrors.NewInvalid(GroupVersion.WithKind("NetConfig").GroupKind(), r.Name, allErrs)
}

// ValidateUpdate implements webhook.Validator so a webhook will be registered for the type
func (r *NetConfig) ValidateUpdate(old runtime.Object) error {
	netconfiglog.Info("validate update", "name", r.Name)

	allErrs := field.ErrorList{}
	basePath := field.NewPath("spec")

	// common network validation
	allErrs = append(allErrs, valiateNetworks(r.Spec.Networks, basePath)...)

	if len(allErrs) == 0 {
		return nil
	}

	// TODO (mschuppert): update validation for content which must not change

	return apierrors.NewInvalid(GroupVersion.WithKind("NetConfig").GroupKind(), r.Name, allErrs)
}

// ValidateDelete implements webhook.Validator so a webhook will be registered for the type
func (r *NetConfig) ValidateDelete() error {
	netconfiglog.Info("validate delete", "name", r.Name)

	// TODO (mschuppert): delete validation, no ipset and reservations should exist.
	return nil
}

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
		if addr != nil && !k8snet.IsIPv4(addr) {
			allErrs = append(allErrs, field.Invalid(path, addrStr, errMixedAddressFamily))
		}
	}

	// Validate IP Family for IPv6
	if k8snet.IsIPv6CIDR(ipPrefix) {
		if addr != nil && !k8snet.IsIPv6(addr) {
			allErrs = append(allErrs, field.Invalid(path, addrStr, errMixedAddressFamily))
		}
	}

	// Validate addr in cidr
	if addr != nil && !ipPrefix.Contains(addr) {
		allErrs = append(allErrs, field.Invalid(path, addrStr, fmt.Sprintf(errNotInCidr, ipPrefix.String())))
	}

	return allErrs
}

func valiateAllocationRange(
	allocRange AllocationRange,
	ipPrefix *net.IPNet,
	path *field.Path,
) field.ErrorList {
	allErrs := field.ErrorList{}

	startAddr := net.ParseIP(allocRange.Start)
	if startAddr == nil {
		allErrs = append(allErrs, field.Invalid(path.Child("start"), allocRange.Start, errNotIPAddr))
		return allErrs
	}
	endAddr := net.ParseIP(allocRange.End)
	if endAddr == nil {
		allErrs = append(allErrs, field.Invalid(path.Child("end"), allocRange.End, errNotIPAddr))
		return allErrs
	}

	if startAddr == nil || endAddr == nil {
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
