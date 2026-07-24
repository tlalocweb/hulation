package ddns

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
)

// Default public-IP detection endpoints, tried in order. Each returns the
// caller's public IP as a bare string. ipify + icanhazip are free and have
// family-specific hostnames so a v4 endpoint can't accidentally return a v6.
var (
	DefaultIPv4Services = []string{
		"https://api.ipify.org",
		"https://ipv4.icanhazip.com",
	}
	DefaultIPv6Services = []string{
		"https://api6.ipify.org",
		"https://ipv6.icanhazip.com",
	}
)

// DetectPublicIPv4 returns the host's public IPv4 using the given services (or
// DefaultIPv4Services when empty), trying each in order and returning the first
// response that parses as an IPv4 address. Non-200s, network errors, garbage
// bodies, and wrong-family answers are skipped. An error is returned only when
// every service fails.
func DetectPublicIPv4(ctx context.Context, client *http.Client, services []string) (string, error) {
	if len(services) == 0 {
		services = DefaultIPv4Services
	}
	return detectPublicIP(ctx, client, services, false)
}

// DetectPublicIPv6 is DetectPublicIPv4's IPv6 counterpart.
func DetectPublicIPv6(ctx context.Context, client *http.Client, services []string) (string, error) {
	if len(services) == 0 {
		services = DefaultIPv6Services
	}
	return detectPublicIP(ctx, client, services, true)
}

// detectPublicIP walks services in order, returning the first body that parses
// as an IP of the requested family. wantV6 selects AAAA (IPv6) vs A (IPv4).
func detectPublicIP(ctx context.Context, client *http.Client, services []string, wantV6 bool) (string, error) {
	if client == nil {
		client = http.DefaultClient
	}
	family := "IPv4"
	if wantV6 {
		family = "IPv6"
	}
	var errs []string
	for _, svc := range services {
		ip, err := fetchIP(ctx, client, svc, wantV6)
		if err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", svc, err))
			continue
		}
		return ip, nil
	}
	return "", fmt.Errorf("ddns: no %s service returned a valid address (%s)", family, strings.Join(errs, "; "))
}

// fetchIP GETs one service and validates the body is an IP of the wanted family.
func fetchIP(ctx context.Context, client *http.Client, svc string, wantV6 bool) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, svc, nil)
	if err != nil {
		return "", fmt.Errorf("request: %w", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 128))
	if err != nil {
		return "", fmt.Errorf("read body: %w", err)
	}
	s := strings.TrimSpace(string(body))
	ip := net.ParseIP(s)
	if ip == nil {
		return "", fmt.Errorf("not an IP: %q", s)
	}
	isV4 := ip.To4() != nil
	if wantV6 && isV4 {
		return "", fmt.Errorf("wanted IPv6 but got IPv4 %q", s)
	}
	if !wantV6 && !isV4 {
		return "", fmt.Errorf("wanted IPv4 but got IPv6 %q", s)
	}
	// Normalise to canonical form (e.g. lower-cased/compressed v6).
	return ip.String(), nil
}
