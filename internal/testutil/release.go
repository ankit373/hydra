// SPDX-License-Identifier: MIT

package testutil

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// FakeRelease serves a GitHub latest-release endpoint for tag and the given
// release files. It returns the API URL and the download base URL.
func FakeRelease(t testing.TB, tag string, files map[string][]byte) (api, download string) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/latest" {
			fmt.Fprintf(w, `{"tag_name":%q}`, tag)
			return
		}
		body, ok := files[strings.TrimPrefix(r.URL.Path, "/dl/"+tag+"/")]
		if !ok {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)
	return srv.URL + "/latest", srv.URL + "/dl"
}

// SumLine is one sha256sum-format line for body named name.
func SumLine(name string, body []byte) string {
	return fmt.Sprintf("%x  %s\n", sha256.Sum256(body), name)
}

// TarGz builds a .tar.gz holding one executable file.
func TarGz(t testing.TB, name string, body []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o755, Size: int64(len(body)), Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(body); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}
