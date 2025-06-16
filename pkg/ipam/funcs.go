package ipam

import (
	"fmt"
	"net/netip"

	networkv1 "github.com/openstack-k8s-operators/infra-operator/apis/network/v1beta1"
)

// AssignIPDetails -
type AssignIPDetails struct {
	IPSet       string
	NetName     string
	SubNet      *networkv1.Subnet
	Reservelist *networkv1.ReservationList
	FixedIP     netip.Addr
	// internal use only
	excluded map[netip.Addr]bool
	reserved map[netip.Addr]string
}

func (a *AssignIPDetails) buildExcluded() error {
	if a.excluded != nil {
		return nil
	}
	a.excluded = make(map[netip.Addr]bool, len(a.SubNet.ExcludeAddresses))
	for _, ipStr := range a.SubNet.ExcludeAddresses {
		if ip, err := netip.ParseAddr(ipStr); err == nil {
			a.excluded[ip] = true
		} else {
			return fmt.Errorf("failed to parse ExcludeAddresses %s: %w", ipStr, err)
		}
	}
	return nil
}

func (a *AssignIPDetails) buildReserved() error {
	if a.reserved != nil {
		return nil
	}
	a.reserved = make(map[netip.Addr]string, len(a.Reservelist.Items))
	for _, r := range a.Reservelist.Items {
		if res, ok := r.Spec.Reservation[a.NetName]; ok {
			if ip, err := netip.ParseAddr(res.Address); err == nil {
				a.reserved[ip] = r.Spec.IPSetRef.Name
			} else {
				return fmt.Errorf("failed to parse reservation ip %s: %w", res.Address, err)
			}
		}
	}
	return nil
}

// AssignIP assigns an IP using a range and a reserve list.
func (a *AssignIPDetails) AssignIP() (*networkv1.IPAddress, error) {

	err := a.buildExcluded()
	if err != nil {
		return nil, fmt.Errorf("failed to build excluded IPs: %w", err)

	}
	err = a.buildReserved()
	if err != nil {
		return nil, fmt.Errorf("failed to build reserved IPs: %w", err)
	}

	if a.FixedIP.IsValid() {
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

	if _, ok := a.excluded[a.FixedIP]; ok {
		return nil, fmt.Errorf("FixedIP %s is in ExcludeAddresses", a.FixedIP.String())
	}

	if reservedIPSet, ok := a.reserved[a.FixedIP]; ok && reservedIPSet != a.IPSet {
		return nil, fmt.Errorf("%s already reserved for %s", a.FixedIP.String(), reservedIPSet)
	}

	return &networkv1.IPAddress{
		Network: networkv1.NetNameStr(a.NetName),
		Subnet:  a.SubNet.Name,
		Address: a.FixedIP.String(),
	}, nil
}

func (a *AssignIPDetails) iterateForAssignment() (*networkv1.IPAddress, error) {

	for _, allocRange := range a.SubNet.AllocationRanges {
		firstip, err := netip.ParseAddr(allocRange.Start)
		if err != nil {
			return nil, fmt.Errorf("failed to parse allocation range start IP %s: %w", allocRange.Start, err)
		}
		lastip, err := netip.ParseAddr(allocRange.End)
		if err != nil {
			return nil, fmt.Errorf("failed to parse allocation range end IP %s: %w", allocRange.End, err)
		}

		for ip := firstip; ip.Compare(lastip) <= 0; ip = ip.Next() {

			if ip.Is4() {
				ip4 := ip.As4()
				// Skip ipv4 addresses ending with 0
				if ip4[3] == 0 {
					continue
				}
			}

			if ip.Is6() {
				ip16 := ip.As16()
				// Skip ipv6 addresses ending with 0
				if ip16[14] == 0 && ip16[15] == 0 {
					continue
				}
			}

			// check if the IP is in the excluded or reserved sets
			if _, ok := a.excluded[ip]; ok {
				continue
			}
			if reservedIPSet, ok := a.reserved[ip]; ok && reservedIPSet != a.IPSet {
				continue
			}

			return &networkv1.IPAddress{
				Network: networkv1.NetNameStr(a.NetName),
				Subnet:  a.SubNet.Name,
				Address: ip.String(),
			}, nil
		}
	}

	return nil, fmt.Errorf("no ip address could be created for %s in subnet %s", a.IPSet, a.SubNet.Name)
}
