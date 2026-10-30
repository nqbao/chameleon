package chameleon

import (
	"net"
	"net/url"
	"regexp"
	"strings"
)

var mcpURLVariable = regexp.MustCompile(`\$\{[A-Za-z_][A-Za-z0-9_]*\}`)

// dockerMCPURL changes only explicit loopback or unspecified hosts in HTTP MCP URLs. It does
// not resolve DNS or expand variables, and leaves invalid URLs to the runtime.
func dockerMCPURL(raw string) string {
	scheme, _, ok := strings.Cut(raw, "://")
	if !ok || (!strings.EqualFold(scheme, "http") && !strings.EqualFold(scheme, "https")) {
		return raw
	}
	authorityStart := len(scheme) + len("://")
	authorityEnd := len(raw)
	if i := strings.IndexAny(raw[authorityStart:], "/?#"); i >= 0 {
		authorityEnd = authorityStart + i
	}
	hostStart := authorityStart + strings.LastIndex(raw[authorityStart:authorityEnd], "@") + 1
	// Validate using a copy with port variables replaced by digits. net/url
	// rejects template ports even though the agent can expand them later.
	parseRaw := raw
	if colon := strings.LastIndexByte(raw[hostStart:authorityEnd], ':'); colon >= 0 {
		portStart := hostStart + colon + 1
		port := raw[portStart:authorityEnd]
		// A closing bracket means this colon belongs to an IPv6 address.
		if !strings.Contains(port, "]") {
			parseRaw = raw[:portStart] + mcpURLVariable.ReplaceAllString(port, "0") + raw[authorityEnd:]
		}
	}
	u, err := url.Parse(parseRaw)
	if err != nil {
		return raw
	}
	host := u.Hostname()
	ip := net.ParseIP(host)
	if !strings.EqualFold(strings.TrimSuffix(host, "."), "localhost") && (ip == nil || (!ip.IsLoopback() && !ip.IsUnspecified())) {
		return raw
	}
	// Replace only the original hostname: URL.String would escape path
	// characters, including runtime ${VAR} references.
	hostEnd := hostStart + len(host)
	if raw[hostStart] == '[' {
		hostEnd += 2 // include the IPv6 brackets
	}
	return raw[:hostStart] + "host.docker.internal" + raw[hostEnd:]
}
