package ingester

import (
	"context"
	"testing"

	"github.com/opentracing/opentracing-go"
	"github.com/opentracing/opentracing-go/mocktracer"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/weaveworks/common/user"

	"github.com/cortexproject/cortex/pkg/cortexpb"
)

// streamWriteReqWithTrace builds a request carrying its own trace context, as the client does.
func streamWriteReqWithTrace(t *testing.T, tracer opentracing.Tracer, tenantID, metricName string) (*cortexpb.StreamWriteRequest, int) {
	t.Helper()

	span := tracer.StartSpan("Distributor.Push")

	req := &cortexpb.StreamWriteRequest{TenantID: tenantID, Request: makeWriteReq(metricName)}
	require.NoError(t, cortexpb.InjectSpanIntoStreamWriteRequest(tracer, span, req))
	return req, span.Context().(mocktracer.MockSpanContext).TraceID
}

// TestPushStream_TracesEachRequestSeparately checks that each push is traced as part of the
// write request it belongs to, and not of the connection it happened to arrive on.
func TestPushStream_TracesEachRequestSeparately(t *testing.T) {
	previousTracer := opentracing.GlobalTracer()
	tracer := mocktracer.New()
	opentracing.SetGlobalTracer(tracer)
	t.Cleanup(func() { opentracing.SetGlobalTracer(previousTracer) })

	ing := newTestIngester(t)

	// The interceptor puts the span of the connection in the stream context.
	streamCtx := user.InjectOrgID(context.Background(), "ingester-127.0.0.1-9095-stream-push-worker-0")
	streamSpan := tracer.StartSpan("/cortex.Ingester/PushStream")
	streamCtx = opentracing.ContextWithSpan(streamCtx, streamSpan)

	tracedReq, tracedTraceID := streamWriteReqWithTrace(t, tracer, "user-1", "metric_one")
	// A client that predates this propagation carries no trace context.
	legacyReq := &cortexpb.StreamWriteRequest{TenantID: "user-2", Request: makeWriteReq("metric_two")}

	srv := &pushStreamServer{ctx: streamCtx, requests: []*cortexpb.StreamWriteRequest{tracedReq, legacyReq}}
	require.NoError(t, ing.PushStream(srv))

	byTrace := map[int][]string{}
	for _, span := range tracer.FinishedSpans() {
		byTrace[span.SpanContext.TraceID] = append(byTrace[span.SpanContext.TraceID], span.OperationName)
	}

	// A push joins the trace of its own write request.
	assert.Equal(t, []string{"Ingester.Push", "Ingester.PushStreamRequest"}, byTrace[tracedTraceID])

	var parentless []*mocktracer.MockSpan
	for _, span := range tracer.FinishedSpans() {
		if span.OperationName == "Ingester.Push" && span.ParentID == 0 {
			parentless = append(parentless, span)
		}
	}
	require.Len(t, parentless, 1)
	assert.NotContains(t, byTrace, streamSpan.Context().(mocktracer.MockSpanContext).TraceID)
}
