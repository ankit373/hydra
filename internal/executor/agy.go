// SPDX-License-Identifier: MIT

package executor

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/ankit373/hydra/internal/config"
	"github.com/ankit373/hydra/internal/util"
)

// AuthRequiredError is returned when agy signals that authentication is needed.
type AuthRequiredError struct {
	ModelFlag string
	Pool      string
	AuthURL   string
}

func (e *AuthRequiredError) Error() string {
	msg := fmt.Sprintf("auth required for agy model %q (pool %q)", e.ModelFlag, e.Pool)
	if e.AuthURL != "" {
		msg += " — authenticate at: " + e.AuthURL
	}
	return msg
}

var authSignalRe = regexp.MustCompile(
	`(?i)(not\s+(logged|authenticated|authorized)|please\s+(log\s*in|sign\s*in|authenticate)|login\s+required|auth(entication)?\s+required|visit\s+https?://accounts\.google\.com|antigravity\.google/auth|sign\s+in\s+to\s+continue)`,
)
var authURLRe = regexp.MustCompile(`https?://\S+`)

// agyMu serializes all agy executions that mutate settings.json.
// agy uses a single shared settings.json; concurrent swaps corrupt it.
// Serialization eliminates the filesystem race at the cost of agy parallelism.
var agyMu sync.Mutex

// AgyExecutor invokes `agy --print` with model selection via settings.json swap.
// Ports all logic from dispatch/agy.sh natively in Go.
type AgyExecutor struct{}

func (e *AgyExecutor) Execute(ctx context.Context, req Request) (*Response, error) {
	modelFlag := req.Head.Meta["model_flag"]
	if modelFlag == "" {
		return nil, fmt.Errorf("agy executor: head %q has no model_flag in Meta", req.Head.ID)
	}

	// Serialize all agy invocations — settings.json is a single shared file.
	// Concurrent swaps corrupt it, making swarm mode produce wrong models.
	agyMu.Lock()
	defer agyMu.Unlock()

	settingsPath := agySitesPath()

	// Recover from a prior SIGKILL that left settings.json in a swapped state.
	recoverAgySwap(settingsPath)

	originalModel, err := swapAgyModel(settingsPath, modelFlag)
	if err != nil {
		// Settings file might not exist yet — proceed without swapping.
		originalModel = ""
		settingsPath = ""
	}
	if settingsPath != "" {
		defer restoreAgyModel(settingsPath, originalModel)
	}

	timeout := 300 * time.Second
	if t := os.Getenv("AGY_TIMEOUT"); t != "" {
		if d, err := time.ParseDuration(t); err == nil {
			timeout = d
		} else if d, err := time.ParseDuration(t + "s"); err == nil {
			// Bare integer seconds (e.g. "300") — treat as seconds.
			timeout = d
		}
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// stderr is only used for auth detection — cap it at 64 KB so a runaway
	// process can't exhaust memory through error output.
	stderr := util.NewAccumulator(64 << 10)
	stdout := util.NewAccumulator(0)
	cmd := exec.CommandContext(ctx, "agy", "--print", req.Prompt, "--print-timeout", fmt.Sprintf("%ds", int(timeout.Seconds())))
	cmd.Stdout = stdout
	cmd.Stderr = stderr

	start := time.Now()
	runErr := cmd.Run()
	duration := time.Since(start)

	stderrStr := stderr.String()
	outStr := stdout.String()

	// Auth detection: check stderr + first 3 lines of stdout only.
	// Never scan full output — model responses may contain auth strings.
	firstLines := strings.Join(strings.SplitN(outStr, "\n", 4)[:min(strings.Count(outStr, "\n")+1, 3)], "\n")
	authSignal := stderrStr + "\n" + firstLines

	if authSignalRe.MatchString(authSignal) {
		authURL := ""
		if m := authURLRe.FindString(authSignal); m != "" {
			authURL = m
		}
		writeAuthRequired(req.Head.Meta["token_pool"], modelFlag, authURL)
		return nil, &AuthRequiredError{
			ModelFlag: modelFlag,
			Pool:      req.Head.Meta["token_pool"],
			AuthURL:   authURL,
		}
	}

	if runErr != nil {
		return nil, fmt.Errorf("agy exec %s: %w — %s", req.Head.ID, runErr, strings.TrimSpace(stderrStr))
	}

	output := strings.TrimSpace(outStr)

	promptTokens := len(req.Prompt) / 4
	responseTokens := len(output) / 4
	writeTokenSidecar(modelFlag, "agy", "estimate", promptTokens, responseTokens)

	return &Response{
		Output:       output,
		Duration:     duration,
		Model:        req.Head.ID,
		InputTokens:  promptTokens,
		OutputTokens: responseTokens,
		Truncated:    stdout.Truncated(),
		// agy does not report token usage — these are char/4 estimates.
		TokensEstimated: true,
	}, nil
}

// agySitesPath returns the path to agy's settings.json.
func agySitesPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".gemini", "antigravity-cli", "settings.json")
}

// agyOrigSuffix is the sentinel backup written before any settings.json swap.
// If the process is SIGKILLed mid-swap, recoverAgySwap restores it on next run.
const agyOrigSuffix = ".hydra-orig"

// swapAgyModel writes modelFlag into settings.json and returns the original model.
// It writes a sentinel backup first so recoverAgySwap() can undo a partial swap
// caused by SIGKILL between os.Rename and the defer restoreAgyModel.
func swapAgyModel(settingsPath, modelFlag string) (string, error) {
	raw, err := os.ReadFile(settingsPath)
	if err != nil {
		return "", err
	}
	// Write sentinel before any mutation — must survive SIGKILL.
	if err := os.WriteFile(settingsPath+agyOrigSuffix, raw, 0o600); err != nil {
		return "", err
	}
	var m map[string]interface{}
	if err := json.Unmarshal(raw, &m); err != nil {
		_ = os.Remove(settingsPath + agyOrigSuffix)
		return "", err
	}
	original, _ := m["model"].(string)
	m["model"] = modelFlag
	updated, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		_ = os.Remove(settingsPath + agyOrigSuffix)
		return "", err
	}
	tmp := settingsPath + ".hydra-tmp"
	if err := os.WriteFile(tmp, updated, 0o600); err != nil {
		_ = os.Remove(settingsPath + agyOrigSuffix)
		return "", err
	}
	if err := os.Rename(tmp, settingsPath); err != nil {
		_ = os.Remove(settingsPath + agyOrigSuffix)
		_ = os.Remove(tmp)
		return "", err
	}
	return original, nil
}

// restoreAgyModel writes back the original model and removes the sentinel.
func restoreAgyModel(settingsPath, originalModel string) {
	raw, err := os.ReadFile(settingsPath)
	if err != nil {
		return
	}
	var m map[string]interface{}
	if err := json.Unmarshal(raw, &m); err != nil {
		return
	}
	if originalModel == "" {
		delete(m, "model")
	} else {
		m["model"] = originalModel
	}
	updated, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return
	}
	tmp := settingsPath + ".hydra-tmp"
	if err := os.WriteFile(tmp, updated, 0o600); err != nil {
		return
	}
	if err := os.Rename(tmp, settingsPath); err == nil {
		_ = os.Remove(settingsPath + agyOrigSuffix)
	}
}

// recoverAgySwap detects a stale sentinel left by a prior SIGKILL and restores
// settings.json to its pre-swap state. No-ops when no sentinel exists.
func recoverAgySwap(settingsPath string) {
	orig, err := os.ReadFile(settingsPath + agyOrigSuffix)
	if err != nil {
		return
	}
	tmp := settingsPath + ".hydra-tmp"
	if err := os.WriteFile(tmp, orig, 0o600); err != nil {
		return
	}
	if err := os.Rename(tmp, settingsPath); err != nil {
		_ = os.Remove(tmp)
		return
	}
	_ = os.Remove(settingsPath + agyOrigSuffix)
}

// writeAuthRequired writes logs/auth_required.json so the TUI can surface it.
func writeAuthRequired(pool, modelFlag, authURL string) {
	type authEntry struct {
		Pool      string `json:"pool"`
		Model     string `json:"model"`
		AuthURL   string `json:"auth_url"`
		DetectedAt string `json:"detected_at"`
	}
	entry := authEntry{
		Pool:      pool,
		Model:     modelFlag,
		AuthURL:   authURL,
		DetectedAt: time.Now().UTC().Format(time.RFC3339),
	}
	data, err := json.MarshalIndent(entry, "", "  ")
	if err != nil {
		return
	}
	logDir := filepath.Join(config.Dir(), "logs")
	_ = os.MkdirAll(logDir, 0o700)
	_ = os.WriteFile(filepath.Join(logDir, "auth_required.json"), data, 0o600)
}

