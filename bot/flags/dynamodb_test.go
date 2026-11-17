package flags

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
)

type fakeDDB struct {
	pages    [][]map[string]types.AttributeValue
	scans    []*dynamodb.ScanInput
	scanErr  error
	writes   []*dynamodb.TransactWriteItemsInput
	writeErr error
}

func (f *fakeDDB) Scan(_ context.Context, in *dynamodb.ScanInput, _ ...func(*dynamodb.Options)) (*dynamodb.ScanOutput, error) {
	f.scans = append(f.scans, in)
	if f.scanErr != nil {
		return nil, f.scanErr
	}
	page := len(f.scans) - 1
	out := &dynamodb.ScanOutput{Items: f.pages[page]}
	if page < len(f.pages)-1 {
		out.LastEvaluatedKey = map[string]types.AttributeValue{"pk": &types.AttributeValueMemberS{Value: "next"}}
	}
	return out, nil
}

func (f *fakeDDB) TransactWriteItems(_ context.Context, in *dynamodb.TransactWriteItemsInput, _ ...func(*dynamodb.Options)) (*dynamodb.TransactWriteItemsOutput, error) {
	f.writes = append(f.writes, in)
	return &dynamodb.TransactWriteItemsOutput{}, f.writeErr
}

func s(v string) types.AttributeValue { return &types.AttributeValueMemberS{Value: v} }

func str(t *testing.T, item map[string]types.AttributeValue, key string) string {
	t.Helper()
	v, ok := item[key].(*types.AttributeValueMemberS)
	if !ok {
		t.Fatalf("item %v: %s is not a string", item, key)
	}
	return v.Value
}

func TestDynamoLoadAllReadsCurrentItemsAcrossPages(t *testing.T) {
	at := time.Date(2026, 11, 1, 9, 30, 0, 0, time.UTC)
	ddb := &fakeDDB{pages: [][]map[string]types.AttributeValue{
		{{"pk": s("a"), "sk": s("current"), "value": s("true"), "updated_by": s("123"), "updated_at": s(at.Format(time.RFC3339Nano)), "prev_value": s("false")}},
		{{"pk": s("b"), "sk": s("current"), "value": s("7")}},
	}}
	got, err := newDynamoBackend(ddb, "flags-tbl").LoadAll(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(ddb.scans) != 2 || ddb.scans[1].ExclusiveStartKey == nil {
		t.Fatalf("expected two paged scans, got %d", len(ddb.scans))
	}
	if aws.ToString(ddb.scans[0].TableName) != "flags-tbl" || ddb.scans[0].FilterExpression == nil {
		t.Fatalf("scan must target the table and filter to current items: %+v", ddb.scans[0])
	}
	want := map[string]Value{
		"a": {Name: "a", Raw: "true", UpdatedBy: "123", UpdatedAt: at, PrevValue: "false"},
		"b": {Name: "b", Raw: "7"},
	}
	if len(got) != len(want) {
		t.Fatalf("got %+v", got)
	}
	for k, w := range want {
		g := got[k]
		if g.Name != w.Name || g.Raw != w.Raw || g.UpdatedBy != w.UpdatedBy || !g.UpdatedAt.Equal(w.UpdatedAt) || g.PrevValue != w.PrevValue {
			t.Errorf("%s = %+v, want %+v", k, g, w)
		}
	}
}

func TestDynamoLoadAllError(t *testing.T) {
	ddb := &fakeDDB{scanErr: errors.New("ResourceNotFoundException")}
	if _, err := newDynamoBackend(ddb, "t").LoadAll(context.Background()); err == nil {
		t.Fatal("a scan error must be returned")
	}
}

func TestDynamoPutWritesCurrentAndAuditTogether(t *testing.T) {
	ddb := &fakeDDB{}
	at := time.Date(2026, 11, 17, 12, 0, 0, 123_000_000, time.UTC)
	c := Change{Name: "edit_thread_context", Old: "false", New: "true", By: "123456789012345678", At: at}
	if err := newDynamoBackend(ddb, "flags-tbl").Put(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	if len(ddb.writes) != 1 || len(ddb.writes[0].TransactItems) != 2 {
		t.Fatalf("expected one transaction with two puts, got %+v", ddb.writes)
	}
	var current, audit map[string]types.AttributeValue
	for _, ti := range ddb.writes[0].TransactItems {
		if ti.Put == nil || aws.ToString(ti.Put.TableName) != "flags-tbl" {
			t.Fatalf("unexpected transact item %+v", ti)
		}
		if str(t, ti.Put.Item, "sk") == "current" {
			current = ti.Put.Item
		} else {
			audit = ti.Put.Item
		}
	}
	if current == nil || audit == nil {
		t.Fatal("missing current or audit item")
	}
	if str(t, current, "pk") != c.Name || str(t, current, "value") != "true" || str(t, current, "prev_value") != "false" || str(t, current, "updated_by") != c.By {
		t.Errorf("current item = %v", current)
	}
	if str(t, audit, "pk") != "audit#"+c.Name || str(t, audit, "old") != "false" || str(t, audit, "new") != "true" || str(t, audit, "by") != c.By {
		t.Errorf("audit item = %v", audit)
	}

	// Audit sort keys must sort by time as strings.
	earlier := auditSortKey(at.Add(-time.Hour))
	if earlier >= str(t, audit, "sk") {
		t.Errorf("audit sort key %q does not sort after %q", str(t, audit, "sk"), earlier)
	}
}

func TestDynamoPutError(t *testing.T) {
	ddb := &fakeDDB{writeErr: errors.New("AccessDenied")}
	if err := newDynamoBackend(ddb, "t").Put(context.Background(), Change{Name: "x", New: "true"}); err == nil {
		t.Fatal("a write error must be returned")
	}
}
