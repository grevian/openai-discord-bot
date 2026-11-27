package bot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	"github.com/openai/openai-go/v3/responses"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

// aiOperation says what an OpenAI call is for. It is the `openai.operation`
// attribute on the call's span.
type aiOperation string

const (
	opChat      aiOperation = "chat"
	opDraw      aiOperation = "draw"
	opEdit      aiOperation = "edit"
	opCondense  aiOperation = "condense"
	opSummarize aiOperation = "summarize"
	opDispatch  aiOperation = "dispatch"
)

// modelPrice is US dollars per one million tokens.
type modelPrice struct{ inputPerM, outputPerM float64 }

// modelPrices is the price table behind the `openai.estimated_cost_usd`
// attribute. UPDATE IT when models or prices change (models.go names the
// models). Source: OpenAI's pricing page, https://developers.openai.com/api/docs/pricing,
// cross-checked on 2026-11-18 against third-party aggregators
// (aipricing.guru, pricepertoken.com); the official page was not read directly,
// so verify before trusting the numbers. Simplifications: cached-input
// discounts are ignored (cost is an upper bound), and image input tokens
// (text and image mixed) are all priced at the image-input rate. A model
// missing from this table gets no cost attribute rather than a wrong one.
var modelPrices = map[string]modelPrice{
	string(chatModel): {inputPerM: 0.10, outputPerM: 0.50},
	string(imageGenerateModel): { // imageEditModel is the same model
		inputPerM: 8.00, outputPerM: 30.00},
}

// estimateCostUSD returns the estimated cost of a call, and false when the
// model has no entry in modelPrices.
func estimateCostUSD(model string, inputTokens, outputTokens int64) (float64, bool) {
	p, ok := modelPrices[model]
	if !ok {
		return 0, false
	}
	return (float64(inputTokens)*p.inputPerM + float64(outputTokens)*p.outputPerM) / 1e6, true
}

// attemptTracker is request middleware that records, across the SDK's retry
// loop, how many HTTP attempts were made and the last X-Request-Id seen.
type attemptTracker struct {
	mu        sync.Mutex
	attempts  int
	requestID string
}

func (t *attemptTracker) middleware(req *http.Request, next option.MiddlewareNext) (*http.Response, error) {
	t.mu.Lock()
	t.attempts++
	t.mu.Unlock()
	resp, err := next(req)
	if resp != nil {
		if id := resp.Header.Get("X-Request-Id"); id != "" {
			t.mu.Lock()
			t.requestID = id
			t.mu.Unlock()
		}
	}
	return resp, err
}

// startAICall opens the span for one OpenAI call and returns the option that
// feeds its tracker. The caller must pass the option to the SDK and call
// finish with the usage it got back (nil usage when there was none).
func startAICall(ctx context.Context, op aiOperation, genAIOp, model string) (context.Context, *aiCall, option.RequestOption) {
	ctx, span := otel.GetTracerProvider().Tracer("AIBot").Start(ctx, "openai "+string(op),
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(
			attribute.String("openai.operation", string(op)),
			attribute.String("gen_ai.provider.name", "openai"),
			attribute.String("gen_ai.operation.name", genAIOp),
			attribute.String("gen_ai.request.model", model),
		))
	c := &aiCall{span: span, model: model, tracker: &attemptTracker{}}
	return ctx, c, option.WithMiddleware(c.tracker.middleware)
}

type aiCall struct {
	span    trace.Span
	model   string
	tracker *attemptTracker
}

// finish records what came back and ends the span. responseID and
// responseModel may be empty; usage is only recorded when the API returned it.
func (c *aiCall) finish(err error, responseID, responseModel string, usage *tokenUsage) {
	defer c.span.End()
	c.tracker.mu.Lock()
	attempts, reqID := c.tracker.attempts, c.tracker.requestID
	c.tracker.mu.Unlock()
	if attempts > 0 {
		c.span.SetAttributes(attribute.Int("openai.retries", attempts-1), attribute.Int("openai.attempts", attempts))
	}
	if reqID != "" {
		c.span.SetAttributes(attribute.String("openai.request_id", reqID))
	}
	if responseID != "" {
		c.span.SetAttributes(attribute.String("gen_ai.response.id", responseID))
	}
	if responseModel != "" {
		c.span.SetAttributes(attribute.String("gen_ai.response.model", responseModel))
	}
	if usage != nil {
		c.span.SetAttributes(
			attribute.Int64("gen_ai.usage.input_tokens", usage.input),
			attribute.Int64("gen_ai.usage.output_tokens", usage.output),
		)
		if cost, ok := estimateCostUSD(c.model, usage.input, usage.output); ok {
			c.span.SetAttributes(attribute.Float64("openai.estimated_cost_usd", cost))
		}
	}
	if err != nil {
		c.span.RecordError(err)
		c.span.SetStatus(codes.Error, err.Error())
		var apiErr *openai.Error
		if errors.As(err, &apiErr) {
			c.span.SetAttributes(attribute.Int("http.response.status_code", apiErr.StatusCode))
		}
	}
}

type tokenUsage struct{ input, output int64 }

// responsesNew calls the Responses API inside an "openai <op>" span.
func (b *AIBot) responsesNew(ctx context.Context, op aiOperation, req responses.ResponseNewParams) (*responses.Response, error) {
	ctx, call, opt := startAICall(ctx, op, "chat", string(req.Model))
	call.setInputJSON(req.Input, req.Instructions.Value)
	resp, err := b.responses.New(ctx, req, opt)
	if err != nil || resp == nil {
		call.finish(err, "", "", nil)
		return resp, err
	}
	var u *tokenUsage
	if resp.Usage.TotalTokens > 0 {
		u = &tokenUsage{resp.Usage.InputTokens, resp.Usage.OutputTokens}
	}
	out := textMessage("assistant", resp.OutputText())
	out.FinishReason = "stop"
	if resp.Status == responses.ResponseStatusIncomplete {
		out.FinishReason = "length"
	}
	call.setOutput(out)
	call.finish(nil, resp.ID, string(resp.Model), u)
	return resp, nil
}

// decisionsNew calls the Decisions API inside an "openai dispatch" span. The
// SDK retries once: the caller falls back to chat on any failure.
func (b *AIBot) decisionsNew(ctx context.Context, req openai.DecisionNewParams) (*openai.Decision, error) {
	ctx, call, opt := startAICall(ctx, opDispatch, "chat", req.Model)
	call.setInputJSON(req.Input, "")
	resp, err := b.decisions.New(ctx, req, opt, option.WithMaxRetries(1))
	if err != nil || resp == nil {
		call.finish(err, "", "", nil)
		return resp, err
	}
	var u *tokenUsage
	if resp.Usage.TotalTokens > 0 {
		// Decision output is free, so report none: the price table charges
		// output tokens at the chat model's rate.
		u = &tokenUsage{input: resp.Usage.InputTokens}
	}
	if len(resp.Answers) > 0 {
		answers := make([]json.RawMessage, len(resp.Answers))
		for i, a := range resp.Answers {
			answers[i] = json.RawMessage(a.RawJSON())
		}
		out := textMessage("assistant", string(mustJSON(answers)))
		out.FinishReason = "stop"
		call.setOutput(out)
	}
	call.finish(nil, "", resp.Model, u)
	return resp, nil
}

// setImageOutput notes the result without the image bytes.
func setImageOutput(call *aiCall, resp *openai.ImagesResponse, err error) {
	if err != nil || resp == nil {
		return
	}
	out := textMessage("assistant", fmt.Sprintf("[%d image(s) generated; not recorded]", len(resp.Data)))
	out.FinishReason = "stop"
	call.setOutput(out)
}

func imageUsage(r *openai.ImagesResponse) *tokenUsage {
	if r == nil || r.Usage.TotalTokens == 0 {
		return nil
	}
	return &tokenUsage{r.Usage.InputTokens, r.Usage.OutputTokens}
}

// imagesGenerate calls the image generation API inside an "openai draw" span.
func (b *AIBot) imagesGenerate(ctx context.Context, req openai.ImageGenerateParams) (*openai.ImagesResponse, error) {
	ctx, call, opt := startAICall(ctx, opDraw, "image_generation", string(req.Model))
	call.setInput([]genAIMessage{textMessage("user", req.Prompt)}, nil)
	resp, err := b.images.Generate(ctx, req, opt)
	setImageOutput(call, resp, err)
	call.finish(err, "", "", imageUsage(resp))
	return resp, err
}

// imagesEdit calls the image edit API inside an "openai edit" span.
func (b *AIBot) imagesEdit(ctx context.Context, req openai.ImageEditParams) (*openai.ImagesResponse, error) {
	ctx, call, opt := startAICall(ctx, opEdit, "image_edit", string(req.Model))
	call.setInput([]genAIMessage{textMessage("user", req.Prompt+"\n[source image omitted]")}, nil)
	resp, err := b.images.Edit(ctx, req, opt)
	setImageOutput(call, resp, err)
	call.finish(err, "", "", imageUsage(resp))
	return resp, err
}
