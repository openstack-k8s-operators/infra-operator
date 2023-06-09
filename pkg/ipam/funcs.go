package ipam

import (
	"fmt"
	"net"
	"net/netip"

	networkv1 "github.com/openstack-k8s-operators/infra-operator/apis/network/v1beta1"
	"golang.org/x/exp/slices"
)

// AssignIPDetails -
type AssignIPDetails struct {
	IPSet       string
	NetName     string
	SubNet      *networkv1.Subnet
	Reservelist *networkv1.ReservationList
	FixedIP     net.IP
}

// AssignIP assigns an IP using a range and a reserve list.
func (a *AssignIPDetails) AssignIP() (*networkv1.IPAddress, error) {
	if a.FixedIP != nil {
		reservation, err := a.fixedIPExists()
		if err != nil {
			return nil, err
		}

		return reservation, err
	}

	newIP, err := a.iterateForAssignment()
	if err != nil {
		return nil, err
	}

	return newIP, nil
}

func (a *AssignIPDetails) fixedIPExists() (*networkv1.IPAddress, error) {
	fixedIP := a.FixedIP.String()
	if slices.Contains(a.SubNet.ExcludeAddresses, fixedIP) {
		return nil, fmt.Errorf("FixedIP %s is in ExcludeAddresses", fixedIP)
	}

	// validate of nextip is already in a reservation and its not us
	f := func(c networkv1.Reservation) bool {
		return c.Spec.Reservation[a.NetName].Address == fixedIP
	}
	idx := slices.IndexFunc(a.Reservelist.Items, f)
	if idx >= 0 && a.Reservelist.Items[idx].Spec.IPSetRef.Name != a.IPSet {
		return nil, fmt.Errorf(fmt.Sprintf("%s already reserved for %s", fixedIP, a.Reservelist.Items[idx].Spec.IPSetRef.Name))
	}

	return &networkv1.IPAddress{
		Network: networkv1.NetNameStr(a.NetName),
		Subnet:  a.SubNet.Name,
		Address: fixedIP,
	}, nil
}

// IterateForAssignment iterates given an IP/IPNet and a list of reserved IPs
func (a *AssignIPDetails) iterateForAssignment() (*networkv1.IPAddress, error) {
	for _, allocRange := range a.SubNet.AllocationRanges {
		firstip, err := netip.ParseAddr(allocRange.Start)
		if err != nil {
			return nil, fmt.Errorf("failed to parse AllocationRange.Start IP %s: %w", allocRange.Start, err)
		}
		lastip, err := netip.ParseAddr(allocRange.End)
		if err != nil {
			return nil, fmt.Errorf("failed to parse AllocationRange.End IP %s: %w", allocRange.End, err)
		}

		// Iterate every IP address in the range
		nextip, _ := netip.ParseAddr(firstip.String())
		endip, _ := netip.ParseAddr(lastip.String())
		for nextip.Compare(endip) < 1 {
			// Skip addresses ending with 0
			ipSlice := nextip.AsSlice()
			if (nextip.Is4()) && (ipSlice[3] == 0) || (nextip.Is6() && (ipSlice[14] == 0) && (ipSlice[15] == 0)) {
				nextip = nextip.Next()
				continue
			}

			// Skip if in ExcludeAddresses list
			if slices.Contains(a.SubNet.ExcludeAddresses, nextip.String()) {
				nextip = nextip.Next()
				continue
			}

			// validate of nextip is already in a reservation and its not us
			f := func(c networkv1.Reservation) bool {
				return c.Spec.Reservation[a.NetName].Address == nextip.String()
			}
			idx := slices.IndexFunc(a.Reservelist.Items, f)
			if idx >= 0 && a.Reservelist.Items[idx].Spec.IPSetRef.Name != a.IPSet {
				nextip = nextip.Next()
				continue
			}

			// Found a free IP
			return &networkv1.IPAddress{
				Network: networkv1.NetNameStr(a.NetName),
				Subnet:  a.SubNet.Name,
				Address: nextip.String(),
			}, nil
		}
	}

	return nil, fmt.Errorf(fmt.Sprintf("no ip address could be created for %s in subnet %s", a.IPSet, a.SubNet.Name))
}
