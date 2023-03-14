package ipam

import (
	"fmt"
	"math/big"
	"net"

	networkv1beta1 "github.com/openstack-k8s-operators/infra-operator/apis/network/v1beta1"
)

// AssignIPDetails -
type AssignIPDetails struct {
	IPNet         net.IPNet
	RangeStart    net.IP
	RangeEnd      net.IP
	Reservelist   []networkv1beta1.IPReservation
	ExcludeRanges []string
	VIP           bool
	FixedIP       net.IP
}

// AssignmentError defines an IP assignment error.
type AssignmentError struct {
	firstIP net.IP
	lastIP  net.IP
	ipnet   net.IPNet
}

func (a AssignmentError) Error() string {
	return fmt.Sprintf("Could not allocate IP in range: ip: %v / - %v / range: %#v", a.firstIP, a.lastIP, a.ipnet)
}

// AssignIP assigns an IP using a range and a reserve list.
func AssignIP(assignIPDetails AssignIPDetails, fixedIP net.IP) (networkv1beta1.IPReservation, []networkv1beta1.IPReservation, error) {

	if fixedIP != nil {
		exists, reservation, err := fixedIPExists(assignIPDetails, fixedIP)
		if exists {
			assignIPDetails.Reservelist = append(assignIPDetails.Reservelist, reservation)
			return reservation, assignIPDetails.Reservelist, nil
		}
		return networkv1beta1.IPReservation{}, nil, err
	}

	newIP, updatedreservelist, err := iterateForAssignment(assignIPDetails)
	if err != nil {
		return networkv1beta1.IPReservation{}, nil, err
	}

	return newIP, updatedreservelist, nil
}

func fixedIPExists(assignIPDetails AssignIPDetails, ip net.IP) (bool, networkv1beta1.IPReservation, error) {
	firstip := assignIPDetails.RangeStart
	lastip := assignIPDetails.RangeEnd
	inRange := false
	for i := iPToBigInt(firstip); iPToBigInt(lastip).Cmp(i) == 1 || iPToBigInt(lastip).Cmp(i) == 0; i.Add(i, big.NewInt(1)) {
		if iPToBigInt(ip).Cmp(i) == 0 {
			inRange = true
			break
		}
	}
	if inRange {
		for _, rs := range assignIPDetails.Reservelist {
			if rs.IP == ip.String() {
				return false, networkv1beta1.IPReservation{}, fmt.Errorf("Already reserved %s", ip.String())
			}
		}
		return true, networkv1beta1.IPReservation{IP: ip.String(), VIP: false}, nil
	}
	return false, networkv1beta1.IPReservation{}, fmt.Errorf("Does not exist in range %s", ip.String())
}

// IterateForAssignment iterates given an IP/IPNet and a list of reserved IPs
func iterateForAssignment(assignIPDetails AssignIPDetails) (networkv1beta1.IPReservation, []networkv1beta1.IPReservation, error) {

	reservedIP := networkv1beta1.IPReservation{}
	firstip := assignIPDetails.RangeStart
	var lastip net.IP
	if assignIPDetails.RangeEnd != nil {
		lastip = assignIPDetails.RangeEnd
	} else {
		var err error
		firstip, lastip, err = getIPRange(assignIPDetails.RangeStart, assignIPDetails.IPNet)
		if err != nil {
			//logging.Errorf("GetIPRange request failed with: %v", err)
			return reservedIP, assignIPDetails.Reservelist, err
		}
	}
	//logging.Debugf("IterateForAssignment input >> ip: %v | ipnet: %v | first IP: %v | last IP: %v", rangeStart, ipnet, firstip, lastip)

	reserved := make(map[string]bool)
	for _, r := range assignIPDetails.Reservelist {
		ip := bigIntToIP(*iPToBigInt(net.ParseIP(r.IP)))
		reserved[ip.String()] = true
	}

	excluded := []*net.IPNet{}
	for _, v := range assignIPDetails.ExcludeRanges {
		_, subnet, _ := net.ParseCIDR(v)
		excluded = append(excluded, subnet)
	}

	// Iterate every IP address in the range
	var assignedip net.IP
	performedassignment := false
MAINITERATION:
	for i := iPToBigInt(firstip); iPToBigInt(lastip).Cmp(i) == 1 || iPToBigInt(lastip).Cmp(i) == 0; i.Add(i, big.NewInt(1)) {

		assignedip = bigIntToIP(*i)
		stringip := fmt.Sprint(assignedip)
		// For each address see if it has been allocated
		if reserved[stringip] {
			// Continue if this IP is allocated.
			continue
		}

		// We can try to work with the current IP
		// However, let's skip 0-based addresses
		// So go ahead and continue if the 4th/16th byte equals 0
		ipbytes := i.Bytes()
		if isIntIPv4(i) {
			if ipbytes[5] == 0 {
				continue
			}
		} else {
			if ipbytes[15] == 0 {
				continue
			}
		}

		// Lastly, we need to check if this IP is within the range of excluded subnets
		for _, subnet := range excluded {
			if subnet.Contains(bigIntToIP(*i).To16()) {
				continue MAINITERATION
			}
		}

		// Ok, this one looks like we can assign it!
		performedassignment = true
		reservedIP = networkv1beta1.IPReservation{
			IP:  assignedip.String(),
			VIP: assignIPDetails.VIP,
		}

		assignIPDetails.Reservelist = append(assignIPDetails.Reservelist, reservedIP)
		break
	}

	if !performedassignment {
		return networkv1beta1.IPReservation{}, assignIPDetails.Reservelist, AssignmentError{firstip, lastip, assignIPDetails.IPNet}
	}

	return reservedIP, assignIPDetails.Reservelist, nil
}

// GetIPRange returns the first and last IP in a range
func getIPRange(ip net.IP, ipnet net.IPNet) (net.IP, net.IP, error) {

	// Good hints here: http://networkbit.ch/golang-ip-address-manipulation/
	// Nice info on bitwise operations: https://yourbasic.org/golang/bitwise-operator-cheat-sheet/
	// Get info about the mask.
	mask := ipnet.Mask
	ones, bits := mask.Size()
	masklen := bits - ones

	// Error when the mask isn't large enough.
	if masklen < 2 {
		return nil, nil, fmt.Errorf("net mask is too short, must be 2 or more: %v", masklen)
	}

	// Get a long from the current IP address
	longip := iPToBigInt(ip)

	// Shift out to get the lowest IP value.
	var lowestiplong big.Int
	lowestiplong.Rsh(longip, uint(masklen))
	lowestiplong.Lsh(&lowestiplong, uint(masklen))

	// Get the mask as a long, shift it out
	var masklong big.Int
	// We need to generate the largest number...
	// Let's try to figure out if it's IPv4 or v6
	var maxval big.Int
	if len(lowestiplong.Bytes()) == net.IPv6len {
		// It's v6
		// Maximum IPv6 value: 0xffffffffffffffffffffffffffffffff
		maxval.SetString("0xffffffffffffffffffffffffffffffff", 0)
	} else {
		// It's v4
		// Maximum IPv4 value: 4294967295
		maxval.SetUint64(4294967295)
	}

	masklong.Rsh(&maxval, uint(ones))

	// Now figure out the highest value...
	// We can OR that value...
	var highestiplong big.Int
	highestiplong.Or(&lowestiplong, &masklong)
	// remove network and broadcast address from the  range
	var incIP big.Int
	incIP.SetInt64(1)
	lowestiplong.Add(&lowestiplong, &incIP)   // fixes to remove network address
	highestiplong.Sub(&highestiplong, &incIP) //fixes to remove broadcast address

	// Convert to net.IPs
	firstip := bigIntToIP(lowestiplong)
	if lowestiplong.Cmp(longip) < 0 { // if range_start was provided and its greater.
		firstip = bigIntToIP(*longip)
	}
	lastip := bigIntToIP(highestiplong)

	return firstip, lastip, nil

}

func isIntIPv4(checkipint *big.Int) bool {
	return !(len(checkipint.Bytes()) == net.IPv6len)
}

// BigIntToIP converts a big.Int to a net.IP
func bigIntToIP(inipint big.Int) net.IP {
	outip := net.IP(make([]byte, net.IPv6len))
	intbytes := inipint.Bytes()
	if len(intbytes) == net.IPv6len {
		// This is an IPv6 address.
		copy(outip, intbytes)
	} else {
		// It's an IPv4 address.
		for i := 0; i < len(intbytes); i++ {
			outip[i+10] = intbytes[i]
		}
	}
	return outip
}

// IPToBigInt converts a net.IP to a big.Int
func iPToBigInt(IPv6Addr net.IP) *big.Int {
	IPv6Int := big.NewInt(0)
	IPv6Int.SetBytes(IPv6Addr)
	return IPv6Int
}
