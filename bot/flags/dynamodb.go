package flags

import (
	"context"
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
)

// Table layout (terraform/storage.tf, aws_dynamodb_table.flags):
//
//	current value: pk=<name>        sk="current"         value, updated_by, updated_at, prev_value
//	audit entry:   pk="audit#<name>" sk=<zero-padded ms> old, new, by, at
//
// pk is ready for scoped overrides later (<name>#guild:<id>), which need no
// schema change.
const (
	currentSortKey = "current"
	auditPrefix    = "audit#"
)

// dynamoAPI is the subset of the DynamoDB client the backend uses.
type dynamoAPI interface {
	Scan(ctx context.Context, in *dynamodb.ScanInput, opts ...func(*dynamodb.Options)) (*dynamodb.ScanOutput, error)
	TransactWriteItems(ctx context.Context, in *dynamodb.TransactWriteItemsInput, opts ...func(*dynamodb.Options)) (*dynamodb.TransactWriteItemsOutput, error)
}

var _ dynamoAPI = (*dynamodb.Client)(nil)

// DynamoBackend stores flags in a DynamoDB table.
type DynamoBackend struct {
	client dynamoAPI
	table  string
}

// NewDynamoBackend returns a backend over the table called table.
func NewDynamoBackend(cfg aws.Config, table string) *DynamoBackend {
	return newDynamoBackend(dynamodb.NewFromConfig(cfg), table)
}

func newDynamoBackend(client dynamoAPI, table string) *DynamoBackend {
	return &DynamoBackend{client: client, table: table}
}

// LoadAll scans the current-value items. The table holds a handful of flags
// plus their audit entries, so a filtered scan is cheap.
func (d *DynamoBackend) LoadAll(ctx context.Context) (map[string]Value, error) {
	out := map[string]Value{}
	in := &dynamodb.ScanInput{
		TableName:                 aws.String(d.table),
		FilterExpression:          aws.String("sk = :current"),
		ExpressionAttributeValues: map[string]types.AttributeValue{":current": &types.AttributeValueMemberS{Value: currentSortKey}},
	}
	for {
		page, err := d.client.Scan(ctx, in)
		if err != nil {
			return nil, err
		}
		for _, item := range page.Items {
			v := Value{
				Name:      attrString(item, "pk"),
				Raw:       attrString(item, "value"),
				UpdatedBy: attrString(item, "updated_by"),
				PrevValue: attrString(item, "prev_value"),
			}
			if at, err := time.Parse(time.RFC3339Nano, attrString(item, "updated_at")); err == nil {
				v.UpdatedAt = at
			}
			if v.Name != "" {
				out[v.Name] = v
			}
		}
		if len(page.LastEvaluatedKey) == 0 {
			return out, nil
		}
		in.ExclusiveStartKey = page.LastEvaluatedKey
	}
}

// Put writes the new current value and its audit entry in one transaction,
// so there is never a change without its audit record.
func (d *DynamoBackend) Put(ctx context.Context, c Change) error {
	at := c.At.UTC().Format(time.RFC3339Nano)
	current := map[string]types.AttributeValue{
		"pk":         sAttr(c.Name),
		"sk":         sAttr(currentSortKey),
		"value":      sAttr(c.New),
		"updated_by": sAttr(c.By),
		"updated_at": sAttr(at),
		"prev_value": sAttr(c.Old),
	}
	audit := map[string]types.AttributeValue{
		"pk":  sAttr(auditPrefix + c.Name),
		"sk":  sAttr(auditSortKey(c.At)),
		"old": sAttr(c.Old),
		"new": sAttr(c.New),
		"by":  sAttr(c.By),
		"at":  sAttr(at),
	}
	_, err := d.client.TransactWriteItems(ctx, &dynamodb.TransactWriteItemsInput{
		TransactItems: []types.TransactWriteItem{
			{Put: &types.Put{TableName: aws.String(d.table), Item: current}},
			{Put: &types.Put{TableName: aws.String(d.table), Item: audit}},
		},
	})
	return err
}

// auditSortKey is the audit entry's sort key: Unix milliseconds, zero-padded
// so that string order is time order.
func auditSortKey(t time.Time) string {
	return fmt.Sprintf("%020d", t.UnixMilli())
}

func sAttr(v string) types.AttributeValue { return &types.AttributeValueMemberS{Value: v} }

func attrString(item map[string]types.AttributeValue, key string) string {
	if v, ok := item[key].(*types.AttributeValueMemberS); ok {
		return v.Value
	}
	return ""
}
