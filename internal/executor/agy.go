package executor

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/ankit373/hydra/internal/config"
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

// AgyExecutor invokes `agy --print` with model selection via settings.json swap.
// Ports all logic from dispatch/agy.sh natively in Go.
type AgyExecutor struct{}

func (e *AgyExecutor) Execute(ctx context.Context, req Request) (*Response, error) {
	modelFlag := req.Head.Meta["model_flag"]
	if modelFlag == "" {
		return nil, fmt.Errorf("agy executor: head %q has no model_flag in Meta", req.Head.ID)
	}

	settingsPath := agySitesPath()
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
		if d, err := time.ParseDuration(t + "s"); err == nil {
			timeout = d
		}
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, "agy", "--print", req.Prompt, "--print-timeout", fmt.Sprintf("%ds", int(timeout.Seconds())))
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	start := time.Now()
	runErr := cmd.Run()
	duration := time.Since(start)

	stderrStr := stderr.String()
	outStr := stdout.String()

	// Auth detection: check stderr + first 3 lines of stdout only.
	// Never scan full output — model responses may contain auth strings.
	firstLines := strings.Join(strings.SplitN(outStr, "\n", 4)[:min3(strings.Count(outStr, "\n")+1, 3)], "\n")
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
		Output:        output,
		Duration:      duration,
		Model:         req.Head.ID,
		InputTokens:   promptTokens,
		OutputTokens:  responseTokens,
	}, nil
}

// agySitesPath returns the path to agy's settings.json.
func agySitesPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".gemini", "antigravity-cli", "settings.json")
}

// swapAgyModel writes modelFlag into settings.json and returns the original model.
func swapAgyModel(settingsPath, modelFlag string) (string, error) {
	raw, err := os.ReadFile(settingsPath)
	if err != nil {
		return "", err
	}
	var m map[string]interface{}
	if err := json.Unmarshal(raw, &m); err != nil {
		return "", err
	}
	original, _ := m["model"].(string)
	m["model"] = modelFlag
	updated, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return "", err
	}
	tmp := settingsPath + ".hydra-tmp"
	if err := os.WriteFile(tmp, updated, 0o600); err != nil {
		return "", err
	}
	return original, os.Rename(tmp, settingsPath)
}

// restoreAgyModel writes back the original model (or deletes the key if empty).
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
	_ = os.Rename(tmp, settingsPath)
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

// writeTokenSidecar writes to HYDRA_TOKEN_SIDECAR if set (for cost.sh compatibility).
func writeTokenSidecar(model, executor, source string, promptTokens, responseTokens int) {
	sidecar := os.Getenv("HYDRA_TOKEN_SIDECAR")
	if sidecar == "" {
		return
	}
	type entry struct {
		Model          string `json:"model"`
		Executor       string `json:"executor"`
		Source         string `json:"source"`
		PromptTokens   int    `json:"prompt_tokens"`
		ResponseTokens int    `json:"response_tokens"`
	}
	data, err := json.Marshal(entry{model, executor, source, promptTokens, responseTokens})
	if err != nil {
		return
	}
	_ = os.WriteFile(sidecar, data, 0o600)
}

func min3(a, b int) int {
	if a < b {
		return a
	}
	return b
}
