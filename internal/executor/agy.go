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
		msg += ", authenticate at: " + e.AuthURL
	}
	return msg
}

var authSignalRe = regexp.MustCompile(
	`(?i)(not\s+(logged|authenticated|authorized)|please\s+(log\s*in|sign\s*in|authenticate)|login\s+required|auth(entication)?\s+required|visit\s+https?://accounts\.google\.com|antigravity\.google/auth|sign\s+in\s+to\s+continue)`,
)
var authURLRe = regexp.MustCompile(`https?://\S+`)

// agyMu guards recoverAgySwap, the only settings.json touch left in Execute:
// cleanup of a stale swap sentinel a pre-#522 Hydra binary may have left
// behind after being killed mid swap. Model selection itself goes through
// agy's own --model flag now (verified against the real CLI, see #522), so
// concurrent calls no longer share any mutable file and cmd.Run(), the
// actual tens-of-seconds subprocess work, runs outside this lock entirely.
var agyMu sync.Mutex

// AgyExecutor invokes `agy --print` with model selection via the --model flag.
// Ports all logic from dispatch/agy.sh natively in Go.
type AgyExecutor struct{}

func (e *AgyExecutor) Execute(ctx context.Context, req Request) (*Response, error) {
	modelFlag := req.Head.Meta["model_flag"]
	if modelFlag == "" {
		return nil, fmt.Errorf("agy executor: head %q has no model_flag in Meta", req.Head.ID)
	}

	agyMu.Lock()
	recoverAgySwap(agySitesPath())
	agyMu.Unlock()

	timeout := 300 * time.Second
	if t := os.Getenv("AGY_TIMEOUT"); t != "" {
		if d, err := time.ParseDuration(t); err == nil {
			timeout = d
		} else if d, err := time.ParseDuration(t + "s"); err == nil {
			// Bare integer seconds (e.g. "300"), treat as seconds.
			timeout = d
		}
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// stderr is only used for auth detection, cap it at 64 KB so a runaway
	// process can't exhaust memory through error output.
	stderr := util.NewAccumulator(64 << 10)
	stdout := util.NewAccumulator(0)
	cmd := exec.CommandContext(ctx, "agy", "--print", req.Prompt,
		"--model", modelFlag, "--print-timeout", fmt.Sprintf("%ds", int(timeout.Seconds())))
	cmd.Stdout = stdout
	cmd.Stderr = stderr

	start := time.Now()
	runErr := cmd.Run()
	duration := time.Since(start)

	stderrStr := stderr.String()
	outStr := stdout.String()

	// Auth detection: check stderr + first 3 lines of stdout only.
	// Never scan full output, model responses may contain auth strings.
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
		return nil, fmt.Errorf("agy exec %s: %w, %s", req.Head.ID, runErr, strings.TrimSpace(stderrStr))
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
		// agy does not report token usage, these are char/4 estimates.
		TokensEstimated: true,
	}, nil
}

// agySitesPath returns the path to agy's settings.json.
func agySitesPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".gemini", "antigravity-cli", "settings.json")
}

// agyOrigSuffix is the sentinel a pre-#522 Hydra could leave behind if
// SIGKILLed mid settings.json swap. Model selection no longer swaps
// settings.json (see Execute), so nothing writes this sentinel anymore,
// recoverAgySwap only cleans up a leftover from an older binary.
const agyOrigSuffix = ".hydra-orig"

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
		Pool       string `json:"pool"`
		Model      string `json:"model"`
		AuthURL    string `json:"auth_url"`
		DetectedAt string `json:"detected_at"`
	}
	entry := authEntry{
		Pool:       pool,
		Model:      modelFlag,
		AuthURL:    authURL,
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
