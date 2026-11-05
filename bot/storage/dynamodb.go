package storage

import (
	"context"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/expression"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/spf13/viper"
)

const maxThreadMessages = 100

type Storage struct {
	client    *dynamodb.Client
	tableName string
}

// ConversationMessage is the bot's internal, library-agnostic representation
// of a single chat turn. Persisted in DynamoDB and assembled into the request
// payload for whichever OpenAI client the bot is using.
type ConversationMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type threadMessage struct {
	ThreadId        string `dynamodbav:"thread_id"`
	MessageUnixTime int64  `dynamodbav:"message_unix_time"`
	MessageSource   string `dynamodbav:"message_source,omitempty"`
	Message         string
}

func NewStorage(cfg aws.Config) *Storage {
	svc := dynamodb.NewFromConfig(cfg)
	return &Storage{
		client:    svc,
		tableName: viper.Get("CONVERSATION_TABLE").(string),
	}
}

func (s *Storage) GetThread(ctx context.Context, threadId string) ([]ConversationMessage, error) {
	var responseMessages []ConversationMessage
	// Construct our query and run it
	keyEx := expression.KeyAnd(
		expression.Key("thread_id").Equal(expression.Value(threadId)),                               // PK
		expression.Key("message_unix_time").LessThanEqual(expression.Value(time.Now().UnixMilli())), // SK
	)
	expr, err := expression.NewBuilder().WithKeyCondition(keyEx).Build()
	if err != nil {
		return responseMessages, err
	}

	q, err := s.client.Query(ctx, &dynamodb.QueryInput{
		TableName:                 aws.String(s.tableName),
		ExpressionAttributeNames:  expr.Names(),
		ExpressionAttributeValues: expr.Values(),
		KeyConditionExpression:    expr.KeyCondition(),
		// Newest first, so the limit keeps the most recent messages; reversed below.
		ScanIndexForward:          aws.Bool(false),
		Limit:                     aws.Int32(maxThreadMessages),
	})
	if err != nil {
		return responseMessages, err
	}

	// Unmarshal the results into a list
	var threadMessages []threadMessage
	err = attributevalue.UnmarshalListOfMaps(q.Items, &threadMessages)
	if err != nil {
		return responseMessages, err
	}

	return threadToConversation(threadMessages), nil
}

// threadToConversation converts newest-first stored messages into a
// chronological (oldest-first) conversation.
func threadToConversation(threadMessages []threadMessage) []ConversationMessage {
	var responseMessages []ConversationMessage
	for t := len(threadMessages) - 1; t >= 0; t-- {
		message := ConversationMessage{
			Content: threadMessages[t].Message,
		}
		if threadMessages[t].MessageSource == "Bot" {
			message.Role = "assistant"
		} else {
			message.Role = "user"
		}
		responseMessages = append(responseMessages, message)
	}
	return responseMessages
}

func (s *Storage) AddThreadMessage(ctx context.Context, threadId string, messageSource string, message string) error {
	messageRecord := &threadMessage{
		ThreadId:        threadId,
		MessageUnixTime: time.Now().UnixMilli(),
		MessageSource:   messageSource,
		Message:         message,
	}
	item, err := attributevalue.MarshalMap(messageRecord)
	if err != nil {
		return err
	}

	_, err = s.client.PutItem(ctx, &dynamodb.PutItemInput{
		Item:      item,
		TableName: aws.String(s.tableName),
	})

	return err
}
