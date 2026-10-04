// SPDX-License-Identifier: MIT

package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ankit373/hydra/internal/dispatch"
	"github.com/ankit373/hydra/internal/executor"
	"github.com/ankit373/hydra/internal/provider"
	"github.com/ankit373/hydra/internal/runid"
	"github.com/ankit373/hydra/internal/serve"
)

// serveRouter adapts the real router to serve.Router. internal/serve does not
// depend on dispatch, so it is testable without a model; this is where the two
// meet, the same seam internal/workflow and internal/vet use.
type serveRouter struct {
	d           *dispatch.Dispatcher
	runID       string
	defaultEnum string
	localOnly   bool
}

func (r serveRouter) Chat(ctx context.Context, req serve.Request) (serve.Answer, error) {
	tier, enum, head, err := routeToDispatch(req.Route, r.defaultEnum)
	if err != nil {
		return serve.Answer{}, err
	}

	res, err := r.d.Dispatch(ctx, flattenConversation(req.Messages), dispatch.Options{
		TierHint:   tier,
		Enum:       enum,
		Head:       head,
		LocalOnly:  r.localOnly,
		MaxTokens:  req.MaxTokens,
		Messages:   req.Messages,
		Tools:      req.Tools,
		ToolChoice: req.ToolChoice,
		OnStream:   streamEvents(req.OnEvent),
		RunID:      r.runID,
		TaskID:     runid.New(),
	})
	if err != nil {
		return serve.Answer{}, callerError(err)
	}

	ans := serve.Answer{Output: res.Output, Head: res.Head.ID, Model: res.Head.Name}
	if res.Response != nil {
		ans.ToolCalls = res.Response.ToolCalls
		ans.FinishReason = res.Response.FinishReason
		ans.InputTokens, ans.OutputTokens = res.Response.InputTokens, res.Response.OutputTokens
	}
	return ans, nil
}

// streamEvents adapts the router's events to the endpoint's, and is nil when
// the client did not ask for a stream, which is what keeps that request on the
// buffered executor path.
//
// StreamAttemptStarted is dropped: the protocol has no frame for "a head was
// picked and is thinking", and inventing one would put a chunk on the wire that
// no client knows how to render.
func streamEvents(on func(serve.Event)) dispatch.OnStream {
	if on == nil {
		return nil
	}
	return func(e dispatch.StreamEvent) {
		switch e.Kind {
		case dispatch.StreamDelta:
			on(serve.Event{
				Kind: serve.EventDelta, Text: e.Text,
				Head: e.Head.ID, Model: e.Head.Name, SpanID: e.SpanID,
			})
		case dispatch.StreamAttemptFailed:
			on(serve.Event{
				Kind: serve.EventAttemptFailed, Reason: e.Reason,
				Head: e.Head.ID, Model: e.Head.Name, SpanID: e.SpanID,
			})
		}
	}
}

// callerError re-labels a refusal the caller's own payload caused, so a client
// is told 400 about its request rather than 502 about a head that never
// answered and was not the reason.
func callerError(err error) error {
	if errors.Is(err, executor.ErrUnaskable) {
		return fmt.Errorf("%w: %w", serve.ErrBadRequest, err)
	}
	// A head the client named is the client's choice, so its failure is a 4xx
	// and must stay out of the SDKs' 5xx retry class. The message is already
	// the right words and is kept exactly (#1145).
	switch {
	case errors.Is(err, dispatch.ErrHeadUnknown):
		return gradedError{serve.ErrNotFound, err}
	case errors.Is(err, dispatch.ErrHeadUnusable):
		return gradedError{serve.ErrBadRequest, err}
	}
	return err
}

// gradedError attaches a status grade without changing what the caller reads.
type gradedError struct {
	grade error
	msg   error
}

func (e gradedError) Error() string   { return e.msg.Error() }
func (e gradedError) Unwrap() []error { return []error{e.msg, e.grade} }

// Models advertises the routing keys alongside the discovered heads, so any
// OpenAI client's model picker becomes Hydra's routing UI.
func (r serveRouter) Models() []serve.Model {
	out := []serve.Model{{ID: "hydra", Object: "model", OwnedBy: "hydra"}}
	for _, name := range dispatch.TierNames() {
		out = append(out, serve.Model{ID: "hydra/" + name, Object: "model", OwnedBy: "hydra"})
	}
	return append(out, advertisable(r.d.Heads())...)
}

// advertisable is the heads a picker may offer. A picker that lists what the
// router refuses is a broken picker: an embedding-only head has no completion
// API at all, so choosing it could only ever fail. `hyctl probe` is where a
// discovered-but-unroutable head belongs, with its reason (#1145).
func advertisable(heads []provider.Head) []serve.Model {
	var out []serve.Model
	for _, h := range heads {
		if executor.Unroutable(h) != "" {
			continue
		}
		out = append(out, serve.Model{ID: h.ID, Object: "model", OwnedBy: h.Provider})
	}
	return out
}

// routeToDispatch turns the client's routing key into what the router reads.
func routeToDispatch(rt serve.Route, defaultEnum string) (tier, enum, head string, err error) {
	switch {
	case rt.Head != "":
		return "", "", rt.Head, nil

	case rt.Tier != "":
		return rt.Tier, "", "", nil

	case rt.Enum != "":
		key := strings.ToUpper(strings.TrimSpace(rt.Enum))
		n, ok := dispatch.ResolveTierName(key)
		if !ok {
			return "", "", "", fmt.Errorf("%w: unknown routing key %q; accepted: hydra, hydra/{%s}, or a head id",
				serve.ErrBadRequest, rt.Enum, strings.Join(dispatch.TierNames(), ", "))
		}
		// "local" is an alias for GRUNT rather than an enum of its own, so it
		// routes but records nothing: a cost row must never name a routing key
		// that does not exist (#832).
		if dispatch.IsKnownEnum(key) {
			return strconv.Itoa(n), key, "", nil
		}
		return strconv.Itoa(n), "", "", nil

	default:
		return dispatch.EnumToTier(defaultEnum), defaultEnum, "", nil
	}
}

// flattenConversation renders every turn as the single prompt the router
// classifies.
//
// Not just the last message. An agent loop's earlier turns carry tool results,
// which for a code-review client are the user's own source, and the ledger and
// the egress gate both read this prompt. Anything left out of it reaches the
// head unclassified.
func flattenConversation(msgs []executor.Message) string {
	var b strings.Builder
	for _, m := range msgs {
		if m.Content == "" {
			continue
		}
		if b.Len() > 0 {
			b.WriteString("\n\n")
		}
		if m.Role != "" {
			b.WriteString(m.Role)
			b.WriteString(":\n")
		}
		b.WriteString(m.Content)
	}
	return b.String()
}

func cmdServe() *cobra.Command {
	var (
		addr, token, enum string
		localOnly         bool
	)

	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Expose the router as an OpenAI-compatible endpoint",
		Long: "Serves POST /v1/chat/completions and GET /v1/models, so any tool that speaks\n" +
			"OpenAI routes through Hydra: policy, fallback, per-domain calibration and spend\n" +
			"logging all apply the way they do to `hyctl dispatch`.\n\n" +
			"The model field is the routing instruction. `hydra` takes the default enum,\n" +
			"`hydra/hard` or `hydra/t4` pin an enum or a tier, and a head id pins that head.\n\n" +
			"`stream: true` is answered as server-sent events. A fallback before any output is\n" +
			"invisible; one after it ends the stream, because SSE cannot take back a partial and\n" +
			"appending the next head's answer to it would compose a reply no head ever gave.\n\n" +
			"Binds 127.0.0.1. Any other address needs --token, because this endpoint spends\n" +
			"money and reads your code, and `hyctl security` reports exactly this risk about\n" +
			"other people's model servers.",
		Example: "  hyctl serve\n" +
			"  hyctl serve --enum SIMPLE --local\n" +
			"  hyctl serve --addr 0.0.0.0:8788 --token \"$HYDRA_SERVE_TOKEN\"\n\n" +
			"  # point open-code-review at it\n" +
			"  OCR_LLM_URL=http://127.0.0.1:8787/v1 OCR_LLM_MODEL=hydra ocr review",
		RunE: func(c *cobra.Command, _ []string) error {
			ctx := c.Context()
			if ctx == nil {
				ctx = context.Background()
			}
			if token == "" {
				token = os.Getenv("HYDRA_SERVE_TOKEN")
			}
			if !dispatch.IsKnownEnum(enum) {
				return fmt.Errorf("unknown enum %q", enum)
			}

			d, err := dispatch.New(ctx)
			if err != nil {
				return fmt.Errorf("dispatcher init: %w", err)
			}
			defer d.Close()

			ln, err := serve.Listen(addr, token)
			if err != nil {
				return err
			}
			r := serveRouter{d: d, runID: runid.New(), defaultEnum: enum, localOnly: localOnly}
			printServeBanner(os.Stdout, ln.Addr().String(), enum, token != "", localOnly, d.Heads())
			return serve.Serve(ctx, ln, serve.Handler(r, token))
		},
	}

	f := cmd.Flags()
	f.StringVar(&addr, "addr", "127.0.0.1:8787", "address to bind")
	f.StringVar(&token, "token", "", "bearer token required of every request (or HYDRA_SERVE_TOKEN)")
	f.StringVar(&enum, "enum", "STANDARD", "routing enum for a request that names no key")
	f.BoolVar(&localOnly, "local", false, "local Heads only, no API calls")
	return cmd
}

func printServeBanner(w io.Writer, addr, enum string, hasToken, localOnly bool, heads []provider.Head) {
	// Over the heads this server can actually reach. Counting all 16 on a
	// --local endpoint advertised ten that no request could ever route to
	// (#1147).
	reachable := 0
	withTools := 0
	for _, h := range heads {
		if localOnly && !h.LocalOnly {
			continue
		}
		if executor.Unroutable(h) != "" {
			continue
		}
		reachable++
		if executor.CanUseTools(h) {
			withTools++
		}
	}
	auth := "none, loopback only"
	if hasToken {
		auth = "bearer token required"
	}

	fmt.Fprintln(w)
	fmt.Fprintf(w, "  %s  %s\n", cortexStyle.Render("hyctl serve"), dimStyle.Render("http://"+addr+"/v1"))
	fmt.Fprintln(w)
	fmt.Fprintf(w, "  default routing   %s\n", enum)
	fmt.Fprintf(w, "  auth              %s\n", auth)
	if localOnly {
		fmt.Fprintf(w, "  heads             local only\n")
	}
	// "can be sent tools", which is a property of the dialect, not a promise
	// that the model will answer with a structured call: measured here, Ollama
	// reports tool support for every local model while Qwen2.5-Coder writes its
	// calls as message text. Saying which claim this is, because an agent
	// author reads this line as "my loop will work" (#1147).
	fmt.Fprintf(w, "  tool calling      %d of %d reachable Heads accept tool definitions\n", withTools, reachable)
	if withTools > 0 {
		fmt.Fprintf(w, "                    %s\n", dimStyle.Render(
			"a call written as message text is recovered; whether a model emits one is the model's"))
	}
	fmt.Fprintln(w)
	fmt.Fprintln(w, dimStyle.Render("  ctrl-c to stop"))
	fmt.Fprintln(w)
}
