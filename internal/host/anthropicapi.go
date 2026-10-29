//go:build client && !server

package host

import (
	"bytes"
	"context"
	"errors"
	"strings"

	"github.com/anthropics/anthropic-sdk-go/v2"
	"github.com/anthropics/anthropic-sdk-go/v2/option"

	"github.com/danielckv/agentic-plan-forge/internal/contracts"
)

// Anthropic model defaults. Deliberation runs on the strongest model: ADR 0009
// measured the architect design part at 110 seconds and 94,184 input tokens,
// and spreading that load by downgrading the model was rejected there. The
// judge runs on a different model so the critic does not simply agree with the
// drafts it reviews, and the narrator runs on the cheapest one because its
// output is a status line that gates nothing.
const (
	anthropicMainModel  = "claude-opus-5-5"
	anthropicJudgeModel = "claude-sonnet-5-5"
	anthropicSmallModel = "claude-haiku-5-5"

	anthropicEndpoint = "https://api.anthropic.com/v1/messages"
	// anthropicLabel is what `doctor` prints as this host's version. An API
	// host has no installed binary, so it reports the wire surface it targets.
	anthropicLabel = "messages-v1"
)

// AnthropicAPI generates over the Messages API with the owner's own Console
// API key.
//
// It is not an alternative sign-in for a Claude subscription, and cannot be:
// Anthropic's terms reserve Claude.ai OAuth for Anthropic's own applications
// and forbid a third-party developer from collecting, storing or intermediating
// those credentials. A subscription is reached only through the unmodified
// `claude` binary, which stays a separate host (see Claude in claude.go).
type AnthropicAPI struct{}

// NewAnthropicAPI constructs the production Anthropic API adapter.
func NewAnthropicAPI() Adapter { return &AnthropicAPI{} }

func (a *AnthropicAPI) Kind() Kind { return KindAnthropic }

func (a *AnthropicAPI) client() (anthropic.Client, error) {
	key, _, err := apiKey(KindAnthropic)
	if err != nil {
		return anthropic.Client{}, err
	}
	if key == "" {
		return anthropic.Client{}, noKeyError(KindAnthropic)
	}
	opts := []option.RequestOption{option.WithAPIKey(key)}
	if base := baseURLFor(KindAnthropic); base != "" {
		opts = append(opts, option.WithBaseURL(base))
	}
	return anthropic.NewClient(opts...), nil
}

// Detect reports the wire surface when a key is configured. An API host is
// "installed" exactly when it has a credential, so this is the cheapest honest
// answer and it starts no process.
func (a *AnthropicAPI) Detect(context.Context) (string, error) {
	key, _, err := apiKey(KindAnthropic)
	if err != nil {
		return "", err
	}
	if key == "" {
		return "", noKeyError(KindAnthropic)
	}
	return anthropicLabel, nil
}

func (a *AnthropicAPI) Auth(context.Context) (AuthState, error) {
	key, source, err := apiKey(KindAnthropic)
	if err != nil {
		return AuthState{}, err
	}
	if key == "" {
		return AuthState{
			Ready:  false,
			Method: "api key",
			Detail: "no key: add one in Setup or export " + anthropicKeyEnv,
		}, nil
	}
	return AuthState{Ready: true, Method: "api key", Detail: "key from " + source}, nil
}

const anthropicKeyEnv = "ANTHROPIC_API_KEY"

// Models asks the provider what it serves, so the window lists what the key
// can actually reach rather than a list baked in at release time. A provider
// that cannot be reached falls back to the catalogue instead of failing: the
// model list is informational, and Preflight is the authority on whether a
// call can be served.
func (a *AnthropicAPI) Models(ctx context.Context) ([]ModelRef, error) {
	client, err := a.client()
	if err != nil {
		return nil, err
	}
	page, err := client.Models.List(ctx, anthropic.ModelListParams{})
	if err != nil {
		return modelCatalogue(KindAnthropic), nil
	}
	out := make([]ModelRef, 0, len(page.Data))
	for _, model := range page.Data {
		label := model.DisplayName
		if label == "" {
			label = model.ID
		}
		out = append(out, ModelRef{ID: model.ID, Label: label})
	}
	if len(out) == 0 {
		return modelCatalogue(KindAnthropic), nil
	}
	return out, nil
}

// Preflight verifies the key against the provider without spending a
// generation. Listing models is an authenticated round trip that costs no
// tokens, which matters because startup preflights every registered host.
func (a *AnthropicAPI) Preflight(ctx context.Context) error {
	client, err := a.client()
	if err != nil {
		return err
	}
	if _, err := client.Models.List(ctx, anthropic.ModelListParams{}); err != nil {
		return &contracts.ErrCapability{
			Host:   string(KindAnthropic),
			Reason: "the Anthropic API rejected this key: " + err.Error(),
			Remedy: "check the key in Setup, or export a working " + anthropicKeyEnv,
		}
	}
	return nil
}

func (a *AnthropicAPI) Generate(
	ctx context.Context,
	req contracts.GenerateRequest,
	emit func(contracts.GenerationEvent) error,
) (contracts.GenerationResult, error) {
	model := anthropicSlotModel(req)
	result := contracts.GenerationResult{Receipt: contracts.ToolFreeReceipt{
		Host:    string(KindAnthropic),
		Version: anthropicLabel,
		Argv:    apiReceiptArgs(anthropicEndpoint, model),
	}}
	client, err := a.client()
	if err != nil {
		return result, err
	}

	callCtx, cancel := context.WithTimeout(ctx, req.ClampTimeout())
	defer cancel()

	params := a.params(req, model)
	text, usage, err := a.stream(callCtx, client, params, req, emit)
	if err != nil {
		// A schema the provider will not accept must not cost the stage. The
		// request is reissued once without the constraint, and the reply then
		// goes through the same unwrap-and-repair path the CLI hosts use.
		if params.OutputConfig.Format.Schema != nil && isSchemaRejection(err) {
			params.OutputConfig.Format = anthropic.JSONOutputFormatParam{}
			text, usage, err = a.stream(callCtx, client, params, req, emit)
		}
		if err != nil {
			result.Usage = usage
			return result, apiFailure(KindAnthropic, "generate", err)
		}
	}
	return finalizeGeneration(req, text, result.Receipt, usage, emit)
}

// params builds one Messages request.
//
// Two cache breakpoints are placed, which is where the cost of a deliberation
// run actually sits. The system prompt is stable for the whole run, and a
// large user payload repeats across revision cycles; both are read back at
// cache rates instead of being paid for again. Render order is tools, system,
// messages, so the system breakpoint covers the stable prefix.
func (a *AnthropicAPI) params(req contracts.GenerateRequest, model string) anthropic.MessageNewParams {
	system := []anthropic.TextBlockParam{{
		Text:         req.SystemPrompt,
		CacheControl: anthropic.NewCacheControlEphemeralParam(),
	}}

	payload := string(promptBytes(req))
	block := anthropic.TextBlockParam{Text: payload}
	if len(payload) >= cacheThresholdBytes {
		block.CacheControl = anthropic.NewCacheControlEphemeralParam()
	}

	params := anthropic.MessageNewParams{
		Model:     anthropic.Model(model),
		MaxTokens: apiMaxTokens,
		System:    system,
		Messages: []anthropic.MessageParam{
			anthropic.NewUserMessage(anthropic.ContentBlockParamUnion{OfText: &block}),
		},
	}

	// Effort is set explicitly because the default differs by model: Opus 5.5
	// defaults to medium, and planning wants the model's full attention.
	params.OutputConfig.Effort = anthropic.OutputConfigEffortHigh
	if schema := outputSchemaMap(req); schema != nil {
		params.OutputConfig.Format = anthropic.JSONOutputFormatParam{Schema: schema}
	}
	return params
}

// stream runs one request and returns the assembled text and token usage.
func (a *AnthropicAPI) stream(
	ctx context.Context,
	client anthropic.Client,
	params anthropic.MessageNewParams,
	req contracts.GenerateRequest,
	emit func(contracts.GenerationEvent) error,
) (string, contracts.Usage, error) {
	var assembled bytes.Buffer
	var usage contracts.Usage

	stream := client.Messages.NewStreaming(ctx, params)
	defer stream.Close()

	for stream.Next() {
		event := stream.Current()
		switch event.Type {
		case "message_start":
			usage.InputTokens = int(event.Message.Usage.InputTokens)
		case "content_block_delta":
			text := event.Delta.Text
			if text == "" {
				continue
			}
			if err := appendBounded(&assembled, text, req.ClampMaxOutput()); err != nil {
				return "", usage, err
			}
			if err := emitEvent(emit, contracts.GenerationEvent{Kind: contracts.EventTextDelta, Text: text}); err != nil {
				return "", usage, err
			}
		case "message_delta":
			if out := int(event.Usage.OutputTokens); out != 0 {
				usage.OutputTokens = out
			}
		}
	}
	if err := stream.Err(); err != nil {
		return "", usage, err
	}
	if assembled.Len() == 0 {
		return "", usage, errors.New("the Anthropic API returned no content")
	}
	return assembled.String(), usage, nil
}

// isSchemaRejection reports whether a provider error is a complaint about the
// output schema rather than a failure of the call. Only a 400 naming the
// schema or the output format qualifies: a rate limit or a network error must
// not silently drop the constraint and return unvalidated output.
func isSchemaRejection(err error) bool {
	var apiErr *anthropic.Error
	if !errors.As(err, &apiErr) || apiErr.StatusCode != 400 {
		return false
	}
	message := strings.ToLower(apiErr.Error())
	return strings.Contains(message, "schema") || strings.Contains(message, "output_config") || strings.Contains(message, "format")
}
