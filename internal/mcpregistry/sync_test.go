// SPDX-License-Identifier: MIT

package mcpregistry

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
)

func withFakeRegistry(t *testing.T, pages [][]ServerRecord) {
	t.Helper()
	page := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if page >= len(pages) {
			_ = json.NewEncoder(w).Encode(serverListResponse{})
			return
		}
		items := make([]serverListItem, len(pages[page]))
		for i, s := range pages[page] {
			items[i] = serverListItem{Server: s}
		}
		resp := serverListResponse{Servers: items}
		page++
		if page < len(pages) {
			resp.Metadata.NextCursor = "next"
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	t.Cleanup(srv.Close)

	origURL := registryServersURL
	registryServersURL = srv.URL
	t.Cleanup(func() { registryServersURL = origURL })
}

func withTempHydraHome(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("HYDRA_HOME", dir)
}

func TestSync_SinglePage(t *testing.T) {
	withTempHydraHome(t)
	withFakeRegistry(t, [][]ServerRecord{
		{{Name: "io.github.foo/bar", Packages: []Package{{RegistryType: "npm", Identifier: "bar-mcp"}}}},
	})

	n, err := Sync(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("Sync() = %d, want 1", n)
	}

	cache, err := loadCache()
	if err != nil {
		t.Fatal(err)
	}
	if len(cache.Servers) != 1 || cache.Servers[0].Name != "io.github.foo/bar" {
		t.Errorf("unexpected cache contents: %+v", cache.Servers)
	}
}

func TestSync_FollowsPagination(t *testing.T) {
	withTempHydraHome(t)
	withFakeRegistry(t, [][]ServerRecord{
		{{Name: "a"}},
		{{Name: "b"}},
		{{Name: "c"}},
	})

	n, err := Sync(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if n != 3 {
		t.Fatalf("Sync() = %d, want 3 (pagination should be followed to the end)", n)
	}
}

func TestSync_ReportsProgressPerPage(t *testing.T) {
	withTempHydraHome(t)
	withFakeRegistry(t, [][]ServerRecord{
		{{Name: "a"}},
		{{Name: "b"}, {Name: "c"}},
	})

	var pages []int
	var counts []int
	_, err := Sync(context.Background(), func(page, soFar int) {
		pages = append(pages, page)
		counts = append(counts, soFar)
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(pages) != 2 || pages[0] != 1 || pages[1] != 2 {
		t.Errorf("pages = %v, want [1 2]", pages)
	}
	if len(counts) != 2 || counts[0] != 1 || counts[1] != 3 {
		t.Errorf("counts = %v, want [1 3] (cumulative)", counts)
	}
}

func TestSync_HTTPErrorPropagates(t *testing.T) {
	withTempHydraHome(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	origURL := registryServersURL
	registryServersURL = srv.URL
	defer func() { registryServersURL = origURL }()

	if _, err := Sync(context.Background(), nil); err == nil {
		t.Fatal("expected an error from a 500 response")
	}
}

func TestWriteCache_CreatesParentDir(t *testing.T) {
	withTempHydraHome(t)
	if err := writeCache(&registryCache{Servers: []ServerRecord{{Name: "x"}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := loadCache(); err != nil {
		t.Fatalf("loadCache after writeCache: %v", err)
	}
}

func TestCachePath_IsUnderHydraHome(t *testing.T) {
	withTempHydraHome(t)
	got := cachePath()
	want := "mcp_registry_cache.json"
	if filepath.Base(got) != want {
		t.Errorf("cachePath() base = %q, want %q", filepath.Base(got), want)
	}
}
