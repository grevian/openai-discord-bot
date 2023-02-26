package storage

import (
	"context"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/expression"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/spf13/viper"
)

type Storage struct {
	client    *dynamodb.Client
	tableName string
}

type threadMessage struct {
	ThreadId        string `dynamodbav:"thread_id"`
	MessageUnixTime int64  `dynamodbav:"message_unix_time"`
	Message         string
}

func NewStorage(cfg aws.Config) *Storage {
	svc := dynamodb.NewFromConfig(cfg)
	return &Storage{
		client:    svc,
		tableName: viper.Get("CONVERSATION_TABLE").(string),
	}
}

func (s *Storage) GetThread(ctx context.Context, threadId string) (string, error) {
	// Construct our query and run it
	keyEx := expression.KeyAnd(
		expression.Key("thread_id").Equal(expression.Value(threadId)),                               // PK
		expression.Key("message_unix_time").LessThanEqual(expression.Value(time.Now().UnixMilli())), // SK
	)
	expr, err := expression.NewBuilder().WithKeyCondition(keyEx).Build()
	if err != nil {
		return "", err
	}

	q, err := s.client.Query(ctx, &dynamodb.QueryInput{
		TableName:                 aws.String(s.tableName),
		ExpressionAttributeNames:  expr.Names(),
		ExpressionAttributeValues: expr.Values(),
		KeyConditionExpression:    expr.KeyCondition(),
		Limit:                     aws.Int32(100),
	})

	// Unmarshal the results into a list
	var threadMessages []threadMessage
	err = attributevalue.UnmarshalListOfMaps(q.Items, &threadMessages)
	if err != nil {
		return "", err
	}

	// Concatenate the list of messages together and return them
	thread := strings.Builder{}
	for t := range threadMessages {
		thread.WriteString(threadMessages[t].Message)
	}
	return thread.String(), nil
}

func (s *Storage) AddThreadMessage(ctx context.Context, threadId string, message string) error {
	messageRecord := &threadMessage{
		ThreadId:        threadId,
		MessageUnixTime: time.Now().UnixMilli(),
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
