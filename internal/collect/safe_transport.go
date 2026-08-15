package collect

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"strings"
)

// ipResolver is the part of net.Resolver needed by safeDialer. Keeping it
// narrow makes the address policy testable without consulting real DNS.
type ipResolver interface {
	LookupIPAddr(context.Context, string) ([]net.IPAddr, error)
}

type dialContextFunc func(context.Context, string, string) (net.Conn, error)

// safeDialer prevents content-controlled web addresses from reaching services
// on the machine running Ziba or on its private network.
//
// It resolves the name itself and dials the validated address directly. Asking
// the ordinary dialer to resolve the hostname again after checking it would
// leave a DNS-rebinding gap between validation and connection.
type safeDialer struct {
	resolver ipResolver
	dial     dialContextFunc
}

func newSafeDialer() *safeDialer {
	dialer := &net.Dialer{}
	return &safeDialer{
		resolver: net.DefaultResolver,
		dial:     dialer.DialContext,
	}
}

func (d *safeDialer) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, fmt.Errorf("split destination %q: %w", address, err)
	}

	resolved, err := d.resolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, fmt.Errorf("resolve destination %s: %w", host, err)
	}
	if len(resolved) == 0 {
		return nil, fmt.Errorf("resolve destination %s: no addresses", host)
	}

	addresses := make([]netip.Addr, 0, len(resolved))
	for _, candidate := range resolved {
		ip, ok := netip.AddrFromSlice(candidate.IP)
		if !ok {
			return nil, fmt.Errorf("resolve destination %s: invalid address %q", host, candidate.IP)
		}
		ip = ip.Unmap()
		if !publicAddress(ip) {
			return nil, fmt.Errorf("destination %s resolves to non-public address %s", host, ip)
		}
		addresses = append(addresses, ip)
	}

	var failures []string
	for _, ip := range addresses {
		target := net.JoinHostPort(ip.String(), port)
		conn, err := d.dial(ctx, network, target)
		if err == nil {
			return conn, nil
		}
		failures = append(failures, err.Error())
	}
	return nil, fmt.Errorf("connect to %s: %s", host, strings.Join(failures, "; "))
}

var nonPublicNetworks = []netip.Prefix{
	// Shared address space and documentation/benchmark networks are not
	// globally reachable destinations, even though netip calls some of them
	// global unicast.
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("2001:db8::/32"),
}

func publicAddress(ip netip.Addr) bool {
	if !ip.IsValid() || !ip.IsGlobalUnicast() || ip.IsPrivate() ||
		ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsMulticast() || ip.IsUnspecified() {
		return false
	}
	for _, blocked := range nonPublicNetworks {
		if blocked.Contains(ip) {
			return false
		}
	}
	return true
}
