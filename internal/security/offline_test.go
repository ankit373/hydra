// SPDX-License-Identifier: MIT

package security

import (
	"fmt"
	"strings"
	"testing"

	"github.com/ankit373/hydra/internal/sandbox"
)

func TestOfflineCheck(t *testing.T) {
	refused := OfflineCheck(fmt.Errorf("%w: user namespaces are off", sandbox.ErrNoNetworkIsolation))
	if refused.Status != "not available" || !strings.Contains(refused.Detail, "not checked") ||
		!strings.Contains(refused.Detail, "user namespaces are off") {
		t.Errorf("refused = %+v, want not available with the reason and not checked", refused)
	}
	if ok := OfflineCheck(nil); ok.Status != "network off" || !strings.Contains(ok.Detail, "read and write your files") {
		t.Errorf("available = %+v, want network off and the file-access limit", ok)
	}
}
