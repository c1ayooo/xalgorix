package assets

import (
	"net/url"
	"strings"
)

// InputsFromURL maps one observed URL or bare path/host to the asset
// identities it proves. It never invents a host for a bare path and never
// fabricates a path for a bare host.
func InputsFromURL(projectID, raw string) []AssetInput {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	out := make([]AssetInput, 0, 2)
	if strings.Contains(raw, "://") {
		if u, err := url.Parse(raw); err == nil && u.Host != "" {
			host := strings.ToLower(u.Hostname())
			if host != "" {
				out = append(out, AssetInput{
					ProjectID: projectID, Kind: hostKind(host), Key: host, DisplayName: host,
				})
			}
			if p := strings.TrimSpace(u.Path); p != "" && p != "/" {
				out = append(out, AssetInput{
					ProjectID: projectID, Kind: KindEndpoint, Key: p, DisplayName: p,
				})
			}
			return out
		}
	}
	if strings.HasPrefix(raw, "/") {
		return append(out, AssetInput{
			ProjectID: projectID, Kind: KindEndpoint, Key: raw, DisplayName: raw,
		})
	}
	// A bare host is a subdomain/root-domain identity; do not fabricate a path.
	if !strings.ContainsAny(raw, " \t?=") && strings.Contains(raw, ".") {
		return append(out, AssetInput{
			ProjectID: projectID, Kind: hostKind(raw), Key: raw, DisplayName: raw,
		})
	}
	return nil
}

func hostKind(host string) Kind {
	if strings.Count(strings.TrimSuffix(host, "."), ".") <= 1 {
		return KindRootDomain
	}
	return KindSubdomain
}
