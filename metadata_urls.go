package gobeyond

import (
	"net/url"
	"strings"
)

// ResolveURLs materializes root-relative metadata using this deployment's public
// origin. It copies collection fields so cached compiled metadata stays immutable.
// Absolute external URLs retain their authored meaning and normal validation.
func (m Metadata) ResolveURLs(publicOrigin string) Metadata {
	origin, err := url.Parse(publicOrigin)
	if err != nil || origin.Host == "" {
		return m
	}
	resolve := func(value string, image bool) string {
		if !strings.HasPrefix(value, "/") || strings.HasPrefix(value, "//") || strings.Contains(value, "\\") {
			return value
		}
		relative, err := url.Parse(value)
		if err != nil {
			return value
		}
		base := *origin
		if image {
			base.Scheme = "https"
		}
		return base.ResolveReference(relative).String()
	}
	m.Canonical = resolve(m.Canonical, false)
	m.OpenGraph.URL = resolve(m.OpenGraph.URL, false)
	if m.OpenGraph.Image != nil {
		copy := *m.OpenGraph.Image
		copy.URL = resolve(copy.URL, true)
		m.OpenGraph.Image = &copy
	}
	m.OpenGraph.Images = append([]string(nil), m.OpenGraph.Images...)
	for i, v := range m.OpenGraph.Images {
		m.OpenGraph.Images[i] = resolve(v, true)
	}
	m.Twitter.Images = append([]string(nil), m.Twitter.Images...)
	for i, v := range m.Twitter.Images {
		m.Twitter.Images[i] = resolve(v, true)
	}
	m.Alternates = append([]Alternate(nil), m.Alternates...)
	for i, v := range m.Alternates {
		m.Alternates[i].URL = resolve(v.URL, false)
	}
	return m
}
