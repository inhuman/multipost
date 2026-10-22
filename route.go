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
// only past it: ~/software/vpn/rus-routes.sh pins their IPs to the physical
// gateway. A post sent the wrong way either hangs or gets anti-bot stubs,
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

// checkRoute resolves host and verifies each IPv4 address leaves through
// the wanted interface kind.
func checkRoute(ctx context.Context, rawURL string, want routeWant) error {
	if want == viaAny {
		return nil
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return err
	}
	host := u.Hostname()
	ips, err := net.DefaultResolver.LookupIP(ctx, "ip4", host)
	if err != nil {
		return fmt.Errorf("resolve %s: %w", host, err)
	}
	for _, ip := range ips {
		out, err := exec.CommandContext(ctx, "ip", "-4", "route", "get", ip.String()).Output()
		if err != nil {
			return fmt.Errorf("ip route get %s: %w", ip, err)
		}
		m := reDev.FindStringSubmatch(string(out))
		if m == nil {
			return fmt.Errorf("ip route get %s: no dev in %q", ip, out)
		}
		vpn := isVPNDev(m[1])
		switch {
		case want == viaDirect && vpn:
			return fmt.Errorf("%s (%s) goes through VPN (%s): add %s to DOMAINS in ~/software/vpn/rus-routes.sh and run sudo ~/software/vpn/rus-routes.sh up",
				host, ip, m[1], host)
		case want == viaVPN && !vpn:
			return fmt.Errorf("%s (%s) goes direct (%s), Telegram needs the VPN: is Amnezia up?", host, ip, m[1])
		}
	}
	return nil
}
