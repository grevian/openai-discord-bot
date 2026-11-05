// Command gallery-backfill publishes the newest existing images to the public
// gallery once, for images made before the gallery existed.
//
// It uses the gallery package's own publish code, so the manifest format,
// ordering, prompt cleaning, eligibility rules (DMs never, excluded guilds
// never) and conditional writes are exactly the bot's. It only reads data the
// bot already stored: the images bucket and the conversation table.
//
//	gallery-backfill --dry-run     # list what would be published
//	gallery-backfill               # publish it
//
// Needs AWS credentials able to list/read/write the images bucket and read the
// conversation table. Bucket and table default to the bot's environment
// variables OPENAIDISCORDBOTIMAGES_NAME and AI_DISCORD_BOT_CONVERSATIONS_NAME.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	ddbtypes "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	"openai-discord-bot/gallery"
)

type threadMessage struct {
	ThreadId        string `dynamodbav:"thread_id"`
	MessageUnixTime int64  `dynamodbav:"message_unix_time"`
	MessageSource   string `dynamodbav:"message_source"`
	Message         string
}

func main() {
	bucket := flag.String("bucket", os.Getenv("OPENAIDISCORDBOTIMAGES_NAME"), "images bucket")
	table := flag.String("table", os.Getenv("AI_DISCORD_BOT_CONVERSATIONS_NAME"), "conversation table (for prompts); empty skips prompt recovery")
	dry := flag.Bool("dry-run", false, "print what would be published and write nothing")
	flag.Parse()
	if *bucket == "" {
		log.Fatal("--bucket (or OPENAIDISCORDBOTIMAGES_NAME) is required")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	awsCfg, err := awsconfig.LoadDefaultConfig(ctx)
	if err != nil {
		log.Fatalf("load AWS config: %v", err)
	}
	cfg, err := gallery.LoadConfig()
	if err != nil {
		log.Fatal(err)
	}

	keys, err := listKeys(ctx, s3.NewFromConfig(awsCfg), *bucket)
	if err != nil {
		log.Fatalf("list bucket: %v", err)
	}
	items, st := gallery.SelectBackfill(cfg, keys)

	missing := 0
	if *table != "" {
		prompts, err := recoverPrompts(ctx, dynamodb.NewFromConfig(awsCfg), *table, items)
		if err != nil {
			log.Fatalf("recover prompts: %v", err)
		}
		for i := range items {
			if p, ok := prompts[items[i].ImageKey]; ok {
				items[i].Prompt, items[i].Kind = p.prompt, p.kind
			}
		}
	}
	for _, it := range items {
		if gallery.CleanPrompt(it.Prompt) == "" {
			missing++
		}
	}

	fmt.Printf("scanned=%d not-image=%d dm-excluded=%d ambiguous-excluded=%d guild-excluded=%d selected=%d without-prompt=%d\n",
		st.Scanned, st.NotImage, st.DM, st.Ambiguous, st.GuildExcluded, len(items), missing)
	for _, it := range items {
		fmt.Printf("%s  %-8s %q\n", it.Created.Format(time.RFC3339), it.Kind, gallery.CleanPrompt(it.Prompt))
	}
	if *dry {
		fmt.Println("dry run: nothing written")
		return
	}

	g := gallery.New(cfg, gallery.NewS3Store(awsCfg, *bucket))
	published, err := g.PublishBackfill(ctx, items)
	if err != nil {
		log.Fatalf("publish: %v", err)
	}
	fmt.Printf("published=%d to %s\n", len(published), gallery.ManifestKey)
	fmt.Println("CloudFront may cache latest.json for up to 60s; invalidate /gallery/latest.json to see it sooner.")
}

func listKeys(ctx context.Context, c *s3.Client, bucket string) ([]string, error) {
	var keys []string
	p := s3.NewListObjectsV2Paginator(c, &s3.ListObjectsV2Input{Bucket: &bucket})
	for p.HasMorePages() {
		page, err := p.NextPage(ctx)
		if err != nil {
			return nil, err
		}
		for _, o := range page.Contents {
			if k := aws.ToString(o.Key); !strings.HasPrefix(k, gallery.Prefix) {
				keys = append(keys, k)
			}
		}
	}
	return keys, nil
}

type recovered struct {
	prompt string
	kind   gallery.Kind
}

// recoverPrompts finds, for each selected image, the bot's "<url>" message in
// the conversation table and the user message just before it in the same
// thread, which holds the prompt (see handleImageMessage and
// handleImageEditMessage in bot/bot.go). Images with no match are absent.
func recoverPrompts(ctx context.Context, c *dynamodb.Client, table string, items []gallery.BackfillItem) (map[string]recovered, error) {
	want := map[string]bool{}
	for _, it := range items {
		want[it.ImageKey] = true
	}
	type ref struct {
		thread string
		at     int64
		key    string
	}
	var refs []ref
	p := dynamodb.NewScanPaginator(c, &dynamodb.ScanInput{
		TableName:        &table,
		FilterExpression: aws.String("message_source = :bot"),
		ExpressionAttributeValues: map[string]ddbtypes.AttributeValue{
			":bot": &ddbtypes.AttributeValueMemberS{Value: "Bot"},
		},
	})
	for p.HasMorePages() {
		page, err := p.NextPage(ctx)
		if err != nil {
			return nil, err
		}
		var msgs []threadMessage
		if err := attributevalue.UnmarshalListOfMaps(page.Items, &msgs); err != nil {
			return nil, err
		}
		for _, m := range msgs {
			if i := strings.Index(m.Message, "://"); i >= 0 {
				// The message is "<base url>/<guild>/<ksuid>".
				parts := strings.Split(strings.TrimSpace(m.Message), "/")
				if len(parts) >= 2 {
					key := parts[len(parts)-2] + "/" + parts[len(parts)-1]
					if want[key] {
						refs = append(refs, ref{m.ThreadId, m.MessageUnixTime, key})
					}
				}
			}
		}
	}

	out := map[string]recovered{}
	for _, r := range refs {
		q, err := c.Query(ctx, &dynamodb.QueryInput{
			TableName:              &table,
			KeyConditionExpression: aws.String("thread_id = :t AND message_unix_time < :s"),
			ExpressionAttributeValues: map[string]ddbtypes.AttributeValue{
				":t": &ddbtypes.AttributeValueMemberS{Value: r.thread},
				":s": &ddbtypes.AttributeValueMemberN{Value: fmt.Sprint(r.at)},
			},
			ScanIndexForward: aws.Bool(false),
			Limit:            aws.Int32(5),
		})
		if err != nil {
			return nil, err
		}
		var msgs []threadMessage
		if err := attributevalue.UnmarshalListOfMaps(q.Items, &msgs); err != nil {
			return nil, err
		}
		// Newest first. The nearest user message that is not a Bot reply is the
		// prompt that produced this image.
		for _, m := range msgs {
			if m.MessageSource == "Bot" {
				continue
			}
			out[r.key] = parsePrompt(m.Message)
			break
		}
	}
	return out, nil
}

// parsePrompt undoes the "edit: <prompt> (source: <url>)" wrapping the bot
// applies to edit prompts.
func parsePrompt(msg string) recovered {
	if rest, ok := strings.CutPrefix(msg, "edit: "); ok {
		if i := strings.LastIndex(rest, " (source: "); i >= 0 {
			rest = rest[:i]
		}
		return recovered{rest, gallery.KindEdit}
	}
	return recovered{msg, gallery.KindGenerate}
}
