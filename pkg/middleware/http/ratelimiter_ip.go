package http

import (
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"strings"
)

// ipResolver derives the rate limit key of a request from the address the request
// actually came from.
//
// X-Forwarded-For is written by the caller, so it can only be believed for the hops
// that a proxy this storage provider trusts appended itself. With no trusted proxy
// configured the header is ignored entirely: honouring it would let any caller mint
// an unlimited number of rate limit buckets just by varying a header.
type ipResolver struct {
	trustedProxies []netip.Prefix
}

// newIPResolver builds a resolver from the configured trusted proxy list. Entries
// are CIDR blocks ("10.0.0.0/8") or single addresses ("192.0.2.1").
func newIPResolver(trustedProxies []string) (*ipResolver, error) {
	resolver := &ipResolver{}
	for _, entry := range trustedProxies {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		prefix, err := parsePrefix(entry)
		if err != nil {
			return nil, fmt.Errorf("invalid trusted proxy %q: %w", entry, err)
		}
		resolver.trustedProxies = append(resolver.trustedProxies, prefix)
	}
	return resolver, nil
}

func parsePrefix(entry string) (netip.Prefix, error) {
	if prefix, err := netip.ParsePrefix(entry); err == nil {
		return prefix, nil
	}
	addr, err := netip.ParseAddr(entry)
	if err != nil {
		return netip.Prefix{}, err
	}
	return netip.PrefixFrom(addr, addr.BitLen()), nil
}

// clientIP returns the address to rate limit the request by.
func (p *ipResolver) clientIP(r *http.Request) string {
	peer := peerAddr(r.RemoteAddr)
	if !p.trusted(peer) {
		return peer.String()
	}
	// walk the chain right to left and stop at the first hop that a trusted proxy
	// did not vouch for; everything further left was supplied by the caller
	hops := strings.Split(r.Header.Get("X-Forwarded-For"), ",")
	for i := len(hops) - 1; i >= 0; i-- {
		addr, err := netip.ParseAddr(strings.TrimSpace(hops[i]))
		if err != nil {
			break
		}
		if !p.trusted(addr) {
			return addr.String()
		}
	}
	return peer.String()
}

func (p *ipResolver) trusted(addr netip.Addr) bool {
	if !addr.IsValid() {
		return false
	}
	for _, prefix := range p.trustedProxies {
		if prefix.Contains(addr) {
			return true
		}
	}
	return false
}

// peerAddr parses the address of the socket the request arrived on, dropping the
// port so that every connection from one client shares a rate limit bucket.
func peerAddr(remoteAddr string) netip.Addr {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		host = remoteAddr
	}
	addr, err := netip.ParseAddr(host)
	if err != nil {
		return netip.Addr{}
	}
	return addr.Unmap()
}
