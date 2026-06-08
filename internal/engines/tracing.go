package engines

import (
	"context"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

// tracer is package-scoped so every engine driver shares the same
// instrumentation-library name. The global TracerProvider (set in
// internal/controlplane/tracing.go) decides whether spans are exported
// or NoOp'd, so there's zero overhead when OTLP isn't configured.
var tracer trace.Tracer = otel.Tracer("github.com/hadihonarvar/flock/internal/engines")

// startChatSpan opens a "<driver>.Chat" span with the standard attributes
// every Flock engine driver should record: driver name, model id, target
// endpoint, message count. The returned span is closed by the caller — by
// convention drivers `defer span.End()` inside the streaming goroutine so
// the span duration covers the full streamed response, not just the
// time-to-first-byte.
//
// Usage:
//
//	ctx, sp := startChatSpan(ctx, "vllm", req.Model, v.endpoint, len(req.Messages))
//	// ... synchronous errors → sp.SetStatus(codes.Error, "..."); sp.End(); return
//	go func() {
//	    defer sp.End()
//	    ...
//	    sp.SetTokens(promptTokens, completionTokens)
//	    sp.SetStatus(codes.Ok, "")
//	}()
func startChatSpan(ctx context.Context, driver, model, endpoint string, messageCount int) (context.Context, *chatSpan) {
	ctx, sp := tracer.Start(ctx, driver+".Chat",
		trace.WithAttributes(
			attribute.String("flock.engine", driver),
			attribute.String("flock.model", model),
			attribute.String("flock.engine.endpoint", endpoint),
			attribute.Int("flock.messages", messageCount),
		),
	)
	return ctx, &chatSpan{Span: sp}
}

// chatSpan wraps trace.Span with two helpers we set per request — the HTTP
// status from the engine response (if any) and the prompt + completion
// token counts from the final stream event. Keeping these on a small
// wrapper avoids repeating the SetAttributes calls in every driver.
type chatSpan struct{ trace.Span }

func (s *chatSpan) SetHTTPStatus(code int) {
	s.SetAttributes(attribute.Int("http.status_code", code))
}

func (s *chatSpan) SetTokens(prompt, completion int) {
	s.SetAttributes(
		attribute.Int("flock.tokens.prompt", prompt),
		attribute.Int("flock.tokens.completion", completion),
	)
}

// markError tags the span and records the error in one call, returning
// codes.Error so the driver can pass it inline:
//
//	return sp.markError("http do", err)
func (s *chatSpan) markError(msg string, err error) codes.Code {
	s.SetStatus(codes.Error, msg)
	if err != nil {
		s.RecordError(err)
	}
	return codes.Error
}
