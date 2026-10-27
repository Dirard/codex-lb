package domain

import "net/netip"

// Addresses that are not globally routable or that reach infrastructure next
// to the host (cloud metadata, carrier NAT, protocol assignments, reserved).
var nonPublicPrefixes = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("::/96"),
	netip.MustParsePrefix("64:ff9b:1::/48"),
	netip.MustParsePrefix("100::/64"),
	netip.MustParsePrefix("2001::/32"),
	netip.MustParsePrefix("fec0::/10"),
}

var (
	nat64Prefix    = netip.MustParsePrefix("64:ff9b::/96")
	sixToFourRange = netip.MustParsePrefix("2002::/16")
)

// PublicUnicast reports whether ip is a globally routable unicast address.
// IPv6 forms that embed an IPv4 address are judged by the embedded address.
func PublicUnicast(ip netip.Addr) bool {
	ip = ip.Unmap()
	if !ip.IsValid() || ip.Zone() != "" || ip.IsLoopback() || ip.IsPrivate() || ip.IsUnspecified() ||
		ip.IsLinkLocalUnicast() || ip.IsMulticast() || ip.IsLinkLocalMulticast() || ip.IsInterfaceLocalMulticast() {
		return false
	}
	for _, prefix := range nonPublicPrefixes {
		if prefix.Contains(ip) {
			return false
		}
	}
	raw := ip.As16()
	switch {
	case nat64Prefix.Contains(ip):
		return PublicUnicast(netip.AddrFrom4([4]byte(raw[12:16])))
	case sixToFourRange.Contains(ip):
		return PublicUnicast(netip.AddrFrom4([4]byte(raw[2:6])))
	}
	return true
}
