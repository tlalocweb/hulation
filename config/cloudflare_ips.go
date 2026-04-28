package config

import (
	_ "embed"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/tlalocweb/hulation/log"
	"gopkg.in/yaml.v2"
)

//go:embed cloudflare_ips_default.yaml
var defaultCloudflareIPsYAML []byte

// cloudflareIPsFile is the YAML structure for Cloudflare IP ranges.
type cloudflareIPsFile struct {
	IPv4 []string `yaml:"ipv4"`
	IPv6 []string `yaml:"ipv6"`
}

// CloudflareIPRanges holds parsed Cloudflare CIDR ranges for fast IP membership checks.
type CloudflareIPRanges struct {
	ranges []*net.IPNet
}

// Contains returns true if the given IP falls within any Cloudflare CIDR range.
func (c *CloudflareIPRanges) Contains(ip net.IP) bool {
	if ip == nil {
		return false
	}
	for _, cidr := range c.ranges {
		if cidr.Contains(ip) {
			return true
		}
	}
	return false
}

// ContainsString parses the IP string and checks membership.
func (c *CloudflareIPRanges) ContainsString(ipStr string) bool {
	// Strip port if present
	host, _, err := net.SplitHostPort(ipStr)
	if err != nil {
		host = ipStr
	}
	return c.Contains(net.ParseIP(host))
}

// Ranges returns the underlying []*net.IPNet for use by badactor CIDR allowlist.
func (c *CloudflareIPRanges) Ranges() []*net.IPNet {
	return c.ranges
}

// LoadCloudflareIPs loads Cloudflare IP ranges with 3-tier fallback:
//  1. Fetch live from https://www.cloudflare.com/ips-v4 + ips-v6
//  2. If fetch fails, read cached {cacheDir}/cloudflare_ips.yaml
//  3. If no cache, use embedded defaults
func LoadCloudflareIPs(cacheDir string) (*CloudflareIPRanges, error) {
	cachePath := ""
	if cacheDir != "" {
		cachePath = filepath.Join(cacheDir, "cloudflare_ips.yaml")
	}

	// Tier 1: fetch live
	ranges, err := fetchCloudflareIPs()
	if err == nil {
		log.Infof("cloudflare_ips: fetched %d live ranges from cloudflare.com", len(ranges.ranges))
		if cachePath != "" {
			if werr := writeIPCache(cachePath, ranges); werr != nil {
				log.Warnf("cloudflare_ips: failed to write cache: %s", werr)
			}
		}
		return ranges, nil
	}
	log.Warnf("cloudflare_ips: live fetch failed: %s", err)

	// Tier 2: cached file
	if cachePath != "" {
		ranges, err = loadIPCache(cachePath)
		if err == nil {
			log.Infof("cloudflare_ips: using cached ranges from %s (%d ranges)", cachePath, len(ranges.ranges))
			return ranges, nil
		}
		log.Debugf("cloudflare_ips: no cache at %s: %s", cachePath, err)
	}

	// Tier 3: embedded defaults
	ranges, err = parseCloudflareIPsYAML(defaultCloudflareIPsYAML)
	if err != nil {
		return nil, fmt.Errorf("cloudflare_ips: failed to parse embedded defaults: %w", err)
	}
	log.Infof("cloudflare_ips: using hardcoded defaults (%d ranges)", len(ranges.ranges))
	return ranges, nil
}

// --- Fetch ---

func fetchCloudflareIPs() (*CloudflareIPRanges, error) {
	client := &http.Client{Timeout: 10 * time.Second}

	v4, err := fetchURL(client, "https://www.cloudflare.com/ips-v4")
	if err != nil {
		return nil, fmt.Errorf("fetch ips-v4: %w", err)
	}
	v6, err := fetchURL(client, "https://www.cloudflare.com/ips-v6")
	if err != nil {
		return nil, fmt.Errorf("fetch ips-v6: %w", err)
	}

	cidrs := append(parseCIDRLines(v4), parseCIDRLines(v6)...)
	return parseCIDRStrings(cidrs)
}

func fetchURL(client *http.Client, url string) (string, error) {
	resp, err := client.Get(url)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("HTTP %d from %s", resp.StatusCode, url)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	return string(body), nil
}

func parseCIDRLines(text string) []string {
	var cidrs []string
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line != "" && !strings.HasPrefix(line, "#") {
			cidrs = append(cidrs, line)
		}
	}
	return cidrs
}

// --- Cache read/write ---

func writeIPCache(path string, ranges *CloudflareIPRanges) error {
	var v4, v6 []string
	for _, cidr := range ranges.ranges {
		s := cidr.String()
		if cidr.IP.To4() != nil {
			v4 = append(v4, s)
		} else {
			v6 = append(v6, s)
		}
	}
	data := cloudflareIPsFile{IPv4: v4, IPv6: v6}
	out, err := yaml.Marshal(&data)
	if err != nil {
		return err
	}
	header := fmt.Sprintf("# Cloudflare IP ranges — auto-fetched %s\n", time.Now().UTC().Format(time.RFC3339))
	return os.WriteFile(path, append([]byte(header), out...), 0644)
}

func loadIPCache(path string) (*CloudflareIPRanges, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return parseCloudflareIPsYAML(data)
}

// --- YAML parsing ---

func parseCloudflareIPsYAML(data []byte) (*CloudflareIPRanges, error) {
	var file cloudflareIPsFile
	if err := yaml.Unmarshal(data, &file); err != nil {
		return nil, err
	}
	cidrs := append(file.IPv4, file.IPv6...)
	return parseCIDRStrings(cidrs)
}

func parseCIDRStrings(cidrs []string) (*CloudflareIPRanges, error) {
	var nets []*net.IPNet
	for _, s := range cidrs {
		_, ipnet, err := net.ParseCIDR(strings.TrimSpace(s))
		if err != nil {
			return nil, fmt.Errorf("invalid CIDR %q: %w", s, err)
		}
		nets = append(nets, ipnet)
	}
	if len(nets) == 0 {
		return nil, fmt.Errorf("no CIDR ranges parsed")
	}
	return &CloudflareIPRanges{ranges: nets}, nil
}
