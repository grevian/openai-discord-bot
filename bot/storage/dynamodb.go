package storage

import (
	"context"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/expression"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	gpt "github.com/sashabaranov/go-openai"
	"github.com/spf13/viper"
)

type Storage struct {
	client    *dynamodb.Client
	tableName string
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

func (s *Storage) GetThread(ctx context.Context, threadId string) ([]gpt.ChatCompletionMessage, error) {
	var responseMessages []gpt.ChatCompletionMessage
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
		Limit:                     aws.Int32(100),
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

	// Concatenate the list of messages together and return them
	for t := range threadMessages {
		message := gpt.ChatCompletionMessage{
			Content: threadMessages[t].Message,
		}
		if threadMessages[t].MessageSource == "Bot" {
			message.Role = "assistant"
		} else {
			message.Role = "user"
		}
		responseMessages = append(responseMessages, message)
	}
	return responseMessages, nil
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
