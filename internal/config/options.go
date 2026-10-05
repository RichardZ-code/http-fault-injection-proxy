// Package config owns option validation and the temporary P03 no-fault schema.
package config

import (
	"fmt"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Options contains resolved CLI/environment values, not validated scenarios.
type Options struct {
	Upstream, Path, Listen, AdminListen string
}

// Validate checks pure option syntax without files, DNS, listeners, or upstream I/O.
func (o Options) Validate() error {
	_, err := o.ValidatedUpstream()
	return err
}

// ValidatedUpstream checks the same pure options and returns the parsed fixed
// origin. Configuration file admission still must precede runtime startup.
func (o Options) ValidatedUpstream() (*url.URL, error) {
	if strings.TrimSpace(o.Path) == "" {
		return nil, fmt.Errorf("--config requires an explicit nonempty path")
	}
	u, ok := parseUpstream(o.Upstream)
	if !ok {
		return nil, fmt.Errorf("--upstream requires an HTTP origin without credentials, query, fragment, or base path")
	}
	data, err := listener(o.Listen)
	if err != nil {
		return nil, fmt.Errorf("--listen requires a numeric IP and port in 1-65535")
	}
	admin, err := listener(o.AdminListen)
	if err != nil {
		return nil, fmt.Errorf("--admin-listen requires a numeric IP and port in 1-65535")
	}
	if data == admin {
		return nil, fmt.Errorf("data and admin listener endpoints must differ")
	}
	u.Path, u.RawPath = "", ""
	return u, nil
}

func listener(raw string) (netip.AddrPort, error) {
	a, err := netip.ParseAddrPort(raw)
	if err != nil || a.Port() == 0 {
		return netip.AddrPort{}, fmt.Errorf("invalid listener")
	}
	return netip.AddrPortFrom(a.Addr().Unmap(), a.Port()), nil
}

func parseUpstream(raw string) (*url.URL, bool) {
	if !utf8.ValidString(raw) || strings.ContainsAny(raw, "#") || strings.IndexFunc(raw, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) >= 0 {
		return nil, false
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "http" || u.Hostname() == "" || u.User != nil || u.Opaque != "" || u.RawQuery != "" || u.ForceQuery || (u.Path != "" && u.Path != "/") {
		return nil, false
	}
	if strings.HasSuffix(u.Host, ":") {
		return nil, false
	}
	if port := u.Port(); port != "" {
		n, err := strconv.ParseUint(port, 10, 16)
		if err != nil || n == 0 {
			return nil, false
		}
	}
	if strings.Contains(u.Hostname(), ":") || strings.HasPrefix(u.Host, "[") {
		a, err := netip.ParseAddr(u.Hostname())
		if err != nil || !a.Is6() || !strings.HasPrefix(u.Host, "[") {
			return nil, false
		}
	}
	return u, true
}
