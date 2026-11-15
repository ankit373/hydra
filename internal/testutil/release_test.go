// SPDX-License-Identifier: MIT

package testutil

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"io"
	"net/http"
	"strings"
	"testing"
)

func get(t *testing.T, url string) (int, string) {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func TestFakeRelease_ServesTagAndFiles(t *testing.T) {
	api, dl := FakeRelease(t, "v1.2.3", map[string][]byte{"a.txt": []byte("hi")})
	if code, body := get(t, api); code != 200 || !strings.Contains(body, `"v1.2.3"`) {
		t.Errorf("latest = %d %q", code, body)
	}
	if code, body := get(t, dl+"/v1.2.3/a.txt"); code != 200 || body != "hi" {
		t.Errorf("file = %d %q", code, body)
	}
	if code, _ := get(t, dl+"/v1.2.3/missing"); code != 404 {
		t.Errorf("missing file = %d, want 404", code)
	}
}

func TestTarGzAndSumLine(t *testing.T) {
	gz, err := gzip.NewReader(bytes.NewReader(TarGz(t, "hyctl", []byte("x"))))
	if err != nil {
		t.Fatal(err)
	}
	h, err := tar.NewReader(gz).Next()
	if err != nil || h.Name != "hyctl" || h.Mode != 0o755 {
		t.Errorf("header = %+v, %v", h, err)
	}
	want := "2d711642b726b04401627ca9fbac32f5c8530fb1903cc4db02258717921a4881  x\n"
	if got := SumLine("x", []byte("x")); got != want {
		t.Errorf("SumLine = %q", got)
	}
}
