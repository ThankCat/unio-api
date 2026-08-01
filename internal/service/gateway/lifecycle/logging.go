package lifecycle

import (
	"context"
	"errors"
	"math/big"
	"net/url"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"

	"github.com/ThankCat/unio-gateway/internal/core/adapter"
	"github.com/ThankCat/unio-gateway/internal/core/requestlog"
	"github.com/ThankCat/unio-gateway/internal/core/routing"
	"github.com/ThankCat/unio-gateway/internal/core/usage"
	"github.com/ThankCat/unio-gateway/internal/platform/failure"
	"github.com/ThankCat/unio-gateway/internal/platform/httpx"
	"github.com/ThankCat/unio-gateway/internal/platform/logging"
	"github.com/ThankCat/unio-gateway/internal/platform/observability/logfields"
)

func (l *RequestLifecycle) requestLogContext(ctx context.Context, request requestlog.RequestRecord) []zap.Field {
	fields := make([]zap.Field, 0, 16)
	if propagated, ok := logfields.FromContext(ctx); ok {
		fields = append(fields, propagated.ZapFields()...)
	} else if traceID := httpx.RequestID(ctx); traceID != "" {
		fields = append(fields, zap.String("trace_id", traceID))
	}
	if request.RequestID != "" {
		fields = append(fields, zap.String("request_id", request.RequestID))
	}
	if request.UserID != 0 {
		fields = append(fields, zap.Int64("user_id", request.UserID))
	}
	if request.APIKeyID != 0 {
		fields = append(fields, zap.Int64("api_key_id", request.APIKeyID))
	}
	if request.RequestedModelID != "" {
		fields = append(fields, zap.String("model", request.RequestedModelID))
	}
	return dedupeLogFields(fields)
}

func candidateLogContext(candidate routing.ChatRouteCandidate) []zap.Field {
	fields := []zap.Field{
		zap.Int64("model_id", candidate.ModelDBID),
		zap.String("route_name", candidate.RouteName),
		zap.Int64("provider_id", candidate.ProviderID),
		zap.String("provider_slug", candidate.Channel.ProviderSlug),
		zap.Int64("channel_id", candidate.Channel.ID),
		zap.String("channel_name", candidate.Channel.Name),
		zap.String("upstream_model", candidate.UpstreamModel),
		zap.String("adapter_key", candidate.AdapterKey),
	}
	return nonEmptyLogFields(fields)
}

func attemptLogContext(request requestlog.RequestRecord, attempt requestlog.AttemptRecord, stream bool) []zap.Field {
	mode := "non_stream"
	if stream {
		mode = "stream"
	}
	return []zap.Field{
		zap.Int64("attempt_id", attempt.ID),
		zap.Int("attempt_index", attempt.AttemptIndex),
		zap.String("endpoint", string(attempt.UpstreamEndpoint)),
		zap.String("mode", mode),
		zap.String("ingress_protocol", string(request.IngressProtocol)),
		zap.String("upstream_protocol", string(attempt.UpstreamProtocol)),
	}
}

func (l *RequestLifecycle) completeAttemptLogContext(
	ctx context.Context,
	request requestlog.RequestRecord,
	attempt requestlog.AttemptRecord,
	candidate routing.ChatRouteCandidate,
	stream bool,
) []zap.Field {
	fields := l.requestLogContext(ctx, request)
	fields = append(fields, candidateLogContext(candidate)...)
	fields = append(fields, attemptLogContext(request, attempt, stream)...)
	return dedupeLogFields(fields)
}

func (l *RequestLifecycle) attemptTimingHooks(
	ctx context.Context,
	request requestlog.RequestRecord,
	attempt requestlog.AttemptRecord,
	candidate routing.ChatRouteCandidate,
	stream bool,
) AttemptTimingHooks {
	return AttemptTimingHooks{
		TransportStarted: func(facts AttemptTimingFacts) {
			fields := l.completeAttemptLogContext(ctx, request, attempt, candidate, stream)
			fields = append(fields,
				zap.String("upstream_url_origin", safeUpstreamOrigin(candidate.Channel.Origin)),
				zap.Int64("first_token_timeout_ms", candidate.Channel.FirstTokenTimeout.Milliseconds()),
				zap.Int64("response_timeout_ms", candidate.Channel.ResponseTimeout.Milliseconds()),
			)
			logging.Debug(l.logger, "upstream", "attempt", "upstream attempt started", fields...)
		},
		ResponseHeadersReceived: func(facts AttemptTimingFacts) {
			fields := l.completeAttemptLogContext(ctx, request, attempt, candidate, stream)
			if facts.UpstreamStatusCode != 0 {
				fields = append(fields, zap.Int("status_code", facts.UpstreamStatusCode))
			}
			if facts.UpstreamRequestID != "" {
				fields = append(fields, zap.String("upstream_request_id", facts.UpstreamRequestID))
			}
			if elapsed := facts.ResponseHeaderMs(); elapsed != nil {
				fields = append(fields, zap.Int64("response_header_ms", *elapsed))
			}
			logging.Debug(l.logger, "upstream", "attempt", "upstream response headers received", fields...)
		},
	}
}

func (l *RequestLifecycle) LogLeadingStreamEvent(
	ctx context.Context,
	request requestlog.RequestRecord,
	attempt requestlog.AttemptRecord,
	candidate routing.ChatRouteCandidate,
	meta StreamChunkMeta,
	timing AttemptTimingFacts,
	eventIndex int,
	eventBytes int,
) {
	fields := l.completeAttemptLogContext(ctx, request, attempt, candidate, true)
	fields = append(fields,
		zap.Int("event_index", eventIndex),
		zap.String("protocol_event_type", meta.ProtocolEventType),
		zap.Int("event_bytes", eventBytes),
		zap.String("classification", meta.Classification),
		zap.Bool("effective_token", false),
	)
	if timing.UpstreamRequestID != "" {
		fields = append(fields, zap.String("upstream_request_id", timing.UpstreamRequestID))
	}
	logging.Debug(l.logger, "upstream", "attempt", "upstream leading event classified", nonEmptyLogFields(fields)...)
}

func (l *RequestLifecycle) LogUpstreamFirstToken(
	ctx context.Context,
	request requestlog.RequestRecord,
	attempt requestlog.AttemptRecord,
	candidate routing.ChatRouteCandidate,
	meta StreamChunkMeta,
	timing AttemptTimingFacts,
	leadingEventCount int,
	leadingEventBytes int,
) {
	fields := l.completeAttemptLogContext(ctx, request, attempt, candidate, true)
	fields = append(fields,
		zap.String("token_kind", meta.TokenKind),
		zap.String("protocol_event_type", meta.ProtocolEventType),
		zap.Int("leading_event_count", leadingEventCount),
		zap.Int("leading_event_bytes", leadingEventBytes),
	)
	if elapsed := timing.FirstTokenMs(); elapsed != nil {
		fields = append(fields, zap.Int64("upstream_ttft_ms", *elapsed))
		logfields.SetUpstreamTTFT(ctx, *elapsed)
	}
	if timing.UpstreamRequestID != "" {
		fields = append(fields, zap.String("upstream_request_id", timing.UpstreamRequestID))
	}
	logging.Debug(l.logger, "upstream", "attempt", "upstream first token received", nonEmptyLogFields(fields)...)
}

type AttemptStreamStats struct {
	EventCount int
	Bytes      int
}

func (l *RequestLifecycle) LogUpstreamAttemptResult(
	ctx context.Context,
	request requestlog.RequestRecord,
	attempt requestlog.AttemptRecord,
	candidate routing.ChatRouteCandidate,
	stream bool,
	timing AttemptTimingFacts,
	facts *adapter.ResponseFacts,
	streamStats AttemptStreamStats,
	err error,
	fallbackAllowed bool,
	deliveredToClient bool,
) {
	fields := l.completeAttemptLogContext(ctx, request, attempt, candidate, stream)
	statusCode, upstreamRequestID := resolveAttemptLogMetadata(timing, facts, err)
	if statusCode != 0 {
		fields = append(fields, zap.Int("status_code", statusCode))
	}
	if upstreamRequestID != "" {
		fields = append(fields, zap.String("upstream_request_id", upstreamRequestID))
	}
	if timing.UpstreamStartedAt != nil && timing.UpstreamCompletedAt != nil {
		fields = append(fields, zap.Int64("duration_ms", nonNegativeDuration(*timing.UpstreamStartedAt, *timing.UpstreamCompletedAt).Milliseconds()))
	}
	if elapsed := timing.FirstTokenMs(); elapsed != nil {
		fields = append(fields, zap.Int64("upstream_ttft_ms", *elapsed))
	}
	if stream {
		fields = append(fields,
			zap.Int("stream_event_count", streamStats.EventCount),
			zap.Int("stream_bytes", streamStats.Bytes),
		)
	}
	if facts != nil {
		fields = append(fields,
			zap.String("finish_reason", string(facts.Finish.Class)),
			zap.Bool("final_usage_received", !facts.UsageSource.IsPartialEstimate()),
		)
		fields = append(fields, usageLogFields(facts.Usage, facts.UsageSource)...)
	}

	if err == nil && facts != nil {
		logging.Debug(l.logger, "upstream", "attempt", "upstream attempt completed", nonEmptyLogFields(fields)...)
		return
	}
	fields = append(fields,
		zap.Bool("fallback_allowed", fallbackAllowed),
		zap.Bool("delivered_to_client", deliveredToClient),
	)
	fields = append(fields, l.safeErrorLogFields(err, "upstream_attempt_failed", stream, timing)...)
	if errorsAreGatewayInternal(err) {
		logging.Error(l.logger, "upstream", "attempt", "upstream attempt failed", nonEmptyLogFields(fields)...)
		return
	}
	logging.Warn(l.logger, "upstream", "attempt", "upstream attempt failed", nonEmptyLogFields(fields)...)
}

func (l *RequestLifecycle) LogUpstreamResponseMissingBillableUsage(
	ctx context.Context,
	request requestlog.RequestRecord,
	attempt requestlog.AttemptRecord,
	candidate routing.ChatRouteCandidate,
	timing AttemptTimingFacts,
) {
	fields := l.completeAttemptLogContext(ctx, request, attempt, candidate, false)
	statusCode, upstreamRequestID := resolveAttemptLogMetadata(timing, nil, nil)
	if statusCode != 0 {
		fields = append(fields, zap.Int("status_code", statusCode))
	}
	if upstreamRequestID != "" {
		fields = append(fields, zap.String("upstream_request_id", upstreamRequestID))
	}
	fields = append(fields, zap.Bool("cost_exposure_required", true))
	logging.Warn(l.logger, "upstream", "attempt", "upstream response missing billable usage", nonEmptyLogFields(fields)...)
}

func (l *RequestLifecycle) LogRoutingFallback(
	ctx context.Context,
	request requestlog.RequestRecord,
	attempt requestlog.AttemptRecord,
	candidate routing.ChatRouteCandidate,
	reason string,
	nextCandidateAvailable bool,
	deliveredToClient bool,
	transparent bool,
	toEndpoint requestlog.UpstreamEndpoint,
) {
	logfields.IncrementFallbackCount(ctx)
	fields := l.completeAttemptLogContext(ctx, request, attempt, candidate, request.Stream)
	fields = append(fields,
		zap.String("reason", reason),
		zap.Bool("next_candidate_available", nextCandidateAvailable),
		zap.Bool("delivered_to_client", deliveredToClient),
	)
	if transparent {
		fields = append(fields,
			zap.String("from_endpoint", string(attempt.UpstreamEndpoint)),
			zap.String("to_endpoint", string(toEndpoint)),
		)
		logging.Warn(l.logger, "routing", "fallback", "transparent fallback triggered", nonEmptyLogFields(fields)...)
		return
	}
	logging.Warn(l.logger, "routing", "fallback", "routing fallback triggered", nonEmptyLogFields(fields)...)
}

func (l *RequestLifecycle) LogRoutingFallbackExhausted(
	ctx context.Context,
	request requestlog.RequestRecord,
	attemptCount int,
	lastChannelID int64,
	reason string,
	err error,
) {
	fields := l.requestLogContext(ctx, request)
	fields = append(fields,
		zap.Int("attempt_count", attemptCount),
		zap.Int64("last_channel_id", lastChannelID),
		zap.String("reason", reason),
	)
	fields = append(fields, l.safeErrorLogFields(err, "routing_fallback_exhausted", request.Stream, AttemptTimingFacts{})...)
	logging.Warn(l.logger, "routing", "fallback", "routing fallback exhausted", nonEmptyLogFields(fields)...)
}

func (l *RequestLifecycle) LogCapacityWaitStarted(ctx context.Context, request requestlog.RequestRecord, candidateCount int, budget time.Duration) {
	fields := l.requestLogContext(ctx, request)
	fields = append(fields,
		zap.Int("candidate_count", candidateCount),
		zap.Int64("wait_budget_ms", budget.Milliseconds()),
	)
	logging.Debug(l.logger, "admission", "capacity_wait", "capacity wait started", fields...)
}

func (l *RequestLifecycle) LogCapacityWaitCompleted(ctx context.Context, request requestlog.RequestRecord, waited time.Duration, outcome capacityWaitOutcome) {
	if outcome != capacityWaitNotWaited {
		logfields.SetCapacityWait(ctx, waited.Milliseconds())
	}
	fields := l.requestLogContext(ctx, request)
	fields = append(fields,
		zap.Int64("waited_ms", waited.Milliseconds()),
		zap.String("outcome", string(outcome)),
	)
	if outcome == capacityWaitCapacityExhausted {
		logging.Warn(l.logger, "admission", "capacity_wait", "capacity wait completed", fields...)
		return
	}
	logging.Debug(l.logger, "admission", "capacity_wait", "capacity wait completed", fields...)
}

func (l *RequestLifecycle) LogSettlementResult(
	ctx context.Context,
	request requestlog.RequestRecord,
	attempt requestlog.AttemptRecord,
	candidate routing.ChatRouteCandidate,
	authorization ChatAuthorization,
	facts adapter.ResponseFacts,
	gatewayFirstTokenDelivered bool,
	err error,
) {
	partial := facts.UsageSource.IsPartialEstimate()
	fields := l.completeAttemptLogContext(ctx, request, attempt, candidate, request.Stream)
	fields = append(fields,
		zap.Int64("reservation_id", authorization.ReservationID),
		zap.String("currency", authorization.Currency),
		zap.String("authorized_amount", numericLogString(authorization.AuthorizedAmount)),
		zap.String("finish_class", string(facts.Finish.Class)),
	)
	fields = append(fields, usageLogFields(facts.Usage, facts.UsageSource)...)
	if err != nil {
		recoveryScheduled := IsChatSettlementRecoveryScheduled(err)
		fields = append(fields, zap.Bool("recovery_scheduled", recoveryScheduled))
		fields = append(fields, l.safeErrorLogFields(err, string(failure.CodeGatewayChatSettlementFailed), request.Stream, AttemptTimingFacts{})...)
		logfields.SetSettlementStatus(ctx, "failed")
		logfields.SetCompletion(ctx, "error", FailureCodeOrFallback(err, string(failure.CodeGatewayChatSettlementFailed)))
		logging.Error(l.logger, "billing", "settlement", "billing settlement failed", nonEmptyLogFields(fields)...)
		if recoveryScheduled {
			logging.Warn(l.logger, "billing", "recovery", "settlement recovery scheduled",
				append(fields, zap.String("recovery_state", "pending"))...,
			)
		} else {
			logging.Error(l.logger, "billing", "recovery", "settlement recovery scheduling failed",
				nonEmptyLogFields(fields)...,
			)
		}
		return
	}
	logfields.SetSettlementStatus(ctx, "completed")
	if partial {
		fields = append(fields,
			zap.String("reason", facts.Finish.RawReason),
			zap.Bool("gateway_first_token_delivered", gatewayFirstTokenDelivered),
		)
		logfields.SetCompletion(ctx, "warning", "")
		logging.Warn(l.logger, "billing", "settlement", "partial billing settlement completed", nonEmptyLogFields(fields)...)
		return
	}
	mode := "full"
	if facts.Usage.OutputTokensTotal.IsKnown() && facts.Usage.OutputTokensTotal.Value == 0 {
		mode = "zero_output"
	}
	fields = append(fields, zap.String("mode", mode))
	logging.Debug(l.logger, "billing", "settlement", "billing settlement completed", nonEmptyLogFields(fields)...)
}

func (l *RequestLifecycle) safeErrorLogFields(err error, fallbackCode string, stream bool, timing AttemptTimingFacts) []zap.Field {
	code := FailureCodeOrFallback(err, fallbackCode)
	fields := []zap.Field{
		zap.String("error_code", code),
		zap.String("error_message", l.safeMessageFor(code)),
	}
	if category, ok := adapter.UpstreamCategoryOf(err); ok {
		fields = append(fields, zap.String("error_category", string(category)))
	}
	if phase := TimeoutPhaseOf(err, stream, timing); phase != "" {
		fields = append(fields, zap.String("error_phase", string(phase)))
	}
	return nonEmptyLogFields(fields)
}

func usageLogFields(facts usage.Facts, source usage.Source) []zap.Field {
	fields := []zap.Field{zap.String("usage_source", string(source))}
	appendKnown := func(key string, count usage.TokenCount) {
		if count.IsKnown() {
			fields = append(fields, zap.Int64(key, count.Value))
		}
	}
	if facts.UncachedInputTokens.IsKnown() || facts.CacheReadInputTokens.IsKnown() || facts.CacheWrite5mInputTokens.IsKnown() || facts.CacheWrite1hInputTokens.IsKnown() || facts.CacheWrite30mInputTokens.IsKnown() {
		fields = append(fields, zap.Int64("input_tokens",
			knownTokenValue(facts.UncachedInputTokens)+knownTokenValue(facts.CacheReadInputTokens)+
				knownTokenValue(facts.CacheWrite5mInputTokens)+knownTokenValue(facts.CacheWrite1hInputTokens)+knownTokenValue(facts.CacheWrite30mInputTokens)))
	}
	appendKnown("cache_read_input_tokens", facts.CacheReadInputTokens)
	if facts.CacheWrite5mInputTokens.IsKnown() || facts.CacheWrite1hInputTokens.IsKnown() || facts.CacheWrite30mInputTokens.IsKnown() {
		fields = append(fields, zap.Int64("cache_write_input_tokens",
			knownTokenValue(facts.CacheWrite5mInputTokens)+knownTokenValue(facts.CacheWrite1hInputTokens)+knownTokenValue(facts.CacheWrite30mInputTokens)))
	}
	appendKnown("output_tokens", facts.OutputTokensTotal)
	appendKnown("reasoning_tokens", facts.ReasoningOutputTokens)
	if total, ok := facts.ActualTotalTokens(); ok {
		fields = append(fields, zap.Int64("total_tokens", total))
	}
	return fields
}

func knownTokenValue(count usage.TokenCount) int64 {
	if count.IsKnown() {
		return count.Value
	}
	return 0
}

func resolveAttemptLogMetadata(timing AttemptTimingFacts, facts *adapter.ResponseFacts, err error) (int, string) {
	statusCode := timing.UpstreamStatusCode
	requestID := timing.UpstreamRequestID
	if facts != nil {
		if facts.Metadata.StatusCode != 0 {
			statusCode = facts.Metadata.StatusCode
		}
		if facts.Metadata.RequestID != "" {
			requestID = facts.Metadata.RequestID
		}
	}
	if metadata, ok := adapter.UpstreamMetadataOf(err); ok {
		if metadata.StatusCode != 0 {
			statusCode = metadata.StatusCode
		}
		if metadata.RequestID != "" {
			requestID = metadata.RequestID
		}
	}
	return statusCode, requestID
}

func errorsAreGatewayInternal(err error) bool {
	return err != nil && (strings.Contains(string(failure.CodeOf(err)), "settlement") ||
		strings.Contains(string(failure.CodeOf(err)), "breaker_store") ||
		strings.Contains(string(failure.CodeOf(err)), "runtime_feedback") ||
		errors.Is(err, errAttemptPermitFinish) || errors.Is(err, ErrAttemptRuntimeFeedback))
}

func safeUpstreamOrigin(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return "invalid"
	}
	// Provider paths are configuration, not protocol templates. They can contain
	// tenant IDs or credentials, so logs retain only the network origin.
	return (&url.URL{Scheme: parsed.Scheme, Host: parsed.Host}).String()
}

func nonNegativeDuration(start, end time.Time) time.Duration {
	if end.Before(start) {
		return 0
	}
	return end.Sub(start)
}

func nonEmptyLogFields(fields []zap.Field) []zap.Field {
	out := fields[:0]
	for _, field := range fields {
		if field.Type == zapcore.StringType && field.String == "" {
			continue
		}
		out = append(out, field)
	}
	return out
}

func dedupeLogFields(fields []zap.Field) []zap.Field {
	last := make(map[string]int, len(fields))
	for index, field := range fields {
		last[field.Key] = index
	}
	out := make([]zap.Field, 0, len(last))
	for index, field := range fields {
		if last[field.Key] == index {
			out = append(out, field)
		}
	}
	return out
}

func numericLogString(value pgtype.Numeric) string {
	if !value.Valid || value.NaN || value.InfinityModifier != pgtype.Finite || value.Int == nil {
		return "0"
	}
	negative := value.Int.Sign() < 0
	digits := new(big.Int).Abs(value.Int).String()
	exponent := int(value.Exp)
	var formatted string
	switch {
	case exponent == 0:
		formatted = digits
	case exponent > 0:
		formatted = digits + strings.Repeat("0", exponent)
	default:
		scale := -exponent
		if len(digits) <= scale {
			digits = strings.Repeat("0", scale-len(digits)+1) + digits
		}
		point := len(digits) - scale
		formatted = digits[:point] + "." + digits[point:]
	}
	if strings.Contains(formatted, ".") {
		formatted = strings.TrimRight(strings.TrimRight(formatted, "0"), ".")
	}
	if formatted == "" {
		formatted = "0"
	}
	if negative && formatted != "0" {
		formatted = "-" + formatted
	}
	return formatted
}
