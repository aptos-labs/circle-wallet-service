package handler

import (
	"fmt"
	"net"
	"net/url"
	"strings"
)

func ValidateWebhookURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("invalid URL: %w", err)
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return fmt.Errorf("webhook_url scheme must be http or https, got %q", u.Scheme)
	}
	host := u.Hostname()
	if host == "" {
		return fmt.Errorf("webhook_url must have a hostname")
	}
	if isPrivateHost(host) {
		return fmt.Errorf("webhook_url must not target private or loopback addresses")
	}
	return nil
}

func isPrivateHost(host string) bool {
	ip := net.ParseIP(host)
	if ip == nil {
		if strings.EqualFold(host, "localhost") {
			return true
		}
		ips, err := net.LookupIP(host)
		if err != nil || len(ips) == 0 {
			return false
		}
		ip = ips[0]
	}
	return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast()
}
