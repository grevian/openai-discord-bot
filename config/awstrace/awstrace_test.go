package awstrace

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
)

type rtFunc func(*http.Request) (*http.Response, error)

func (f rtFunc) Do(r *http.Request) (*http.Response, error) { return f(r) }

func newConfig(t *testing.T, status int, body string) (aws.Config, *tracetest.InMemoryExporter, *http.Header) {
	t.Helper()
	exp := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exp))
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })

	var seen http.Header
	cfg := aws.Config{
		Region:      "ca-central-1",
		Credentials: credentials.NewStaticCredentialsProvider("id", "secret", ""),
		HTTPClient: rtFunc(func(r *http.Request) (*http.Response, error) {
			seen = r.Header.Clone()
			return &http.Response{
				StatusCode: status,
				Header:     http.Header{"X-Amzn-Requestid": {"req-123"}, "X-Amz-Request-Id": {"req-123"}, "Content-Type": {"application/x-amz-json-1.0"}},
				Body:       io.NopCloser(bytes.NewBufferString(body)),
				Request:    r,
			}, nil
		}),
		RetryMaxAttempts: 1,
	}
	appendWith(&cfg.APIOptions, tp, propagation.TraceContext{})
	return cfg, exp, &seen
}

func attrMap(kvs []attribute.KeyValue) map[string]attribute.Value {
	m := map[string]attribute.Value{}
	for _, kv := range kvs {
		m[string(kv.Key)] = kv.Value
	}
	return m
}

func TestS3SpanAttributes(t *testing.T) {
	cfg, exp, _ := newConfig(t, 200, "")
	_, err := s3.NewFromConfig(cfg).PutObject(context.Background(), &s3.PutObjectInput{
		Bucket: aws.String("bkt"), Key: aws.String("img.png"), Body: bytes.NewReader([]byte("x")),
	})
	if err != nil {
		t.Fatal(err)
	}
	spans := exp.GetSpans()
	if len(spans) != 1 {
		t.Fatalf("got %d spans, want 1", len(spans))
	}
	s := spans[0]
	if s.Name != "S3.PutObject" || s.SpanKind != trace.SpanKindClient {
		t.Errorf("name/kind = %q/%v", s.Name, s.SpanKind)
	}
	a := attrMap(s.Attributes)
	want := map[string]string{
		"rpc.system.name": "aws-api", "rpc.method": "S3/PutObject", "aws.region": "ca-central-1",
		"aws.s3.bucket": "bkt", "aws.s3.key": "img.png", "aws.request_id": "req-123",
	}
	for k, v := range want {
		if got := a[k].AsString(); got != v {
			t.Errorf("%s = %q, want %q", k, got, v)
		}
	}
	if got := a["http.response.status_code"].AsInt64(); got != 200 {
		t.Errorf("status code = %d", got)
	}
}

func TestDynamoDBSpanAndPropagation(t *testing.T) {
	cfg, exp, hdr := newConfig(t, 200, "{}")
	_, err := dynamodb.NewFromConfig(cfg).GetItem(context.Background(), &dynamodb.GetItemInput{TableName: aws.String("hist"), Key: map[string]types.AttributeValue{"id": &types.AttributeValueMemberS{Value: "1"}}})
	if err != nil {
		t.Fatal(err)
	}
	spans := exp.GetSpans()
	if len(spans) != 1 || spans[0].Name != "DynamoDB.GetItem" {
		t.Fatalf("spans = %v", spans)
	}
	a := attrMap(spans[0].Attributes)
	if a["rpc.method"].AsString() != "DynamoDB/GetItem" || a["db.system.name"].AsString() != "aws.dynamodb" {
		t.Errorf("attrs = %v", spans[0].Attributes)
	}
	if got := a["aws.dynamodb.table_names"].AsStringSlice(); len(got) != 1 || got[0] != "hist" {
		t.Errorf("table names = %v", got)
	}
	if hdr.Get("Traceparent") == "" {
		t.Error("traceparent header not injected")
	}
}

func TestErrorMarksSpan(t *testing.T) {
	cfg, exp, _ := newConfig(t, 400, `{"__type":"com.amazonaws.dynamodb.v20120810#ResourceNotFoundException","message":"nope"}`)
	_, err := dynamodb.NewFromConfig(cfg).GetItem(context.Background(), &dynamodb.GetItemInput{TableName: aws.String("hist"), Key: map[string]types.AttributeValue{"id": &types.AttributeValueMemberS{Value: "1"}}})
	if err == nil {
		t.Fatal("expected error")
	}
	spans := exp.GetSpans()
	if len(spans) != 1 || spans[0].Status.Code != 1 /* Error */ {
		t.Fatalf("spans = %+v", spans)
	}
	if got := attrMap(spans[0].Attributes)["http.response.status_code"].AsInt64(); got != 400 {
		t.Errorf("status = %d", got)
	}
}
