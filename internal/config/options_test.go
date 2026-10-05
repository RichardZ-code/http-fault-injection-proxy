package config

import (
	"strings"
	"testing"
)

func TestOptionValidation(t *testing.T) {
	base := Options{Upstream: "http://unresolved.invalid", Path: "not opened.yaml", Listen: "127.0.0.1:8080", AdminListen: "127.0.0.1:9090"}
	if err := base.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, u := range []string{"http://host/", "http://127.0.0.1:1", "http://[::1]:65535"} {
		o := base
		o.Upstream = u
		if err := o.Validate(); err != nil {
			t.Errorf("accepted origin %q: %v", u, err)
		}
	}
	for _, u := range []string{"", " ", "https://host", "http://", "http://user:private@host", "http://host?", "http://host?q=x", "http://host#", "http://host#fragment", "http://host/base", "http://host:0", "http://host:65536", "http://host:", "http://host:bad", "http:opaque", "http://::1", "http://[host]", "http://host/%ZZ", "http://host\n"} {
		o := base
		o.Upstream = u
		if err := o.Validate(); err == nil {
			t.Errorf("invalid origin accepted %q", u)
		} else if strings.Contains(err.Error(), "private") {
			t.Fatal("credentials leaked")
		}
	}
	for _, addr := range []string{"", " ", "localhost:8080", "127.0.0.1:0", "127.0.0.1:65536", "127.0.0.1", "::1:8080"} {
		o := base
		o.Listen = addr
		if err := o.Validate(); err == nil {
			t.Errorf("invalid data listener accepted %q", addr)
		}
		o = base
		o.AdminListen = addr
		if err := o.Validate(); err == nil {
			t.Errorf("invalid admin listener accepted %q", addr)
		}
	}
	o := base
	o.Path = " \t"
	if err := o.Validate(); err == nil {
		t.Fatal("whitespace path accepted")
	}
	o = base
	o.AdminListen = o.Listen
	if err := o.Validate(); err == nil {
		t.Fatal("duplicate endpoint accepted")
	}
	o = base
	o.Listen = "[::ffff:127.0.0.1]:9090"
	if err := o.Validate(); err == nil {
		t.Fatal("equivalent mapped endpoint accepted")
	}
	o = base
	o.Listen = "[::]:8080"
	o.AdminListen = "[::1]:9090"
	if err := o.Validate(); err != nil {
		t.Fatalf("numeric IPv6 listener: %v", err)
	}
}
