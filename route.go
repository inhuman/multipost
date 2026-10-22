package main

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"os/exec"
	"regexp"
	"strings"
)

// Telegram is reachable only through the VPN, Russian platforms (MAX, VK)
// only past it (on the author's machine rus-routes.sh pins their IPs to the
// physical gateway). A post sent the wrong way either hangs or gets anti-bot stubs,
// so every host is checked before it is used, upload hosts included —
// those are handed out per request and may not be pinned.

type routeWant int

const (
	viaVPN routeWant = iota
	viaDirect
	viaAny
)

var reDev = regexp.MustCompile(`\bdev\s+(\S+)`)

func isVPNDev(dev string) bool {
	for _, p := range []string{"amn", "awg", "wg", "tun", "tap", "ppp"} {
		if strings.HasPrefix(dev, p) {
			return true
		}
	}
	return false
}

type routeFunc func(ctx context.Context, rawURL string, want routeWant) error

// routeGuard checks where traffic to a host leaves the machine; lookup and
// dev are the system resolver and `ip route get` in production.
type routeGuard struct {
	lookup func(ctx context.Context, host string) ([]net.IP, error)
	dev    func(ctx context.Context, ip net.IP) (string, error)
}

var systemRoutes = routeGuard{
	lookup: func(ctx context.Context, host string) ([]net.IP, error) {
		return net.DefaultResolver.LookupIP(ctx, "ip4", host)
	},
	dev: func(ctx context.Context, ip net.IP) (string, error) {
		out, err := exec.CommandContext(ctx, "ip", "-4", "route", "get", ip.String()).Output()
		if err != nil {
			return "", fmt.Errorf("ip route get %s: %w", ip, err)
		}
		m := reDev.FindStringSubmatch(string(out))
		if m == nil {
			return "", fmt.Errorf("ip route get %s: no dev in %q", ip, out)
		}
		return m[1], nil
	},
}

var checkRoute routeFunc = systemRoutes.check

// check resolves the host and verifies every IPv4 address leaves through
// the wanted interface kind: one wrong address is enough to refuse, since
// the client may pick any of them.
func (g routeGuard) check(ctx context.Context, rawURL string, want routeWant) error {
	if want == viaAny {
		return nil
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return err
	}
	host := u.Hostname()
	ips, err := g.lookup(ctx, host)
	if err != nil {
		return fmt.Errorf("resolve %s: %w", host, err)
	}
	if len(ips) == 0 {
		return fmt.Errorf("resolve %s: no IPv4 address", host)
	}
	for _, ip := range ips {
		dev, err := g.dev(ctx, ip)
		if err != nil {
			return err
		}
		vpn := isVPNDev(dev)
		switch {
		case want == viaDirect && vpn:
			return fmt.Errorf("%s (%s) goes through VPN (%s): add %s to the direct routes (rus-routes.sh up)", host, ip, dev, host)
		case want == viaVPN && !vpn:
			return fmt.Errorf("%s (%s) goes direct (%s), this platform needs the VPN: is it up?", host, ip, dev)
		}
	}
	return nil
}
