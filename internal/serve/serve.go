// SPDX-License-Identifier: MIT

package serve

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/ankit373/hydra/internal/executor"
)

// ErrBadRequest marks a caller's mistake, so a routing key the router cannot
// resolve answers 400 rather than reading as Hydra having failed.
var ErrBadRequest = errors.New("bad request")

// hydraModel is the routing prefix the model field uses.
const hydraModel = "hydra"

// maxBody bounds one request. An agent loop's prompt grows with every tool
// result, so this is generous, but unbounded is a memory exhaustion away.
const maxBody = 32 << 20

// Route is the client's `model` field read as a routing instruction. At most
// one field is set.
type Route struct {
	Enum string
	Tier string
	Head string
}

// Request is one chat completion, already parsed.
type Request struct {
	Messages   []executor.Message
	Tools      []executor.ToolDef
	ToolChoice json.RawMessage
	MaxTokens  int
	Route      Route

	// OnEvent is set only when the client asked for a stream. A router that
	// ignores it still answers correctly: its whole output then arrives when
	// Chat returns, and serve sends it as one delta.
	OnEvent func(Event)
}

// Answer is what a head replied.
type Answer struct {
	Output       string
	ToolCalls    []executor.ToolCall
	FinishReason string
	Head         string
	Model        string
	InputTokens  int
	OutputTokens int
}

// Model is one entry in the /v1/models listing.
type Model struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	OwnedBy string `json:"owned_by"`
}

// Router routes one request. This package deliberately does not import
// dispatch, so it is testable without a model; cmd/hydra adapts the real one.
type Router interface {
	Chat(ctx context.Context, req Request) (Answer, error)
	Models() []Model
}

// Handler is the OpenAI-compatible surface: the two endpoints every client
// touches, and nothing else.
func Handler(r Router, token string) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/models", guard(token, func(w http.ResponseWriter, req *http.Request) {
		if req.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "use GET")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"object": "list", "data": r.Models()})
	}))
	mux.HandleFunc("/v1/chat/completions", guard(token, func(w http.ResponseWriter, req *http.Request) {
		if req.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "use POST")
			return
		}
		chat(w, req, r)
	}))
	return mux
}

// Serve runs the endpoint on ln until ctx is done, then drains in-flight
// requests. A dispatch in progress has already been paid for, so closing the
// listener out from under it would spend money and discard the answer.
func Serve(ctx context.Context, ln net.Listener, h http.Handler) error {
	srv := &http.Server{
		Handler: h,
		// No write timeout: a routed dispatch can take minutes on a local
		// head, and a deadline here would cut off an answer already paid for.
		ReadHeaderTimeout: 10 * time.Second,
	}
	errc := make(chan error, 1)
	go func() { errc <- srv.Serve(ln) }()

	select {
	case err := <-errc:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		return srv.Shutdown(shutdown)
	}
}

type chatRequest struct {
	Model    string             `json:"model"`
	Messages []executor.Message `json:"messages"`
	// Both spellings: the newer one is what open-code-review sends, and a
	// server that reads only the old name silently ignores the caller's cap.
	MaxTokens           int  `json:"max_tokens"`
	MaxCompletionTokens int  `json:"max_completion_tokens"`
	Stream              bool `json:"stream"`
	StreamOptions       *struct {
		IncludeUsage bool `json:"include_usage"`
	} `json:"stream_options"`
	Tools      []executor.ToolDef `json:"tools"`
	ToolChoice json.RawMessage    `json:"tool_choice"`
}

func chat(w http.ResponseWriter, req *http.Request, r Router) {
	raw, err := io.ReadAll(io.LimitReader(req.Body, maxBody+1))
	if err != nil {
		writeError(w, http.StatusBadRequest, "could not read the request body")
		return
	}
	if len(raw) > maxBody {
		writeError(w, http.StatusRequestEntityTooLarge, fmt.Sprintf("request exceeds %d bytes", maxBody))
		return
	}

	var in chatRequest
	if err := json.Unmarshal(raw, &in); err != nil {
		writeError(w, http.StatusBadRequest, "could not parse the request as JSON: "+err.Error())
		return
	}
	if len(in.Messages) == 0 {
		writeError(w, http.StatusBadRequest, "messages is empty, so there is nothing to answer")
		return
	}
	route, err := parseRoute(in.Model)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	if in.Stream {
		streamChat(w, req, r, in, route)
		return
	}

	ans, err := r.Chat(req.Context(), request(in, route, nil))
	if err != nil {
		status := http.StatusBadGateway
		if errors.Is(err, ErrBadRequest) {
			status = http.StatusBadRequest
		}
		writeError(w, status, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"id":      "chatcmpl-" + randomID(),
		"object":  "chat.completion",
		"created": time.Now().Unix(),
		"model":   firstNonEmpty(ans.Model, ans.Head, in.Model),
		"choices": []map[string]any{{
			"index": 0,
			"message": executor.Message{
				Role:      "assistant",
				Content:   ans.Output,
				ToolCalls: ans.ToolCalls,
			},
			"finish_reason": finishReason(ans),
		}},
		"usage": map[string]int{
			"prompt_tokens":     ans.InputTokens,
			"completion_tokens": ans.OutputTokens,
			"total_tokens":      ans.InputTokens + ans.OutputTokens,
		},
	})
}

// streamChat answers as server-sent events.
func streamChat(w http.ResponseWriter, req *http.Request, r Router, in chatRequest, route Route) {
	ctx, cancel := context.WithCancel(req.Context())
	defer cancel()

	st := newStreamer(w, in.Model)
	ans, err := r.Chat(ctx, request(in, route, func(e Event) { st.event(e, cancel) }))
	st.done = true

	switch {
	case st.aborted:
		// The stream already said why it ended, and the dispatch that carried
		// on past it was cancelled: nobody is left to read that answer.
	case err != nil && !st.started():
		// Nothing has reached the client, so an error is still a real status
		// code rather than a 200 carrying bad news.
		status := http.StatusBadGateway
		if errors.Is(err, ErrBadRequest) {
			status = http.StatusBadRequest
		}
		writeError(w, status, err.Error())
	case err != nil:
		st.fail(err.Error())
	default:
		st.finish(ans, in.StreamOptions != nil && in.StreamOptions.IncludeUsage)
	}
}

// request is the one derivation of what the router is asked, so the streamed
// and buffered paths cannot come to disagree about it.
func request(in chatRequest, route Route, on func(Event)) Request {
	return Request{
		Messages:   in.Messages,
		Tools:      in.Tools,
		ToolChoice: in.ToolChoice,
		MaxTokens:  firstPositive(in.MaxCompletionTokens, in.MaxTokens),
		Route:      route,
		OnEvent:    on,
	}
}

// parseRoute reads the model field as a routing instruction, which makes any
// OpenAI client's model picker into Hydra's routing UI.
//
//	"" or "hydra"   the configured default
//	"hydra/HARD"    that enum
//	"hydra/t4"      that tier
//	anything else   pin that head by id
func parseRoute(model string) (Route, error) {
	m := strings.TrimSpace(model)
	if m == "" || strings.EqualFold(m, hydraModel) {
		return Route{}, nil
	}
	rest, ok := cutPrefixFold(m, hydraModel+"/")
	if !ok {
		return Route{Head: m}, nil
	}
	if rest == "" {
		return Route{}, fmt.Errorf("%w: %q names no routing key; try %q, %q or a head id",
			ErrBadRequest, m, hydraModel, hydraModel+"/HARD")
	}
	// A tier is "t" plus a number. "trivial" also starts with t, and reading it
	// as tier "rivial" would route somewhere nobody asked for.
	if n, isTier := cutPrefixFold(rest, "t"); isTier {
		if tier, convErr := strconv.Atoi(n); convErr == nil && tier > 0 {
			return Route{Tier: n}, nil
		}
	}
	return Route{Enum: strings.ToUpper(rest)}, nil
}

func guard(token string, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		if token != "" && !authorized(req, token) {
			writeError(w, http.StatusUnauthorized, "missing or wrong bearer token")
			return
		}
		next(w, req)
	}
}

// authorized compares in constant time, so the endpoint does not leak the token
// one byte at a time to anything that can reach it.
func authorized(req *http.Request, token string) bool {
	got, ok := cutPrefixFold(strings.TrimSpace(req.Header.Get("Authorization")), "bearer ")
	if !ok {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(strings.TrimSpace(got)), []byte(token)) == 1
}

// finishReason: a head that reported none but asked for tools still stopped for
// one, and a client branches on this rather than on the content.
func finishReason(a Answer) string {
	if a.FinishReason != "" {
		return a.FinishReason
	}
	if len(a.ToolCalls) > 0 {
		return "tool_calls"
	}
	return "stop"
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

// writeError uses OpenAI's own error envelope, which is what every client
// already knows how to surface to its user.
func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]any{
		"error": map[string]any{"message": msg, "type": errorType(status)},
	})
}

func errorType(status int) string {
	switch {
	case status == http.StatusUnauthorized:
		return "authentication_error"
	case status >= 500:
		return "api_error"
	default:
		return "invalid_request_error"
	}
}

func randomID() string {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		return strconv.FormatInt(time.Now().UnixNano(), 16)
	}
	return hex.EncodeToString(b[:])
}

func cutPrefixFold(s, prefix string) (string, bool) {
	if len(s) < len(prefix) || !strings.EqualFold(s[:len(prefix)], prefix) {
		return "", false
	}
	return s[len(prefix):], true
}

func firstPositive(vals ...int) int {
	for _, v := range vals {
		if v > 0 {
			return v
		}
	}
	return 0
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
