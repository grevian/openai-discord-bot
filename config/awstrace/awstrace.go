// Package awstrace adds OpenTelemetry client spans to AWS SDK v2 operations.
//
// It replaces the deprecated otelaws contrib package with a small smithy
// middleware that emits the same span name ("<ServiceID>.<Operation>"), kind
// and core attributes (rpc.system.name, rpc.method, aws.region,
// aws.request_id, http.response.status_code), plus the S3 bucket/key and
// DynamoDB table name attributes this service relies on.
package awstrace

import (
	"context"
	"reflect"

	awsmiddleware "github.com/aws/aws-sdk-go-v2/aws/middleware"
	"github.com/aws/smithy-go/middleware"
	smithyhttp "github.com/aws/smithy-go/transport/http"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	semconv "go.opentelemetry.io/otel/semconv/v1.43.0"
	"go.opentelemetry.io/otel/trace"
)

// ScopeName is the instrumentation scope name of the spans created.
const ScopeName = "openai-discord-bot/config/awstrace"

const (
	s3ServiceID       = "S3"
	dynamoDBServiceID = "DynamoDB"
)

// AppendMiddlewares registers tracing on the given API options, typically
// aws.Config.APIOptions. The global tracer provider and propagator are
// resolved when called, so call it after they have been configured.
func AppendMiddlewares(apiOptions *[]func(*middleware.Stack) error) {
	appendWith(apiOptions, otel.GetTracerProvider(), otel.GetTextMapPropagator())
}

func appendWith(apiOptions *[]func(*middleware.Stack) error, tp trace.TracerProvider, prop propagation.TextMapPropagator) {
	m := tracer{tracer: tp.Tracer(ScopeName), propagator: prop}
	*apiOptions = append(*apiOptions, m.register)
}

type tracer struct {
	tracer     trace.Tracer
	propagator propagation.TextMapPropagator
}

func (t tracer) register(stack *middleware.Stack) error {
	// Start the span before everything else in Initialize so retries,
	// signing and serialization are covered by it; the service, operation
	// and region are only known to the context once the SDK's own
	// registration middleware has run, so we insert After it.
	if err := stack.Initialize.Add(middleware.InitializeMiddlewareFunc("OTelSpan", t.handleInitialize), middleware.After); err != nil {
		return err
	}
	if err := stack.Finalize.Add(middleware.FinalizeMiddlewareFunc("OTelInject", t.handleFinalize), middleware.After); err != nil {
		return err
	}
	return stack.Deserialize.Add(middleware.DeserializeMiddlewareFunc("OTelResponse", handleDeserialize), middleware.Before)
}

func (t tracer) handleInitialize(ctx context.Context, in middleware.InitializeInput, next middleware.InitializeHandler) (middleware.InitializeOutput, middleware.Metadata, error) {
	serviceID := awsmiddleware.GetServiceID(ctx)
	operation := awsmiddleware.GetOperationName(ctx)

	attrs := []attribute.KeyValue{
		semconv.RPCSystemNameKey.String("aws-api"),
		semconv.RPCMethod(serviceID + "/" + operation),
		attribute.String("aws.region", awsmiddleware.GetRegion(ctx)),
	}
	attrs = append(attrs, serviceAttributes(serviceID, in.Parameters)...)

	name := serviceID
	if operation != "" {
		name += "." + operation
	}
	ctx, span := t.tracer.Start(ctx, name,
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(attrs...))
	defer span.End()

	out, md, err := next.HandleInitialize(ctx, in)
	if err != nil {
		span.SetAttributes(semconv.ErrorType(err))
		span.SetStatus(codes.Error, err.Error())
	}
	return out, md, err
}

func (t tracer) handleFinalize(ctx context.Context, in middleware.FinalizeInput, next middleware.FinalizeHandler) (middleware.FinalizeOutput, middleware.Metadata, error) {
	if req, ok := in.Request.(*smithyhttp.Request); ok {
		t.propagator.Inject(ctx, propagation.HeaderCarrier(req.Header))
	}
	return next.HandleFinalize(ctx, in)
}

func handleDeserialize(ctx context.Context, in middleware.DeserializeInput, next middleware.DeserializeHandler) (middleware.DeserializeOutput, middleware.Metadata, error) {
	out, md, err := next.HandleDeserialize(ctx, in)
	if resp, ok := out.RawResponse.(*smithyhttp.Response); ok {
		span := trace.SpanFromContext(ctx)
		span.SetAttributes(semconv.HTTPResponseStatusCode(resp.StatusCode))
		if id, ok := awsmiddleware.GetRequestIDMetadata(md); ok {
			span.SetAttributes(attribute.String("aws.request_id", id))
		}
	}
	return out, md, err
}

func serviceAttributes(serviceID string, params any) []attribute.KeyValue {
	var attrs []attribute.KeyValue
	switch serviceID {
	case s3ServiceID:
		if v, ok := stringField(params, "Bucket"); ok {
			attrs = append(attrs, semconv.AWSS3Bucket(v))
		}
		if v, ok := stringField(params, "Key"); ok {
			attrs = append(attrs, semconv.AWSS3Key(v))
		}
	case dynamoDBServiceID:
		attrs = append(attrs, semconv.DBSystemNameAWSDynamoDB)
		if v, ok := stringField(params, "TableName"); ok {
			attrs = append(attrs, semconv.AWSDynamoDBTableNames(v))
		}
	}
	return attrs
}

// stringField returns the non-empty *string field name of a (pointer to a)
// struct, tolerating nil inputs. Reflection avoids enumerating every
// operation input type.
func stringField(params any, name string) (string, bool) {
	v := reflect.ValueOf(params)
	if v.Kind() == reflect.Pointer {
		if v.IsNil() {
			return "", false
		}
		v = v.Elem()
	}
	if v.Kind() != reflect.Struct {
		return "", false
	}
	f := v.FieldByName(name)
	if !f.IsValid() || f.Kind() != reflect.Pointer || f.IsNil() || f.Elem().Kind() != reflect.String {
		return "", false
	}
	s := f.Elem().String()
	return s, s != ""
}
