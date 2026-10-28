package domain

import (
	"net/netip"
	"testing"
)

func TestPublicUnicastClassification(t *testing.T) {
	for address, public := range map[string]bool{
		"203.0.113.10": true, "8.8.8.8": true, "2606:4700::1111": true,
		"127.0.0.1": false, "10.1.2.3": false, "172.16.0.1": false, "192.168.1.1": false,
		"169.254.169.254": false, "100.100.100.200": false, "0.0.0.0": false, "0.1.2.3": false,
		"224.0.0.1": false, "255.255.255.255": false, "198.18.0.1": false, "192.0.0.170": false,
		"::1": false, "::": false, "fe80::1": false, "fd00:ec2::254": false, "fec0::1": false,
		"::ffff:127.0.0.1": false, "::ffff:8.8.8.8": true, "64:ff9b::a00:1": false, "64:ff9b::808:808": true,
		"2002:a00:1::": false, "2002:808:808::": true, "2001:0:4136:e378::1": false, "ff02::1": false,
	} {
		if got := PublicUnicast(netip.MustParseAddr(address)); got != public {
			t.Errorf("PublicUnicast(%s) = %v, want %v", address, got, public)
		}
	}
}
