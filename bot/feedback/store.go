// Package feedback stores user feedback and feature requests in DynamoDB.
//
// Table layout (terraform/storage.tf, aws_dynamodb_table.feedback):
//
//	pk = "feedback"   sk = "<unix ms, 13 digits>#<discord message id>"
//
// scripts/feedback.py reads and updates the same attributes; keep the names
// below in step with it.
package feedback

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
)

// Item is one piece of feedback.
type Item struct {
	Kind      string // KindFeedback or KindIdea
	Text      string
	Truncated bool
	CreatedAt time.Time
	UserID    string
	Username  string
	GuildID   string // empty in DMs
	ChannelID string
	MessageID string
	// ReplyToMessageID and ReplyToContent are set when the message replies
	// to one of the bot's own messages.
	ReplyToMessageID string
	ReplyToContent   string
	BotVersion       string
}

const (
	KindFeedback = "feedback"
	KindIdea     = "idea"

	// StatusNew is the status every item starts with. Agents change it.
	StatusNew = "new"

	partitionKey = "feedback"
)

// SortKey is the item's sk, and the ID agents use for it.
func SortKey(t time.Time, messageID string) string {
	return fmt.Sprintf("%013d#%s", t.UnixMilli(), messageID)
}

// MessageURL is the link to the message in Discord.
func MessageURL(guildID, channelID, messageID string) string {
	if guildID == "" {
		guildID = "@me"
	}
	return fmt.Sprintf("https://discord.com/channels/%s/%s/%s", guildID, channelID, messageID)
}

// SortKey is the item's sort key.
func (it Item) SortKey() string { return SortKey(it.CreatedAt, it.MessageID) }

type putAPI interface {
	PutItem(ctx context.Context, in *dynamodb.PutItemInput, opts ...func(*dynamodb.Options)) (*dynamodb.PutItemOutput, error)
}

var _ putAPI = (*dynamodb.Client)(nil)

// Store writes feedback to a DynamoDB table.
type Store struct {
	client putAPI
	table  string
}

// NewStore returns a store over the table called table.
func NewStore(cfg aws.Config, table string) *Store {
	return &Store{client: dynamodb.NewFromConfig(cfg), table: table}
}

func s(v string) types.AttributeValue { return &types.AttributeValueMemberS{Value: v} }

func (it Item) attributes() map[string]types.AttributeValue {
	t := it.CreatedAt.UTC()
	attrs := map[string]types.AttributeValue{
		"pk":          s(partitionKey),
		"sk":          s(SortKey(t, it.MessageID)),
		"kind":        s(it.Kind),
		"text":        s(it.Text),
		"status":      s(StatusNew),
		"created_at":  s(t.Format(time.RFC3339)),
		"user_id":     s(it.UserID),
		"channel_id":  s(it.ChannelID),
		"message_id":  s(it.MessageID),
		"message_url": s(MessageURL(it.GuildID, it.ChannelID, it.MessageID)),
	}
	optional := map[string]string{
		"username":            it.Username,
		"guild_id":            it.GuildID,
		"bot_version":         it.BotVersion,
		"reply_to_message_id": it.ReplyToMessageID,
		"reply_to_content":    it.ReplyToContent,
	}
	for k, v := range optional {
		if v != "" {
			attrs[k] = s(v)
		}
	}
	if it.Truncated {
		attrs["truncated"] = &types.AttributeValueMemberBOOL{Value: true}
	}
	return attrs
}

// Put stores the item. Storing the same message twice (a gateway redelivery)
// is not an error and does not change the first item.
func (st *Store) Put(ctx context.Context, it Item) error {
	_, err := st.client.PutItem(ctx, &dynamodb.PutItemInput{
		TableName:           aws.String(st.table),
		Item:                it.attributes(),
		ConditionExpression: aws.String("attribute_not_exists(pk)"),
	})
	var dup *types.ConditionalCheckFailedException
	if errors.As(err, &dup) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("storing feedback: %w", err)
	}
	return nil
}
