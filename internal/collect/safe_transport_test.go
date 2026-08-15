package collect

import (
	"context"
	"net"
	"net/netip"
	"strings"
	"testing"
)

type fixedResolver []net.IPAddr

func (r fixedResolver) LookupIPAddr(context.Context, string) ([]net.IPAddr, error) {
	return r, nil
}

func resolved(addresses ...string) fixedResolver {
	result := make(fixedResolver, 0, len(addresses))
	for _, address := range addresses {
		result = append(result, net.IPAddr{IP: net.ParseIP(address)})
	}
	return result
}

func TestPublicAddressPolicy(t *testing.T) {
	tests := []struct {
		address string
		public  bool
	}{
		{"93.184.216.34", true},
		{"2606:2800:220:1:248:1893:25c8:1946", true},
		{"127.0.0.1", false},
		{"10.0.0.1", false},
		{"172.16.0.1", false},
		{"192.168.1.1", false},
		{"169.254.169.254", false},
		{"100.64.0.1", false},
		{"198.18.0.1", false},
		{"192.0.2.1", false},
		{"::1", false},
		{"fc00::1", false},
		{"fe80::1", false},
		{"::ffff:127.0.0.1", false},
		{"2001:db8::1", false},
	}

	for _, tt := range tests {
		t.Run(tt.address, func(t *testing.T) {
			address := netip.MustParseAddr(tt.address).Unmap()
			if got := publicAddress(address); got != tt.public {
				t.Errorf("publicAddress(%s) = %t, want %t", address, got, tt.public)
			}
		})
	}
}

func TestSafeDialerRejectsAnyNonPublicResolution(t *testing.T) {
	called := false
	dialer := &safeDialer{
		resolver: resolved("93.184.216.34", "192.168.1.20"),
		dial: func(context.Context, string, string) (net.Conn, error) {
			called = true
			return nil, nil
		},
	}

	_, err := dialer.DialContext(context.Background(), "tcp", "feed.example:443")
	if err == nil || !strings.Contains(err.Error(), "non-public address 192.168.1.20") {
		t.Fatalf("error = %v, want the private resolution rejected", err)
	}
	if called {
		t.Error("dial was attempted after a non-public DNS answer")
	}
}

func TestSafeDialerConnectsToTheValidatedAddress(t *testing.T) {
	var target string
	dialer := &safeDialer{
		resolver: resolved("93.184.216.34"),
		dial: func(_ context.Context, network, address string) (net.Conn, error) {
			if network != "tcp" {
				t.Errorf("network = %q, want tcp", network)
			}
			target = address
			return nil, nil
		},
	}

	if _, err := dialer.DialContext(context.Background(), "tcp", "feed.example:443"); err != nil {
		t.Fatalf("DialContext returned error: %v", err)
	}
	if target != "93.184.216.34:443" {
		t.Errorf("dialed %q, want the already validated address", target)
	}
}

func TestSafeDialerRejectsLiteralPrivateAddress(t *testing.T) {
	dialer := &safeDialer{
		resolver: resolved("127.0.0.1"),
		dial: func(context.Context, string, string) (net.Conn, error) {
			t.Fatal("private address reached the network dialer")
			return nil, nil
		},
	}

	if _, err := dialer.DialContext(context.Background(), "tcp", "127.0.0.1:8080"); err == nil {
		t.Fatal("private literal was accepted")
	}
}
