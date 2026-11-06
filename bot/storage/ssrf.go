package storage

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"syscall"
	"time"
)

// ErrBlockedAddress is returned (wrapped) when a fetch would connect to an
// address that must never be reached from the bot: loopback, private,
// link-local (including the EC2 instance metadata service at 169.254.169.254
// and fd00:ec2::254), unspecified, multicast, CGNAT and other special ranges.
var ErrBlockedAddress = errors.New("destination address is not allowed")

// ErrBlockedScheme is returned (wrapped) for any URL that is not http or https.
var ErrBlockedScheme = errors.New("only http and https URLs are allowed")

// maxRedirects caps how many redirects an outbound fetch follows.
const maxRedirects = 5

// blockedPrefixes are the ranges an outbound fetch may not connect to. IPv4
// addresses are checked after unmapping, so ::ffff:169.254.169.254 is caught
// by the IPv4 entry.
var blockedPrefixes = func() []netip.Prefix {
	cidrs := []string{
		// IPv4
		"0.0.0.0/8",       // "this network", includes the unspecified 0.0.0.0
		"10.0.0.0/8",      // private
		"100.64.0.0/10",   // CGNAT (shared address space)
		"127.0.0.0/8",     // loopback
		"169.254.0.0/16",  // link-local, includes EC2 IMDS 169.254.169.254
		"172.16.0.0/12",   // private
		"192.0.0.0/24",    // IETF protocol assignments
		"192.0.2.0/24",    // TEST-NET-1
		"192.88.99.0/24",  // 6to4 relay anycast
		"192.168.0.0/16",  // private
		"198.18.0.0/15",   // benchmarking
		"198.51.100.0/24", // TEST-NET-2
		"203.0.113.0/24",  // TEST-NET-3
		"224.0.0.0/4",     // multicast
		"240.0.0.0/4",     // reserved, includes broadcast 255.255.255.255
		// IPv6
		"::/128",         // unspecified
		"::1/128",        // loopback
		"64:ff9b::/96",   // NAT64: can embed any IPv4 address, including IMDS
		"64:ff9b:1::/48", // local-use NAT64
		"100::/64",       // discard-only
		"2001:db8::/32",  // documentation
		"fc00::/7",       // unique local, includes EC2 IMDS fd00:ec2::254
		"fe80::/10",      // link-local
		"ff00::/8",       // multicast
	}
	out := make([]netip.Prefix, 0, len(cidrs))
	for _, c := range cidrs {
		out = append(out, netip.MustParsePrefix(c))
	}
	return out
}()

// isBlockedAddr reports whether an outbound connection to addr must be refused.
func isBlockedAddr(addr netip.Addr) bool {
	addr = addr.Unmap()
	if !addr.IsValid() || addr.Zone() != "" {
		return true
	}
	for _, p := range blockedPrefixes {
		if p.Contains(addr) {
			return true
		}
	}
	return false
}

// dialControl runs after DNS resolution, once per connection attempt, with the
// literal IP the socket is about to connect to. Checking here rather than on
// the URL's host covers redirects and DNS rebinding: whatever name led here,
// the address actually dialled is the one checked.
func dialControl(blocked func(netip.Addr) bool) func(network, address string, _ syscall.RawConn) error {
	return func(network, address string, _ syscall.RawConn) error {
		host, _, err := net.SplitHostPort(address)
		if err != nil {
			return fmt.Errorf("%w: %q: %v", ErrBlockedAddress, address, err)
		}
		ip, err := netip.ParseAddr(host)
		if err != nil {
			return fmt.Errorf("%w: %q is not an IP address", ErrBlockedAddress, host)
		}
		if blocked(ip) {
			return fmt.Errorf("%w: %s", ErrBlockedAddress, ip)
		}
		return nil
	}
}

// checkScheme refuses any URL that is not http or https.
func checkScheme(u *url.URL) error {
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("%w: %q", ErrBlockedScheme, u.Scheme)
	}
	return nil
}

// newGuardedTransport returns a transport for fetching untrusted URLs. It
// never uses a proxy (a proxy would be the address dialled, hiding the real
// destination from the check) and refuses connections to blocked addresses.
func newGuardedTransport(blocked func(netip.Addr) bool) *http.Transport {
	dialer := &net.Dialer{
		Timeout:   30 * time.Second,
		KeepAlive: 30 * time.Second,
		Control:   dialControl(blocked),
	}
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.Proxy = nil
	t.DialContext = dialer.DialContext
	return t
}

// checkRedirect applies the scheme rule and a redirect cap to every hop. The
// address of each hop is checked at dial time by the transport.
func checkRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= maxRedirects {
		return fmt.Errorf("stopped after %d redirects", maxRedirects)
	}
	return checkScheme(req.URL)
}
