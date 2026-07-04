// Package stocking ingests the WDFW Fish Plants feed (Socrata dataset
// 6fex-3r7d) into lakes + stocking_events. geo_code is the lake spine; the
// Socrata :id (a content hash) is the idempotent dedup key.
package stocking

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/michaelpeterswa/washington-fish-api/internal/httpx"
)

const userAgent = "washington-fish-api/0.1 (+https://github.com/michaelpeterswa/washington-fish-api)"

// pageSize is the Socrata page size; the API caps a single response at 50000.
const pageSize = 1000

// Row is one Socrata stocking record. All Socrata JSON scalars arrive as
// strings, so parsing/typing happens in the mapping layer (stocking.go).
type Row struct {
	SocrataID        string `json:":id"`
	Agency           string `json:"agency"`
	Facility         string `json:"facility"`
	ReleaseStartDate string `json:"release_start_date"`
	ReleaseLocation  string `json:"release_location"`
	County           string `json:"county"`
	Elevation        string `json:"elevation"`
	GeoCode          string `json:"geo_code"`
	SpeciesType      string `json:"species_type"`
	Species          string `json:"species"`
	NumberReleased   string `json:"number_released"`
	Lifestage        string `json:"lifestage"`
}

// Client fetches rows from the Socrata feed.
type Client struct {
	baseURL  string
	appToken string
	http     *httpx.Client
}

// NewClient builds a stocking feed client. appToken may be empty (anonymous
// access works but is rate-limited).
func NewClient(baseURL, appToken string, logger *slog.Logger) *Client {
	return &Client{
		baseURL:  baseURL,
		appToken: appToken,
		http:     httpx.New(httpx.WithTimeout(60*time.Second), httpx.WithLogger(logger)),
	}
}

// FetchTroutSince returns all Trout/Kokanee rows released on or after `since`,
// paging through the whole result set. Ordered by :id for stable pagination.
func (c *Client) FetchTroutSince(ctx context.Context, since time.Time) ([]Row, error) {
	where := fmt.Sprintf("species_type='Trout/Kokanee' AND release_start_date >= '%s'",
		since.Format("2006-01-02"))

	var all []Row
	for offset := 0; ; offset += pageSize {
		page, err := c.fetchPage(ctx, where, offset)
		if err != nil {
			return nil, err
		}
		all = append(all, page...)
		if len(page) < pageSize {
			break
		}
	}
	return all, nil
}

func (c *Client) fetchPage(ctx context.Context, where string, offset int) ([]Row, error) {
	q := url.Values{}
	q.Set("$select", ":*, *") // :* = system fields (:id); * = all data columns
	q.Set("$where", where)
	q.Set("$order", ":id")
	q.Set("$limit", strconv.Itoa(pageSize))
	q.Set("$offset", strconv.Itoa(offset))

	reqURL := c.baseURL + "?" + q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, fmt.Errorf("stocking: build request: %w", err)
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "application/json")
	if c.appToken != "" {
		req.Header.Set("X-App-Token", c.appToken)
	}

	resp, err := c.http.Do(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("stocking: fetch (offset %d): %w", offset, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("stocking: unexpected status %d (offset %d): %s", resp.StatusCode, offset, body)
	}

	var rows []Row
	if err := json.NewDecoder(resp.Body).Decode(&rows); err != nil {
		return nil, fmt.Errorf("stocking: decode (offset %d): %w", offset, err)
	}
	return rows, nil
}
