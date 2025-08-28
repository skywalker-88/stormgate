package rl

import (
	"strings"

	cfg "github.com/skywalker-88/stormgate/pkg/config"
)

// EffectiveLimit returns the per-route limit with fallback to the default.
func EffectiveLimit(c *cfg.Config, route string) cfg.Limit {
	if c == nil {
		return cfg.Limit{}
	}
	if l, ok := c.Limits.Routes[route]; ok {
		return l
	}
	return c.Limits.Default
}

func EffectiveGlobalClientLimit(c *cfg.Config) (cfg.Limit, bool) {
	if c == nil {
		return cfg.Limit{}, false
	}
	l := c.Limits.GlobalClient
	enabled := l.RPS > 0 || l.Burst > 0
	return l, enabled
}

// IsAllowlisted returns true if the clientID is allowlisted for mitigation.
// Supported patterns in config:
//   - exact match: "1.2.3.4" or "partner-key-abc"
//   - wildcard all: "*"
//   - prefix match: "partner-*" (matches any clientID starting with "partner-")
func IsAllowlisted(c *cfg.Config, clientID string) bool {
	if c == nil {
		return false
	}
	alist := c.Mitigation.Allowlist.Clients
	for _, pat := range alist {
		switch {
		case pat == clientID:
			return true
		case pat == "*":
			return true
		case strings.HasSuffix(pat, "*") && strings.HasPrefix(clientID, strings.TrimSuffix(pat, "*")):
			return true
		}
	}
	return false
}

func NormalizeRoute(c *cfg.Config, path string) string {
	if c == nil {
		return path
	}
	// exact match fast path
	if _, ok := c.Limits.Routes[path]; ok {
		return path
	}
	// longest prefix among configured routes
	longest := ""
	for r := range c.Limits.Routes {
		if r == "" || r[0] != '/' {
			continue
		}
		if strings.HasPrefix(path, r) && len(r) > len(longest) {
			longest = r
		}
	}
	if longest != "" {
		return longest
	}
	return path
}
