// Package wdfwlakes crawls WDFW's lowland-lake pages, which expose (unlike the
// spatial layers) the geo_code alongside coordinates/acreage/elevation. That
// makes an EXACT geo_code join to the stocking-seeded lakes possible — no fuzzy
// name matching.
package wdfwlakes

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"log/slog"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/michaelpeterswa/washington-fish-api/internal/httpx"
)

// errNotFound signals a 404 — for the sitemap it means we've walked past the
// last page; for a lake page it means the page is gone (skip it).
var errNotFound = errors.New("wdfwlakes: not found")

const userAgent = "washington-fish-api/0.1 (+https://github.com/michaelpeterswa/washington-fish-api)"

// lakePathRe matches lowland- and high-lake page URLs in the sitemap; group 1
// is the section (lowland-lakes | high-lakes), used to tag lake_type.
var lakePathRe = regexp.MustCompile(`https://wdfw\.wa\.gov/fishing/locations/(lowland-lakes|high-lakes)/[a-z0-9\-]+`)

// Field extractors against the server-rendered page. Markup is
// `<strong>Label:</strong> value<br>`; geo_code also appears as JSON "geo_id".
var (
	locRe     = regexp.MustCompile(`<loc>([^<]+)</loc>`)
	geoCodeRe = regexp.MustCompile(`(?i)Geo Code:</strong>\s*([A-Za-z0-9]+)`)
	geoIDRe   = regexp.MustCompile(`"geo_id"\s*:\s*"([^"]+)"`)
	centerRe  = regexp.MustCompile(`(?i)Center:</strong>\s*(-?\d+\.\d+),\s*(-?\d+\.\d+)`)
	countyRe  = regexp.MustCompile(`(?i)County:</strong>\s*([^<]+?)\s*<`)
	acresRe   = regexp.MustCompile(`(?i)Acreage:</strong>\s*([\d.]+)\s*ac`)
	elevRe    = regexp.MustCompile(`(?i)Elevation:</strong>\s*([\d.]+)\s*ft`)
	titleRe   = regexp.MustCompile(`(?is)<title>\s*(.*?)\s*</title>`)
)

// LakePage is the data extracted from one lake page.
type LakePage struct {
	URL      string
	GeoCode  string
	Name     string
	County   string
	LakeType string // "lowland" | "high", from the URL section
	Lat      float64
	Lon      float64
	Acres    float64 // 0 if absent
	ElevFt   float64 // 0 if absent
}

// Client fetches the sitemap and individual lake pages.
type Client struct {
	sitemapURL string
	http       *httpx.Client
}

func NewClient(sitemapURL string, logger *slog.Logger) *Client {
	return &Client{
		sitemapURL: sitemapURL,
		http:       httpx.New(httpx.WithTimeout(30*time.Second), httpx.WithLogger(logger)),
	}
}

// LakeURLs enumerates all lowland-lake page URLs across the paginated sitemap.
// It walks sitemap.xml?page=1.. until a page yields no <loc> entries.
func (c *Client) LakeURLs(ctx context.Context) ([]string, error) {
	seen := make(map[string]struct{})
	var urls []string
	const maxSitemapPages = 50 // safety cap; the real sitemap is a handful of pages
	for page := 1; page <= maxSitemapPages; page++ {
		body, err := c.get(ctx, fmt.Sprintf("%s?page=%d", c.sitemapURL, page))
		if errors.Is(err, errNotFound) {
			break // walked past the last sitemap page (WDFW 404s beyond the end)
		}
		if err != nil {
			return nil, err
		}
		locs := locRe.FindAllStringSubmatch(body, -1)
		if len(locs) == 0 {
			break // empty page
		}
		for _, m := range locs {
			u := lakePathRe.FindString(m[1])
			// Skip the section index itself (…/lowland-lakes with no slug).
			if u == "" || strings.HasSuffix(u, "/lowland-lakes") {
				continue
			}
			if _, ok := seen[u]; ok {
				continue
			}
			seen[u] = struct{}{}
			urls = append(urls, u)
		}
	}
	return urls, nil
}

// FetchLake fetches and parses one lake page. Returns nil (no error) if the
// page has neither a geo_code nor coordinates — not every page is a real lake.
func (c *Client) FetchLake(ctx context.Context, url string) (*LakePage, error) {
	body, err := c.get(ctx, url)
	if errors.Is(err, errNotFound) {
		return nil, nil // page gone; skip
	}
	if err != nil {
		return nil, err
	}

	geo := firstGroup(geoCodeRe, body)
	if geo == "" {
		geo = firstGroup(geoIDRe, body)
	}
	center := centerRe.FindStringSubmatch(body)
	if geo == "" || center == nil {
		return nil, nil // not a parseable lake page
	}
	lat, err1 := strconv.ParseFloat(center[1], 64)
	lon, err2 := strconv.ParseFloat(center[2], 64)
	if err1 != nil || err2 != nil {
		return nil, nil
	}

	p := &LakePage{
		URL:      url,
		GeoCode:  strings.ToUpper(strings.TrimSpace(geo)),
		Name:     lakeName(body),
		County:   strings.TrimSpace(firstGroup(countyRe, body)),
		LakeType: lakeTypeFromURL(url),
		Lat:      lat,
		Lon:      lon,
		Acres:    parseFloat(firstGroup(acresRe, body)),
		ElevFt:   parseFloat(firstGroup(elevRe, body)),
	}
	return p, nil
}

func (c *Client) get(ctx context.Context, url string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", userAgent)
	resp, err := c.http.Do(ctx, req)
	if err != nil {
		return "", fmt.Errorf("wdfwlakes: get %s: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return "", errNotFound
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("wdfwlakes: get %s: status %d", url, resp.StatusCode)
	}
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("wdfwlakes: read %s: %w", url, err)
	}
	return string(b), nil
}

// lakeTypeFromURL classifies a lake by which WDFW section its page lives in.
func lakeTypeFromURL(url string) string {
	switch {
	case strings.Contains(url, "/high-lakes/"):
		return "high"
	case strings.Contains(url, "/lowland-lakes/"):
		return "lowland"
	default:
		return ""
	}
}

// lakeName pulls the page title ("Lake Washington | Washington Department…").
func lakeName(body string) string {
	m := titleRe.FindStringSubmatch(body)
	if m == nil {
		return ""
	}
	name := m[1]
	if i := strings.Index(name, "|"); i >= 0 {
		name = name[:i]
	}
	return strings.TrimSpace(name)
}

func firstGroup(re *regexp.Regexp, s string) string {
	m := re.FindStringSubmatch(s)
	if m == nil {
		return ""
	}
	return m[1]
}

func parseFloat(s string) float64 {
	f, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil {
		return 0
	}
	return f
}
