// SPDX-License-Identifier: MIT

package executor

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/ankit373/hydra/internal/provider"
)

// Real end-to-end: a real curl subprocess, spawned with the exact env
// gateIfLocalOnly produces, actually attempting real HTTP calls. Not calling
// gateway.Allowed or the gateway's handler directly — this proves the whole
// chain a real LocalOnly head goes through, env rewrite included.
func TestLocalOnly_EndToEnd_RealSubprocessThroughGate(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("local-ok"))
	}))
	defer ts.Close()

	head := provider.Head{ID: "test-local", Provider: "test", Executable: "/bin/sh", LocalOnly: true}
	baseEnv := []string{"PATH=" + os.Getenv("PATH")}

	env, closeGate, err := gateIfLocalOnly(context.Background(), head, baseEnv)
	if err != nil {
		t.Fatalf("gateIfLocalOnly: %v", err)
	}
	defer closeGate()

	var sawProxy bool
	for _, kv := range env {
		if strings.HasPrefix(kv, "HTTP_PROXY=") {
			sawProxy = true
			t.Logf("env carries %s", kv)
		}
	}
	if !sawProxy {
		t.Fatal("HTTP_PROXY not present in the env a LocalOnly subprocess would actually run with")
	}

	script := fmt.Sprintf(`
set -e
LOCAL=$(curl -s -o /dev/null -w "%%{http_code}" %s)
echo "local_status=$LOCAL"
PUBLIC=$(curl -s -o /dev/null -w "%%{http_code}" -m 5 http://93.184.216.34/ || echo "curl_failed")
echo "public_status=$PUBLIC"
`, ts.URL)

	cmd := exec.Command("/bin/sh", "-c", script)
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	t.Logf("subprocess output:\n%s", out)
	if err != nil {
		t.Fatalf("subprocess failed: %v, output: %s", err, out)
	}

	outStr := string(out)
	if !strings.Contains(outStr, "local_status=200") {
		t.Errorf("real subprocess could not reach the local server through the gate: %s", outStr)
	}
	if strings.Contains(outStr, "public_status=200") {
		t.Errorf("real subprocess reached a public address through a LocalOnly gate: %s", outStr)
	}
	if !strings.Contains(outStr, "public_status=403") && !strings.Contains(outStr, "curl_failed") {
		t.Errorf("expected the public request denied (403) or refused by curl, got: %s", outStr)
	}
}

// Same proof, but through the real production entry point (CLIExecutor.Execute)
// rather than calling the internal helper directly: cliTemplates lookup,
// sandbox.Harden, gateIfLocalOnly wiring and cmd.Run(), exactly as a real
// dispatch would run it. The "continue" provider template runs the bare
// executable with the prompt on stdin, which /bin/sh happily treats as a
// script to run.
func TestLocalOnly_EndToEnd_ThroughRealExecutor(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("local-ok"))
	}))
	defer ts.Close()

	e := &CLIExecutor{}
	req := Request{
		Prompt: fmt.Sprintf(`
LOCAL=$(curl -s -o /dev/null -w "%%{http_code}" %s)
echo "local_status=$LOCAL"
PUBLIC=$(curl -s -o /dev/null -w "%%{http_code}" -m 5 http://93.184.216.34/ || echo "curl_failed")
echo "public_status=$PUBLIC"
`, ts.URL),
		Head: provider.Head{ID: "test-local-2", Provider: "continue", Executable: "/bin/sh", LocalOnly: true},
	}

	resp, err := e.Execute(context.Background(), req)
	if err != nil {
		t.Fatalf("CLIExecutor.Execute: %v", err)
	}
	t.Logf("real executor output:\n%s", resp.Output)

	if !strings.Contains(resp.Output, "local_status=200") {
		t.Errorf("real dispatch could not reach the local server through the gate: %s", resp.Output)
	}
	if strings.Contains(resp.Output, "public_status=200") {
		t.Errorf("real dispatch reached a public address through a LocalOnly gate: %s", resp.Output)
	}
}
