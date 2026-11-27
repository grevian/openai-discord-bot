package feedback

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
)

type fakePut struct {
	in  *dynamodb.PutItemInput
	err error
}

func (f *fakePut) PutItem(_ context.Context, in *dynamodb.PutItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.PutItemOutput, error) {
	f.in = in
	return &dynamodb.PutItemOutput{}, f.err
}

var at = time.Date(2025, 11, 27, 12, 0, 0, 0, time.UTC)

func str(t *testing.T, m map[string]types.AttributeValue, k string) string {
	t.Helper()
	v, ok := m[k].(*types.AttributeValueMemberS)
	if !ok {
		t.Fatalf("attribute %q missing or not a string: %v", k, m[k])
	}
	return v.Value
}

func TestSortKeyFormat(t *testing.T) {
	if got := SortKey(time.UnixMilli(5), "99"); got != "0000000000005#99" {
		t.Errorf("got %q", got)
	}
	if got := SortKey(at, "1312"); got != "1764244800000#1312" {
		t.Errorf("got %q", got)
	}
}

func TestPutBuildsItemAndCondition(t *testing.T) {
	f := &fakePut{}
	st := &Store{client: f, table: "tbl"}
	err := st.Put(context.Background(), Item{Kind: KindIdea, Text: "more cats", CreatedAt: at, UserID: "u1", Username: "alice",
		ChannelID: "c1", MessageID: "m1", BotVersion: "abc"})
	if err != nil {
		t.Fatal(err)
	}
	if *f.in.TableName != "tbl" || *f.in.ConditionExpression != "attribute_not_exists(pk)" {
		t.Errorf("bad input %+v", f.in)
	}
	it := f.in.Item
	want := map[string]string{"pk": "feedback", "sk": SortKey(at, "m1"), "kind": "idea", "text": "more cats", "status": "new",
		"created_at": "2025-11-27T12:00:00Z", "user_id": "u1", "username": "alice", "channel_id": "c1", "message_id": "m1",
		"message_url": "https://discord.com/channels/@me/c1/m1", "bot_version": "abc"}
	for k, v := range want {
		if got := str(t, it, k); got != v {
			t.Errorf("%s = %q, want %q", k, got, v)
		}
	}
	for _, k := range []string{"guild_id", "reply_to_message_id", "reply_to_content", "truncated"} {
		if _, ok := it[k]; ok {
			t.Errorf("empty optional %q must be left out", k)
		}
	}
}

func TestPutOptionalFields(t *testing.T) {
	f := &fakePut{}
	st := &Store{client: f, table: "tbl"}
	if err := st.Put(context.Background(), Item{Kind: KindFeedback, Text: "x", Truncated: true, CreatedAt: at, GuildID: "g1",
		ChannelID: "c1", MessageID: "m1", ReplyToMessageID: "r1", ReplyToContent: "hi"}); err != nil {
		t.Fatal(err)
	}
	it := f.in.Item
	if str(t, it, "guild_id") != "g1" || str(t, it, "reply_to_message_id") != "r1" || str(t, it, "reply_to_content") != "hi" ||
		str(t, it, "message_url") != "https://discord.com/channels/g1/c1/m1" {
		t.Errorf("optional fields wrong: %v", it)
	}
	if b, ok := it["truncated"].(*types.AttributeValueMemberBOOL); !ok || !b.Value {
		t.Errorf("truncated not set: %v", it["truncated"])
	}
}

func TestPutDuplicateIsNotAnError(t *testing.T) {
	st := &Store{client: &fakePut{err: &types.ConditionalCheckFailedException{}}, table: "t"}
	if err := st.Put(context.Background(), Item{CreatedAt: at}); err != nil {
		t.Errorf("duplicate put returned %v", err)
	}
}

func TestPutError(t *testing.T) {
	st := &Store{client: &fakePut{err: errors.New("boom")}, table: "t"}
	if err := st.Put(context.Background(), Item{CreatedAt: at}); err == nil {
		t.Error("expected an error")
	}
}

func TestLimiterWindowAndPruning(t *testing.T) {
	now := at
	l := NewLimiter(2, 10*time.Minute)
	l.now = func() time.Time { return now }
	first := l.Allow("a")
	if second := l.Allow("a"); !first || !second {
		t.Fatal("first two must pass")
	}
	if l.Allow("a") {
		t.Error("third in window must be refused")
	}
	if !l.Allow("b") {
		t.Error("another user is unaffected")
	}
	now = now.Add(11 * time.Minute)
	if !l.Allow("a") {
		t.Error("window must slide")
	}
	if _, ok := l.seen["b"]; ok {
		t.Error("stale user must be pruned")
	}
}
