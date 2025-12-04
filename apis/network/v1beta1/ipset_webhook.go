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
	"fmt"
	"net"
	"slices"
	"strings"

	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/validation/field"
	k8snet "k8s.io/utils/net"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

// log is for logging in this package.
var ipsetlog = logf.Log.WithName("ipset-resource")

var _ webhook.Defaulter = &IPSet{}

// Default implements webhook.Defaulter so a webhook will be registered for the type
func (r *IPSet) Default() {
	ipsetlog.Info("default", "name", r.Name)

	// TODO(user): fill in your defaulting logic.
}

var _ webhook.Validator = &IPSet{}

// ValidateCreate implements webhook.Validator so a webhook will be registered for the type
func (r *IPSet) ValidateCreate() (admission.Warnings, error) {
	ipsetlog.Info("validate create", "name", r.Name)

	// check if there is already a NetConfig in the namespace.
	netcfg, err := getNetConfig(webhookClient, r)
	if err != nil {
		return nil, err
	}
	// stop if there is no NetConfig in the namespace.
	if netcfg == nil {
		return nil, fmt.Errorf("no NetConfig found in namespace %s, please create one", r.GetNamespace())
	}

	allErrs := field.ErrorList{}
	basePath := field.NewPath("spec")

	// validate requested networks exist in netcfg
	allErrs = append(allErrs, validateIPSetNetwork(r.Spec.Networks, basePath, &netcfg.Spec)...)

	if len(allErrs) == 0 {
		return nil, nil
	}

	return nil, apierrors.NewInvalid(GroupVersion.WithKind("IPSet").GroupKind(), r.Name, allErrs)
}

// ValidateUpdate implements webhook.Validator so a webhook will be registered for the type
func (r *IPSet) ValidateUpdate(old runtime.Object) (admission.Warnings, error) {
	ipsetlog.Info("validate update", "name", r.Name)

	oldIPSet, ok := old.(*IPSet)
	if !ok || oldIPSet == nil {
		return nil, apierrors.NewInternalError(fmt.Errorf("unable to convert existing object"))
	}

	// Do not allow to update of Spec.Networks if the Immuatable was set on the existing object
	if oldIPSet.Spec.Immutable && !equality.Semantic.DeepEqual(oldIPSet.Spec.Networks, r.Spec.Networks) {
		return nil, apierrors.NewForbidden(
			schema.GroupResource{
				Group:    GroupVersion.WithKind("IPSet").Group,
				Resource: GroupVersion.WithKind("IPSet").Kind,
			}, r.GetName(), &field.Error{
				Type:     field.ErrorTypeForbidden,
				Field:    "*",
				BadValue: r.Name,
				Detail:   "Invalid value: \"object\": Spec.Networks is immutable",
			},
		)
	}

	allErrs := field.ErrorList{}
	// only run the validations on update if the object won't get updated
	// to be deleted (remove finalizer).
	if r.DeletionTimestamp.IsZero() {
		// check if there is already a NetConfig in the namespace.
		netcfg, err := getNetConfig(webhookClient, r)
		if err != nil {
			return nil, err
		}
		// stop if there is no NetConfig in the namespace.
		if netcfg == nil {
			return nil, fmt.Errorf("no NetConfig found in namespace %s, please create one", r.GetNamespace())
		}

		basePath := field.NewPath("spec")

		// validate requested networks exist in
		allErrs = append(allErrs, validateIPSetNetwork(r.Spec.Networks, basePath, &netcfg.Spec)...)

		// validate against the previous object only
		allErrs = append(allErrs, validateIPSetChanged(r.Spec.Networks, oldIPSet.Spec.Networks, basePath)...)
	}

	if len(allErrs) == 0 {
		return nil, nil
	}

	return nil, apierrors.NewInvalid(GroupVersion.WithKind("IPSet").GroupKind(), r.Name, allErrs)
}

// ValidateDelete implements webhook.Validator so a webhook will be registered for the type
func (r *IPSet) ValidateDelete() (admission.Warnings, error) {
	ipsetlog.Info("validate delete", "name", r.Name)

	// TODO(user): fill in your validation logic upon object deletion.
	return nil, nil
}

// validateIPSetNetwork
// - networks are uniq in the list
// - networks and subnets exist in netcfg
// - FixedIP is a valid IP address
// - FixedIP has correct IP version of subnet
// - FixedIP is in the subnet cidr
// - Route is only specified on a single network per IPFamily
func validateIPSetNetwork(
	networks []IPSetNetwork,
	path *field.Path,
	netCfgSpec *NetConfigSpec,
) field.ErrorList {
	allErrs := field.ErrorList{}
	netNames := map[string]field.Path{}
	defaultRouteCount := map[k8snet.IPFamily]int{
		k8snet.IPv4:            0,
		k8snet.IPv6:            0,
		k8snet.IPFamilyUnknown: 0, // should not be required since the cidr gets validated on the NetConfig
	}

	for _netIdx, _net := range networks {
		path := path.Child("networks").Index(_netIdx)

		// validate uniqe networks in request
		allErrs = append(allErrs, valiateUniqElement(netNames, string(_net.Name), path, "name", errDupeNetworkName)...)

		// validate if requested network is in NetConfig
		f := func(n Network) bool {
			return strings.EqualFold(string(n.Name), string(_net.Name))
		}
		netIdx := slices.IndexFunc(netCfgSpec.Networks, f)

		if netIdx >= 0 {
			// validate if requested subnet is in NetConfig
			f := func(n Subnet) bool {
				return strings.EqualFold(string(n.Name), string(_net.SubnetName))
			}
			subNetIdx := slices.IndexFunc(netCfgSpec.Networks[netIdx].Subnets, f)

			if subNetIdx >= 0 {
				// net and subnet are valid
				subNetCfg := netCfgSpec.Networks[netIdx].Subnets[subNetIdx]
				if _net.FixedIP != nil || _net.DefaultRoute != nil {
					cidr := subNetCfg.Cidr
					_, ipPrefix, ipPrefixErr := net.ParseCIDR(cidr)
					if ipPrefixErr != nil {
						// this should never happen as the subnet CIDR was already validated
						// via the netcfg webhook
						allErrs = append(allErrs, field.Invalid(path.Child("cidr"), cidr, errInvalidCidr))
						return allErrs
					}
					ipFam := k8snet.IPFamilyOfCIDR(ipPrefix)

					// validate the requested FixedIP
					if _net.FixedIP != nil {
						path := path.Child("fixedIP")
						if err := valiateAddress(*_net.FixedIP, ipPrefix, path); err != nil {
							allErrs = append(allErrs, err...)
						}
					}

					// check that there are not multiple have the defaultRoute flag
					if _net.DefaultRoute != nil && *_net.DefaultRoute {
						defaultRouteCount[ipFam]++

						for fam, count := range defaultRouteCount {
							if count > 1 {
								allErrs = append(allErrs, field.Invalid(path.Child("defaultRoute"), _net.Name, fmt.Sprintf(errMultiDefaultRoute, string(fam))))
							}
						}

						// validate that default GW is configured on subnet when requested
						if subNetCfg.Gateway == nil || (subNetCfg.Gateway != nil && *subNetCfg.Gateway == "") {
							allErrs = append(allErrs, field.Invalid(path.Child("defaultRoute"), _net.Name, fmt.Sprintf(errNoDefaultRoute, subNetCfg.Name)))
						}
					}
				}

				continue
			}

			// the requested subnet was not found in the network
			allErrs = append(allErrs, field.Invalid(path.Child("subnetName"), _net.SubnetName, fmt.Sprintf(errSubnetNotInNetwork, _net.SubnetName, _net.Name)))
			continue
		}

		// the requested net was not found in the NetConfig
		allErrs = append(allErrs, field.Invalid(path.Child("name"), _net.Name, fmt.Sprintf(errNetworkNotFound, _net.Name)))
	}

	return allErrs
}

// validateIPSetChanged
// - if a previous requested network is still in the list
// - if subnet changed within a network
// - if fixedIP changed
// - if defaultRoute changed
func validateIPSetChanged(
	networks []IPSetNetwork,
	oldNetworks []IPSetNetwork,
	path *field.Path,
) field.ErrorList {
	allErrs := field.ErrorList{}

	for _netIdx, _net := range oldNetworks {
		path := path.Child("networks").Index(_netIdx)

		// validate if a previous requested network is still in the CR
		f := func(n IPSetNetwork) bool {
			return equality.Semantic.DeepEqual(n.Name, _net.Name)
		}
		netIdx := slices.IndexFunc(networks, f)
		if netIdx < 0 {
			// the network was removed
			allErrs = append(allErrs, field.Invalid(path.Child("name"), _net.Name, fmt.Sprintf(errNetworkChanged, _net.Name)))
			return allErrs
		}

		// validate if subnet changed within a network
		if !equality.Semantic.DeepEqual(_net.SubnetName, networks[netIdx].SubnetName) {
			allErrs = append(allErrs, field.Invalid(path.Child("subnetName"), _net.Name, fmt.Sprintf(errSubnetChanged, oldNetworks[_netIdx].SubnetName, networks[_netIdx].SubnetName)))
		}
		// validate if fixedIP changed
		if !equality.Semantic.DeepEqual(_net.FixedIP, networks[netIdx].FixedIP) {
			allErrs = append(allErrs, field.Invalid(path.Child("fixedIP"), _net.Name, errFixedIPChanged))
		}
		// validate if defaultRoute changed
		if !equality.Semantic.DeepEqual(_net.DefaultRoute, networks[netIdx].DefaultRoute) {
			allErrs = append(allErrs, field.Invalid(path.Child("defaultRoute"), _net.Name, errDefaultRouteChanged))
		}
	}

	return allErrs
}
