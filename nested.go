package pinpoint

// NestedTracer returns a view of tracer for an instrumentation layer that
// found tracer already active in the context it was handed: a middleware
// installed twice, a framework middleware inside a wrapped handler, or
// compile-time instrumentation outside a manual wrapper. The view records
// span events, distributed tracing headers and errors on the existing span
// and ignores EndSpan, which belongs to the layer that created the span. The
// Java agent behaves the same way: DefaultTraceFactory.checkAndGet keeps the
// existing Trace ("already Trace Object exist") instead of starting a second
// transaction for one request.
//
// The plugins call it from their server entry points (pphttp.NewHttpServerTracer,
// ppgrpc's server interceptors); application code normally does not need it.
func NestedTracer(tracer Tracer) Tracer {
	if _, ok := tracer.(nestedTracer); ok {
		return tracer
	}
	return nestedTracer{tracer}
}

// IsNestedTracer reports whether tracer came from NestedTracer. The plugins
// use it to leave span-level request and response recording to the layer
// that owns the span.
func IsNestedTracer(tracer Tracer) bool {
	_, ok := tracer.(nestedTracer)
	return ok
}

type nestedTracer struct {
	Tracer
}

// EndSpan does nothing: the layer that created the span ends it.
func (t nestedTracer) EndSpan() {}

// NewSpanEvent keeps the returned tracer nested, so that a layer holding on
// to the result cannot end the span through it either.
func (t nestedTracer) NewSpanEvent(operationName string) Tracer {
	t.Tracer.NewSpanEvent(operationName)
	return t
}
