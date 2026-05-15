package sandbox

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const MaxFetchBodyBytes = 8 * 1024 * 1024 // 8 MiB

// normalizeFetchURLForPolicy returns rawURL with its scheme and host
// lowercased so case-insensitive match succeeds against deny rules written
// in conventional lowercase form. Path/query stay as-is (paths may be
// case-sensitive on the target server). On parse failure rawURL is
// returned unchanged — PolicyCheck will deny obviously malformed URLs
// either way and assertFetchURLAllowed re-parses for the SSRF check.
func normalizeFetchURLForPolicy(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return rawURL
	}
	if u.Scheme != "" {
		u.Scheme = strings.ToLower(u.Scheme)
	}
	if u.Host != "" {
		u.Host = strings.ToLower(u.Host)
	}
	return u.String()
}

// assertFetchURLAllowed refuses URLs that target loopback, private, link-local,
// multicast, or cloud metadata ranges. Call on the initial URL and again on
// every redirect.
func assertFetchURLAllowed(rawURL string) error {
	u, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("parse url: %w", err)
	}
	switch strings.ToLower(u.Scheme) {
	case "http", "https":
		// ok
	default:
		return fmt.Errorf("%w: unsupported scheme %q", ErrBlockedHost, u.Scheme)
	}
	host := u.Hostname()
	if host == "" {
		return fmt.Errorf("%w: empty host", ErrBlockedHost)
	}
	// V-13: explicit zone-id reject. `[fe80::1%25eth0]` survives url.Parse
	// and url.Hostname() returns `fe80::1%eth0` (zone separator decoded).
	// net.ParseIP rejects strings with `%`, so without this check the host
	// would silently fall through to LookupIP and surface as a generic DNS
	// failure. A zone-id is a same-machine concept; surface it as the
	// SSRF policy rejection it actually is.
	if strings.Contains(host, "%") {
		return fmt.Errorf("%w: IPv6 zone-id not permitted in fetch host %q", ErrBlockedHost, host)
	}
	// Well-known cloud metadata hostnames. Block these before DNS so a
	// resolver that maps them to public IPs cannot evade the IP filter.
	lowerHost := strings.ToLower(host)
	if lowerHost == "metadata.google.internal" || lowerHost == "metadata.goog" ||
		lowerHost == "metadata.goog.internal" || lowerHost == "metadata.goog.com" {
		return fmt.Errorf("%w: cloud metadata host %q", ErrBlockedHost, host)
	}
	// Host is allowed to be a bare IP literal; parse first so we don't need DNS.
	if ip := net.ParseIP(host); ip != nil {
		if isBlockedIP(ip) {
			return fmt.Errorf("%w: literal address %s is blocked", ErrBlockedHost, ip)
		}
		return nil
	}
	// Resolve host to IP(s) and reject if any address falls in a blocked range.
	// A DNS failure is treated as block, not allow: the previous "pass on
	// LookupIP error" behavior allowed attacker-controlled hostnames that
	// briefly NXDOMAIN'd but later resolved to an internal IP — the HTTP
	// client's own resolver could hit a different result than ours.
	ips, err := net.LookupIP(host)
	if err != nil {
		return fmt.Errorf("%w: DNS resolution failed for %s: %v", ErrBlockedHost, host, err)
	}
	if len(ips) == 0 {
		return fmt.Errorf("%w: no addresses for %s", ErrBlockedHost, host)
	}
	for _, ip := range ips {
		if isBlockedIP(ip) {
			return fmt.Errorf("%w: %s resolved to blocked address %s", ErrBlockedHost, host, ip)
		}
	}
	return nil
}

// isBlockedIP returns true if ip is a loopback, private (RFC1918/ULA),
// link-local, multicast, unspecified, or cloud-metadata IP.
func isBlockedIP(ip net.IP) bool {
	if ip == nil {
		return true
	}
	if ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsMulticast() || ip.IsUnspecified() || ip.IsInterfaceLocalMulticast() {
		return true
	}
	if ip.IsPrivate() {
		return true
	}
	// Cloud metadata literals.
	if ip.Equal(net.IPv4(169, 254, 169, 254)) {
		return true // AWS IMDS (IPv4)
	}
	if ip.Equal(net.IPv4(168, 63, 129, 16)) {
		return true // Azure IMDS
	}
	if ip.Equal(net.ParseIP("fd00:ec2::254")) {
		return true // AWS IMDS (IPv6)
	}
	// 0.0.0.0/8 - explicitly block any lingering non-routable addresses.
	if v4 := ip.To4(); v4 != nil && v4[0] == 0 {
		return true
	}
	// V-I2: RFC 6598 carrier-grade NAT space (100.64.0.0/10). Go's
	// IsPrivate() does not include this range — but on cloud hosts
	// (especially AWS NAT gateway-fronted) and ISP networks it routes
	// to internal infrastructure. Treat as private for SSRF purposes.
	if v4 := ip.To4(); v4 != nil && v4[0] == 100 && v4[1] >= 64 && v4[1] <= 127 {
		return true
	}
	// IPv6: RFC 3849 documentation prefix (2001:db8::/32). Used in
	// documentation and examples; some internal networks use it.
	// Block to prevent information disclosure via SSRF to documentation-only ranges.
	if isIPv6InPrefix(ip, net.ParseIP("2001:db8::"), 32) {
		return true
	}
	// IPv6: RFC 3964 Site-Local address space (deprecated, but some
	// internal networks may still have it). fec0::/10 - deprecated by RFC 3879.
	if isIPv6InPrefix(ip, net.ParseIP("fec0::"), 10) {
		return true
	}
	return false
}

// ErrBlockedHost indicates the target host/IP falls into a blocked range
// (loopback, private, link-local, cloud metadata, etc.) and was refused for SSRF reasons.
var ErrBlockedHost = errors.New("host blocked by SSRF policy")

// isIPv6InPrefix returns true if ip is in the IPv6 prefix specified by
// network (the start of the prefix) and the number of significant bits.
// Used for blocking RFC 3849 documentation prefixes (2001:db8::/32) and
// deprecated site-local ranges (fec0::/10) that IsPrivate/IsLinkLocal don't cover.
func isIPv6InPrefix(ip net.IP, network net.IP, bits int) bool {
	if ip == nil || network == nil {
		return false
	}
	ip6 := ip.To16()
	net6 := network.To16()
	if ip6 == nil || net6 == nil {
		return false
	}
	// Compare the first ceil(bits/8) bytes, then compare the remaining bits.
	wholeBytes := bits / 8
	remainingBits := bits % 8
	for i := 0; i < wholeBytes; i++ {
		if ip6[i] != net6[i] {
			return false
		}
	}
	if remainingBits > 0 && wholeBytes < 16 {
		// Mask to get the significant bits
		mask := byte(0xFF << (8 - remainingBits))
		if ip6[wholeBytes]&mask != net6[wholeBytes]&mask {
			return false
		}
	}
	return true
}

func (s *SandboxImpl) Fetch(ctx context.Context, req FetchReq) (FetchResp, error) {
	// Policy check. URLs are normalized first so deny rules like
	// `http://169.254.169.254/*` match `HTTP://169.254.169.254/foo`. The
	// scheme and host components are case-insensitive per RFC 3986; the
	// path portion stays case-sensitive (paths can be case-sensitive on
	// real servers). Closes F-28: pre-fix, an attacker could try
	// `HTTPS://metadata.google.internal/...` and the cloud-metadata deny
	// glob would never fire because the regex compare was byte-for-byte.
	if err := s.PolicyCheck("fetch", normalizeFetchURLForPolicy(req.URL)); err != nil {
		return FetchResp{}, err
	}

	// SSRF pre-check on the initial URL.
	if err := assertFetchURLAllowed(req.URL); err != nil {
		return FetchResp{}, err
	}

	// Basic HTTP fetch
	method := req.Method
	if method == "" {
		method = "GET"
	}

	var bodyReader io.Reader
	if req.Body != "" {
		bodyReader = strings.NewReader(req.Body)
	}

	httpReq, err := http.NewRequestWithContext(ctx, method, req.URL, bodyReader)
	if err != nil {
		return FetchResp{}, fmt.Errorf("create request: %w", err)
	}

	// V-13: validate header keys and values for CR/LF before Header.Set.
	// Go's net/http only enforces this at request-write time and surfaces a
	// generic "net/http: invalid header field …" — by that point the SSRF
	// pre-check, dialer setup, and DNS work has already been spent. Reject
	// upfront so the caller sees the actual reason.
	for k, v := range req.Headers {
		if strings.ContainsAny(k, "\r\n:") || strings.ContainsAny(v, "\r\n") {
			return FetchResp{}, fmt.Errorf("invalid header %q: contains CR/LF or colon", k)
		}
		httpReq.Header.Set(k, v)
	}

	// Guard against an unset/zero-value timeout. req.Timeout == 0 would
	// produce client.Timeout == 0, which means "no deadline" — a slow or
	// malicious server could then hang the goroutine indefinitely.
	timeout := req.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	// DNS-rebinding-safe transport: DialContext resolves the host itself,
	// validates every returned IP, and dials the literal IP. Without this,
	// http.Transport performs a *second* DNS lookup after assertFetchURLAllowed
	// and an attacker-controlled authoritative server can return a public IP
	// for the pre-check and 127.0.0.1 / 169.254.169.254 for the connect.
	dialer := &net.Dialer{Timeout: timeout, KeepAlive: 30 * time.Second}
	transport := &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			host, port, err := net.SplitHostPort(addr)
			if err != nil {
				return nil, err
			}
			// IP literal: validate once, dial directly.
			if ip := net.ParseIP(host); ip != nil {
				if isBlockedIP(ip) {
					return nil, fmt.Errorf("%w: literal address %s is blocked", ErrBlockedHost, ip)
				}
				return dialer.DialContext(ctx, network, addr)
			}
			// Hostname: resolve, validate every result, then dial the first
			// allowed IP by its literal address. Dialing the hostname again
			// here would reopen the rebinding window.
			ips, err := net.DefaultResolver.LookupIP(ctx, "ip", host)
			if err != nil {
				return nil, fmt.Errorf("%w: DNS resolution failed for %s: %v", ErrBlockedHost, host, err)
			}
			if len(ips) == 0 {
				return nil, fmt.Errorf("%w: no addresses for %s", ErrBlockedHost, host)
			}
			for _, ip := range ips {
				if isBlockedIP(ip) {
					return nil, fmt.Errorf("%w: %s resolved to blocked address %s", ErrBlockedHost, host, ip)
				}
			}
			return dialer.DialContext(ctx, network, net.JoinHostPort(ips[0].String(), port))
		},
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: timeout,
		MaxIdleConns:          10,
		IdleConnTimeout:       90 * time.Second,
	}
	defer transport.CloseIdleConnections()
	client := &http.Client{
		Timeout:   timeout,
		Transport: transport,
		// Re-check every redirect target against SSRF policy.
		CheckRedirect: func(r *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return errors.New("stopped after 10 redirects")
			}
			return assertFetchURLAllowed(r.URL.String())
		},
	}
	resp, err := client.Do(httpReq)
	if err != nil {
		return FetchResp{}, fmt.Errorf("fetch: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	// Cap body size to avoid runaway memory on huge responses.
	body, err := io.ReadAll(io.LimitReader(resp.Body, int64(MaxFetchBodyBytes)))
	if err != nil {
		return FetchResp{}, fmt.Errorf("read body: %w", err)
	}

	headers := make(map[string]string)
	for k, vv := range resp.Header {
		if len(vv) > 0 {
			headers[k] = vv[0]
		}
	}

	content := string(body)

	// Apply unified return-policy filter; see ApplyReturnPolicy for rules.
	// RawBody preserves the full bytes for the content store.
	filtered := ApplyReturnPolicy(content, req.Intent, req.Return)

	return FetchResp{
		Status:     resp.StatusCode,
		Headers:    headers,
		Body:       filtered.Body,
		RawBody:    content,
		Matches:    filtered.Matches,
		Summary:    filtered.Summary,
		Vocabulary: filtered.Vocabulary,
	}, nil
}
