// SPDX-License-Identifier: MIT

package sandbox

import (
	"os/exec"
	"reflect"
	"strings"
	"testing"
)

func TestOffline_WrapsInDenyNetworkProfile(t *testing.T) {
	cmd := exec.Command("/bin/echo", "a", "b")
	isolate(cmd)
	want := []string{sandboxExec, "-p", offlineProfile, "/bin/echo", "a", "b"}
	if cmd.Path != sandboxExec || !reflect.DeepEqual(cmd.Args, want) {
		t.Fatalf("got %q %q, want %q", cmd.Path, cmd.Args, want)
	}
	if !strings.Contains(offlineProfile, "(deny network*)") {
		t.Fatalf("profile %q does not deny the network", offlineProfile)
	}
}
