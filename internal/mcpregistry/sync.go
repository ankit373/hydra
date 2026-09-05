// SPDX-License-Identifier: MIT

package mcpregistry

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

// registryServersURL is a var so tests can override it with httptest.Server.
var registryServersURL = "https://registry.modelcontextprotocol.io/v0/servers"

var httpClient = &http.Client{Timeout: 15 * time.Second}

// maxResponseBytes caps a single page's response body.
const maxResponseBytes = 10 << 20

// maxPages bounds a sync run so a misbehaving server (an infinite cursor
// loop) can't hang hyctl forever. At 100/page this covers 500k servers,
// several times the entire known registry.
const maxPages = 5000

type serverListItem struct {
	Server ServerRecord `json:"server"`
}

type serverListResponse struct {
	Servers  []serverListItem `json:"servers"`
	Metadata struct {
		NextCursor string `json:"nextCursor"`
	} `json:"metadata"`
}

// Sync fetches the full official MCP registry (paginated) and writes it to
// the local cache. Returns the number of servers written. onProgress, if
// non-nil, is called after every page, the full registry is thousands of
// servers over dozens of sequential requests, easily a minute or two, and a
// caller driving a CLI needs that to show something rather than sit silent.
func Sync(ctx context.Context, onProgress func(page, serversSoFar int)) (int, error) {
	var servers []ServerRecord
	cursor := ""

	for page := 1; page <= maxPages; page++ {
		url := registryServersURL + "?limit=100"
		if cursor != "" {
			url += "&cursor=" + cursor
		}

		resp, err := fetchPage(ctx, url)
		if err != nil {
			return 0, err
		}
		for _, item := range resp.Servers {
			servers = append(servers, item.Server)
		}
		if onProgress != nil {
			onProgress(page, len(servers))
		}
		if resp.Metadata.NextCursor == "" {
			break
		}
		cursor = resp.Metadata.NextCursor
	}

	if err := writeCache(&registryCache{FetchedAt: time.Now().UTC(), Servers: servers}); err != nil {
		return 0, err
	}
	return len(servers), nil
}

func fetchPage(ctx context.Context, url string) (*serverListResponse, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "hydra/1 (+https://github.com/ankit373/hydra)")

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("mcp registry fetch: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("mcp registry fetch: HTTP %d", resp.StatusCode)
	}

	limited := io.LimitReader(resp.Body, maxResponseBytes+1)
	var out serverListResponse
	if err := json.NewDecoder(limited).Decode(&out); err != nil {
		return nil, fmt.Errorf("mcp registry parse: %w", err)
	}
	return &out, nil
}

// LoadCache reads the local sync cache. Returns an error wrapping
// os.ErrNotExist-checkable state when sync has never run.
func loadCache() (*registryCache, error) {
	raw, err := os.ReadFile(cachePath())
	if err != nil {
		return nil, err
	}
	var c registryCache
	if err := json.Unmarshal(raw, &c); err != nil {
		return nil, fmt.Errorf("mcp registry cache corrupt: %w", err)
	}
	return &c, nil
}

func writeCache(c *registryCache) error {
	raw, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	path := cachePath()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	defer func() { _ = os.Remove(tmp) }()

	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return os.WriteFile(path, raw, 0o600)
	}
	return nil
}
