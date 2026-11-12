// SPDX-License-Identifier: MIT

package main

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"sync"

	"github.com/spf13/cobra"

	"github.com/ankit373/hydra/internal/dispatch"
	"github.com/ankit373/hydra/internal/executor"
	"github.com/ankit373/hydra/internal/provider"
	"github.com/ankit373/hydra/internal/rank"
	"github.com/ankit373/hydra/internal/runid"
	"github.com/ankit373/hydra/internal/runlog"
	"github.com/ankit373/hydra/internal/serve"
	"github.com/ankit373/hydra/internal/trust"
)

// serveRouter adapts the real router to serve.Router. internal/serve does not
// depend on dispatch, so it is testable without a model; this is where the two
// meet, the same seam internal/workflow and internal/vet use.
type serveRouter struct {
	d *dispatch.Dispatcher
	// defaultEnum is --enum, the route of a `hydra` request no objective orders.
	defaultEnum string
	// objective is --objective, opt-in until v2.0.0: what an unpinned request
	// naming none optimises. Empty keeps `hydra` on defaultEnum.
	objective rank.Objective
	// implicit is set when the operator chose neither --enum nor --objective:
	// the default v2.0.0 changes, so relying on it is deprecated (#1206).
	implicit bool
	notice   *deprecationNotice
	// defaultClass is --task-class, the class an unpinned request naming none
	// routes for. Empty leaves such a request routing exactly as before E1.
	defaultClass string
	localOnly    bool
}

func (r serveRouter) Chat(ctx context.Context, req serve.Request) (serve.Answer, error) {
	opts, ev, err := r.options(req)
	if err != nil {
		return serve.Answer{}, err
	}
	if r.Deprecated(req) {
		r.notice.warn()
	}
	// Before dispatch, because a stream's headers leave with its first frame.
	if ev != nil && req.OnEvent != nil {
		req.OnEvent(serve.Event{Kind: serve.EventEvidence, Evidence: ev})
	}

	// One run per request. A server's unit of work is the request, not the
	// process: one run per session gave a long-lived server a single
	// ever-growing run, and `hyctl trace view` with no argument showed that
	// instead of the last request (#1149).
	prompt := flattenConversation(req.Messages)
	opts.TaskID = runid.New()
	runlog.DeclareRun(req.RequestID, opts.TaskID, prompt)
	defer runlog.FinishRun(req.RequestID, opts.TaskID)

	res, err := r.d.Dispatch(ctx, prompt, opts)
	if err != nil {
		// Every head over the client's own ceiling is the request's limit, not
		// a failed head, so it must stay out of the 5xx retry class.
		if errors.Is(err, dispatch.ErrOverCostCeiling) && opts.MaxCostSource == serve.MaxCostHeader {
			return serve.Answer{}, gradedError{serve.ErrBadRequest, err}
		}
		return serve.Answer{}, callerError(err)
	}

	ans := serve.Answer{Output: res.Output, Head: res.Head.ID, Model: res.Head.Name, Evidence: ev}
	if req.OnEvent == nil {
		ans.Evidence = withObjective(ev, res)
	}
	if res.Response != nil {
		ans.ToolCalls = res.Response.ToolCalls
		ans.FinishReason = res.Response.FinishReason
		ans.InputTokens, ans.OutputTokens = res.Response.InputTokens, res.Response.OutputTokens
	}
	return ans, nil
}

// withObjective reports the objective and the answering head's P(pass) basis,
// only when the objective is what ordered the candidates. A stream's headers
// leave before that is known, so it reports the class alone.
func withObjective(ev *serve.Evidence, res *dispatch.Result) *serve.Evidence {
	if res.Objective == "" {
		return ev
	}
	var out serve.Evidence
	if ev != nil {
		out = *ev
	}
	out.Objective, out.PPassBasis = string(res.Objective), passBasis(res.Scores[res.Head.ID])
	return &out
}

// passBasis says what P(pass) rests on: this class's own record, evidence
// borrowed from other classes, or the catalogue's score alone.
func passBasis(sc rank.Score) string {
	switch {
	case sc.Measured():
		return "measured"
	case sc.N > 0:
		return "borrowed"
	}
	return "catalogue_prior"
}

// Deprecated is serve.Deprecator: an unpinned `hydra` request naming no
// objective, on a server whose operator chose no default either.
func (r serveRouter) Deprecated(req serve.Request) bool {
	return r.implicit && req.Route == (serve.Route{}) && req.Objective == ""
}

// implicitDefaultWarning is the stderr line; the header says the same per request.
const implicitDefaultWarning = "hyctl serve: deprecated: model \"hydra\" with no objective routes as " +
	dispatch.ServedDefaultEnum + " today and will route by objective quality in v2.0.0. " +
	"Opt in now with --objective quality or the X-Hydra-Objective header; keep today's route " +
	"with model \"hydra/standard\" or an explicit --enum " + dispatch.ServedDefaultEnum + ". " +
	"HYDRA_NO_DEPRECATION_WARNINGS=1 silences this."

// deprecationNotice prints implicitDefaultWarning once per server start. Nil
// prints nothing, which is what HYDRA_NO_DEPRECATION_WARNINGS=1 gives.
type deprecationNotice struct {
	once sync.Once
	w    io.Writer
}

func (n *deprecationNotice) warn() {
	if n != nil {
		n.once.Do(func() { fmt.Fprintln(n.w, implicitDefaultWarning) })
	}
}

// newDeprecationNotice is nil when the operator silenced deprecations.
func newDeprecationNotice(w io.Writer) *deprecationNotice {
	if os.Getenv("HYDRA_NO_DEPRECATION_WARNINGS") == "1" {
		return nil
	}
	return &deprecationNotice{w: w}
}

// options is the one translation of a served request into what the router is
// asked, plus the class it routes for, nil when it routes for none.
func (r serveRouter) options(req serve.Request) (dispatch.Options, *serve.Evidence, error) {
	// An objective, the client's or the operator's, leaves `hydra` unpinned for
	// it to order; with none, `hydra` keeps --enum exactly as before #1206.
	obj := cmp.Or(req.Objective, r.objective)
	unpinned := r.defaultEnum
	if obj != "" {
		unpinned = ""
	}
	tier, enum, head, err := routeToDispatch(req.Route, unpinned)
	if err != nil {
		return dispatch.Options{}, nil, err
	}
	opts := dispatch.Options{
		TierHint:    tier,
		Enum:        enum,
		Head:        head,
		LocalOnly:   r.localOnly,
		MaxTokens:   req.MaxTokens,
		Temperature: req.Temperature,
		Messages:    req.Messages,
		Tools:       req.Tools,
		ToolChoice:  req.ToolChoice,
		OnStream:    streamEvents(req.OnEvent),
		RunID:       req.RequestID,
	}
	opts.MaxCostUSD, opts.MaxCostSource = requestCeiling(req.MaxCostUSD)

	// A class is reported only where it routes. A pinned head is the client's
	// choice of model, so a class can neither reorder nor be claimed for it.
	var ev *serve.Evidence
	switch {
	case req.Route.Head != "":
	case req.TaskClass != "":
		ev = &serve.Evidence{TaskClass: req.TaskClass, TaskClassBasis: "header"}
	case r.defaultClass != "" && (req.Route == (serve.Route{}) || isServedDefault(req.Route, enum)):
		ev = &serve.Evidence{TaskClass: r.defaultClass, TaskClassBasis: "route"}
	}
	if ev != nil {
		// The cache key does not cover the class, so a class-routed answer
		// must neither be served from nor stored for an unclassed request.
		opts.Domain, opts.NoCache = ev.TaskClass, true
	}
	// Only an unpinned route is ordered by an objective. The cache key does not
	// cover one, so a client choosing it neither reads nor fills the cache.
	if tier == "" && head == "" {
		opts.Objective = obj
		opts.NoCache = opts.NoCache || req.Objective != ""
	}
	return opts, ev, nil
}

// isServedDefault is hydra/standard, the pre-E1 `hydra`, which keeps the
// --task-class that route had.
func isServedDefault(rt serve.Route, enum string) bool {
	return rt.Enum != "" && enum == dispatch.ServedDefaultEnum
}

// requestCeiling is the lower of the client's and the policy's, read per request
// so a policy edit applies without a restart. A request that set none keeps
// the no-ceiling it had before E1, so the header can only lower.
func requestCeiling(client float64) (float64, string) {
	if client <= 0 {
		return 0, ""
	}
	if policyUSD, from := servePolicyCeiling(); policyUSD > 0 && policyUSD < client {
		return policyUSD, from
	}
	return client, serve.MaxCostHeader
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
// An empty defaultEnum leaves `hydra` unpinned, for the objective to order.
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

	case defaultEnum != "":
		return dispatch.EnumToTier(defaultEnum), defaultEnum, "", nil
	}
	return "", "", "", nil
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
		taskClass, obj    string
		localOnly         bool
		confidence        float64
		heads             []string
	)

	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Expose the router as an OpenAI-compatible endpoint",
		Long: "Serves POST /v1/chat/completions and GET /v1/models, so any tool that speaks\n" +
			"OpenAI routes through Hydra: policy, fallback, per-domain calibration and spend\n" +
			"logging all apply the way they do to `hyctl dispatch`.\n\n" +
			"POST /v1/decisions and /v1/systemone take typed questions in either wire format and\n" +
			"ask the decision sources in order: one when its answer clears --confidence, more\n" +
			"through the SPRT when not. X-Hydra-Decision-Sources and -Confidence say which.\n\n" +
			"The model field is the routing instruction. `hydra` takes the default enum,\n" +
			"`hydra/hard` or `hydra/t4` pin an enum or a tier, and a head id pins that head.\n\n" +
			"Routing by objective is opt-in: --objective, or X-Hydra-Objective per request, has\n" +
			"`hydra` try the head measured strongest in the task class first (quality) or the\n" +
			"cheapest one measured competent (cost). In v2.0.0 quality becomes the default; until\n" +
			"then a `hydra` request relying on neither gets a Deprecation header, and\n" +
			"`hydra/standard` or an explicit --enum keeps today's route after the change.\n\n" +
			"`stream: true` is answered as server-sent events. A fallback before any output is\n" +
			"invisible; one after it ends the stream, because SSE cannot take back a partial and\n" +
			"appending the next head's answer to it would compose a reply no head ever gave.\n\n" +
			"Binds 127.0.0.1. Any other address needs --token, because this endpoint spends\n" +
			"money and reads your code, and `hyctl security` reports exactly this risk about\n" +
			"other people's model servers.",
		Example: "  hyctl serve\n" +
			"  hyctl serve --enum SIMPLE --local\n" +
			"  hyctl serve --addr 0.0.0.0:8788 --token \"$HYDRA_SERVE_TOKEN\"\n" +
			"  hyctl serve --heads ollama/qwen3:8b,env/openai\n\n" +
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
			objective, err := serveObjective(c, obj)
			if err != nil {
				return err
			}
			defaultClass := ""
			if taskClass != "" {
				if defaultClass, err = trust.TaskClass(taskClass); err != nil {
					return fmt.Errorf("--task-class: %w", err)
				}
			}

			d, err := dispatch.New(ctx)
			if err != nil {
				return fmt.Errorf("dispatcher init: %w", err)
			}
			defer d.Close()
			if err := restrictServeHeads(d, heads, c.Flags().Changed("heads"), localOnly); err != nil {
				return err
			}

			ln, err := serve.Listen(addr, token)
			if err != nil {
				return err
			}
			decider, err := newDecisionRouter(ctx, confidence, localOnly, d.Allows)
			if err != nil {
				return err
			}
			r := serveRouter{
				d: d, defaultEnum: enum, objective: objective, implicit: implicitDefault(c, objective),
				notice: newDeprecationNotice(os.Stderr), defaultClass: defaultClass, localOnly: localOnly,
			}
			printServeBanner(os.Stdout, ln.Addr().String(), defaultRouting(enum, objective), token != "", localOnly, d.Heads(), decider.unlisted)
			return serve.Serve(ctx, ln, serve.Handler(r, decider, token))
		},
	}

	f := cmd.Flags()
	f.StringVar(&addr, "addr", "127.0.0.1:8787", "address to bind")
	f.StringVar(&token, "token", "", "bearer token required of every request (or HYDRA_SERVE_TOKEN)")
	f.StringVar(&enum, "enum", dispatch.ServedDefaultEnum, "routing enum for a request that names no key")
	f.StringVar(&obj, "objective", "", "route `hydra` by objective, quality or cost (opt-in; quality becomes the default in v2.0.0)")
	f.StringVar(&taskClass, "task-class", "", "dotted task class for a request that names none, e.g. classification.banking77")
	f.BoolVar(&localOnly, "local", false, "local Heads only, no API calls")
	f.Float64Var(&confidence, "confidence", 0.95, "stop asking decision sources once an answer is this sure")
	f.StringSliceVar(&heads, "heads", nil, "route among only these head ids (default [serve] heads in config.toml; "+
		"--heads \"\" clears it). Embedding models stay available to the cache and recall, listed or not; "+
		"decision sources are not heads it can name, so with a list set /v1/decisions has none")
	return cmd
}

// restrictServeHeads applies the allowlist before the server binds, so a typo is a
// startup error rather than a server quietly routing to heads the operator excluded.
func restrictServeHeads(d *dispatch.Dispatcher, flagHeads []string, flagSet, localOnly bool) error {
	ids := d.ServeHeads()
	if flagSet {
		ids = flagHeads
	}
	if err := d.RestrictHeads(ids, localOnly); err != nil {
		return fmt.Errorf("head allowlist: %w", err)
	}
	return nil
}

// serveObjective is --objective: empty when not passed, refused when passed
// empty or unknown, or alongside --enum, since both say how `hydra` routes.
func serveObjective(c *cobra.Command, obj string) (rank.Objective, error) {
	if !c.Flags().Changed("objective") {
		return "", nil
	}
	o, err := rank.ParseObjective(obj)
	if err == nil && o == "" {
		err = fmt.Errorf("give %s or %s", rank.Quality, rank.Cost)
	}
	if err != nil {
		return "", fmt.Errorf("--objective: %w", err)
	}
	if c.Flags().Changed("enum") {
		return "", errors.New("--objective and --enum both set how `hydra` routes; pass one")
	}
	return o, nil
}

// implicitDefault is a server whose operator chose neither --enum nor
// --objective, so `hydra` takes the default v2.0.0 changes.
func implicitDefault(c *cobra.Command, o rank.Objective) bool {
	return !c.Flags().Changed("enum") && o == ""
}

// defaultRouting is the banner's account of how `hydra` routes.
func defaultRouting(enum string, o rank.Objective) string {
	if o != "" {
		return fmt.Sprintf("objective %s (hydra/standard pins %s)", o, dispatch.ServedDefaultEnum)
	}
	return enum
}

func printServeBanner(w io.Writer, addr, routing string, hasToken, localOnly bool, heads []provider.Head, unlistedDecisions int) {
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
	fmt.Fprintf(w, "  default routing   %s\n", routing)
	fmt.Fprintf(w, "  auth              %s\n", auth)
	if localOnly {
		fmt.Fprintf(w, "  heads             local only\n")
	}
	if unlistedDecisions > 0 {
		fmt.Fprintf(w, "  decisions         none: --heads names no decision source, so /v1/decisions has none (%d dropped)\n",
			unlistedDecisions)
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
