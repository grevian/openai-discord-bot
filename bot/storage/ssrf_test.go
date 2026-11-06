package storage

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
)

func TestIsBlockedAddr(t *testing.T) {
	blocked := []string{
		"169.254.169.254", // EC2 IMDS (IPv4)
		"fd00:ec2::254",   // EC2 IMDS (IPv6)
		"::ffff:169.254.169.254",
		"127.0.0.1",
		"127.1.2.3",
		"::1",
		"10.0.0.1",
		"172.16.0.1",
		"172.31.255.255",
		"192.168.1.1",
		"100.64.0.1", // CGNAT
		"100.127.255.255",
		"0.0.0.0",
		"::",
		"224.0.0.1",
		"ff02::1",
		"255.255.255.255",
		"fe80::1",
		"fc00::1",
		"64:ff9b::a9fe:a9fe", // NAT64 of 169.254.169.254
	}
	for _, s := range blocked {
		if !isBlockedAddr(netip.MustParseAddr(s)) {
			t.Errorf("%s should be blocked", s)
		}
	}
	allowed := []string{
		"162.159.128.233", // a Cloudflare (Discord CDN) address
		"8.8.8.8",
		"100.63.255.255", // just below CGNAT
		"100.128.0.0",    // just above CGNAT
		"172.15.255.255", // just below 172.16/12
		"172.32.0.0",     // just above 172.16/12
		"2606:4700::6810:84e5",
	}
	for _, s := range allowed {
		if isBlockedAddr(netip.MustParseAddr(s)) {
			t.Errorf("%s should be allowed", s)
		}
	}
}

func TestGetImageFromURLRefusesLoopback(t *testing.T) {
	var hit bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hit = true
		_, _ = w.Write([]byte("secret"))
	}))
	defer srv.Close()

	s := &ImageStorage{httpClient: newGuardedClient(isBlockedAddr)}
	for _, u := range []string{
		srv.URL,
		strings.Replace(srv.URL, "127.0.0.1", "localhost", 1), // a name resolving to loopback
	} {
		rc, _, err := s.GetImageFromURL(context.Background(), u)
		if !errors.Is(err, ErrBlockedAddress) {
			t.Errorf("%s: err = %v, want ErrBlockedAddress", u, err)
		}
		if rc != nil {
			_ = rc.Close()
		}
	}
	if hit {
		t.Error("the loopback server was reached")
	}
}

func TestGetImageFromURLRefusesNonHTTPSchemes(t *testing.T) {
	s := &ImageStorage{httpClient: newGuardedClient(isBlockedAddr)}
	for _, u := range []string{"file:///etc/passwd", "ftp://example.com/a.png", "gopher://example.com/"} {
		if _, _, err := s.GetImageFromURL(context.Background(), u); !errors.Is(err, ErrBlockedScheme) {
			t.Errorf("%s: err = %v, want ErrBlockedScheme", u, err)
		}
	}
}

// A permitted host that redirects to a blocked one must not get through: the
// check runs again when the redirect target is dialled.
func TestGetImageFromURLRefusesRedirectToBlockedAddress(t *testing.T) {
	var internalHit bool
	internal := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		internalHit = true
		_, _ = w.Write([]byte("iam credentials"))
	}))
	defer internal.Close()
	public := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, internal.URL+"/latest/meta-data/", http.StatusFound)
	}))
	defer public.Close()

	publicAddr := netip.MustParseAddrPort(strings.TrimPrefix(public.URL, "http://"))
	internalAddr := netip.MustParseAddrPort(strings.TrimPrefix(internal.URL, "http://"))
	if publicAddr.Addr() != internalAddr.Addr() {
		t.Fatalf("test servers on different hosts: %s %s", publicAddr, internalAddr)
	}
	// Both servers listen on 127.0.0.1, so tell them apart by port: allow the
	// first dial (the public server) and block every later one.
	dials := 0
	blocked := func(netip.Addr) bool {
		dials++
		return dials > 1
	}
	s := &ImageStorage{httpClient: newGuardedClient(blocked)}

	_, _, err := s.GetImageFromURL(context.Background(), public.URL+"/cat.png")
	if !errors.Is(err, ErrBlockedAddress) {
		t.Fatalf("err = %v, want ErrBlockedAddress", err)
	}
	if internalHit {
		t.Error("the redirect target was reached")
	}
}

func TestGetImageFromURLRefusesRedirectToOtherScheme(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "file:///etc/passwd", http.StatusFound)
	}))
	defer srv.Close()
	s := &ImageStorage{httpClient: newGuardedClient(func(netip.Addr) bool { return false })}
	_, _, err := s.GetImageFromURL(context.Background(), srv.URL)
	if err == nil {
		t.Fatal("expected the redirect to a file: URL to fail")
	}
}

func TestGetImageFromURLCapsRedirects(t *testing.T) {
	n := 0
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n++
		http.Redirect(w, r, fmt.Sprintf("%s/%d", srv.URL, n), http.StatusFound)
	}))
	defer srv.Close()
	s := &ImageStorage{httpClient: newGuardedClient(func(netip.Addr) bool { return false })}
	if _, _, err := s.GetImageFromURL(context.Background(), srv.URL); err == nil {
		t.Fatal("expected an endless redirect chain to fail")
	}
	if n > maxRedirects {
		t.Errorf("followed %d redirects, cap is %d", n-1, maxRedirects)
	}
}

func TestGetImageFromURLAllowsPermittedAddress(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("png bytes"))
	}))
	defer srv.Close()
	s := &ImageStorage{httpClient: newGuardedClient(func(netip.Addr) bool { return false })}
	rc, _, err := s.GetImageFromURL(context.Background(), srv.URL)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	_ = rc.Close()
}
