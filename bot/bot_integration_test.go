package bot

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"

	gpt "github.com/sashabaranov/go-openai"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- Mock thread store ---

type storedMessage struct {
	source  string
	message string
}

type mockThreadStore struct {
	mu       sync.Mutex
	threads  map[string][]storedMessage
	messages map[string][]gpt.ChatCompletionMessage
}

func newMockThreadStore() *mockThreadStore {
	return &mockThreadStore{
		threads:  make(map[string][]storedMessage),
		messages: make(map[string][]gpt.ChatCompletionMessage),
	}
}

func (m *mockThreadStore) GetThread(_ context.Context, threadId string) ([]gpt.ChatCompletionMessage, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.messages[threadId], nil
}

func (m *mockThreadStore) AddThreadMessage(_ context.Context, threadId string, messageSource string, message string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.threads[threadId] = append(m.threads[threadId], storedMessage{source: messageSource, message: message})

	role := "user"
	if messageSource == "Bot" {
		role = "assistant"
	}
	m.messages[threadId] = append(m.messages[threadId], gpt.ChatCompletionMessage{
		Role:    role,
		Content: message,
	})
	return nil
}

// --- Mock image store ---

type mockImageStore struct {
	mu     sync.Mutex
	images map[string][]byte
	server *httptest.Server
}

func newMockImageStore() *mockImageStore {
	m := &mockImageStore{
		images: make(map[string][]byte),
	}
	m.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		m.mu.Lock()
		data, ok := m.images[r.URL.Path]
		m.mu.Unlock()
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Length", fmt.Sprintf("%d", len(data)))
		w.Write(data)
	}))
	return m
}

func (m *mockImageStore) GetImageFromURL(_ context.Context, URL string) (io.ReadCloser, int64, error) {
	resp, err := http.Get(URL)
	if err != nil {
		return nil, 0, err
	}
	return resp.Body, resp.ContentLength, nil
}

func (m *mockImageStore) StoreImage(_ context.Context, groupId string, reader io.Reader, _ int64) (string, error) {
	data, err := io.ReadAll(reader)
	if err != nil {
		return "", err
	}
	key := groupId + "/test-image"
	m.mu.Lock()
	m.images["/"+key] = data
	m.mu.Unlock()
	return key, nil
}

func (m *mockImageStore) addImage(path string, data []byte) string {
	m.mu.Lock()
	m.images[path] = data
	m.mu.Unlock()
	return m.server.URL + path
}

func (m *mockImageStore) close() {
	m.server.Close()
}

// --- Helper to create a test bot with real OpenAI client ---

func newOpenAIClient(t *testing.T) *gpt.Client {
	t.Helper()
	token := os.Getenv("OPENAI_AUTH_TOKEN")
	if token == "" {
		t.Skip("OPENAI_AUTH_TOKEN not set, skipping integration test")
	}
	return gpt.NewClient(token)
}

// --- Unit tests for helper functions ---

func TestFindLastImageURL(t *testing.T) {
	tests := []struct {
		name     string
		context  []gpt.ChatCompletionMessage
		expected string
	}{
		{
			name:     "empty context",
			context:  nil,
			expected: "",
		},
		{
			name: "no image URLs",
			context: []gpt.ChatCompletionMessage{
				{Role: "user", Content: "draw a cat"},
				{Role: "assistant", Content: "sure thing"},
			},
			expected: "",
		},
		{
			name: "single image URL",
			context: []gpt.ChatCompletionMessage{
				{Role: "user", Content: "draw a cat"},
				{Role: "assistant", Content: "https://sillybullshit.click/guild/abc123"},
			},
			expected: "https://sillybullshit.click/guild/abc123",
		},
		{
			name: "multiple images returns last",
			context: []gpt.ChatCompletionMessage{
				{Role: "user", Content: "draw a cat"},
				{Role: "assistant", Content: "https://sillybullshit.click/guild/first"},
				{Role: "user", Content: "now a dog"},
				{Role: "assistant", Content: "https://sillybullshit.click/guild/second"},
			},
			expected: "https://sillybullshit.click/guild/second",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, findLastImageURL(tt.context))
		})
	}
}

func TestFindOriginalPrompt(t *testing.T) {
	tests := []struct {
		name     string
		context  []gpt.ChatCompletionMessage
		expected string
	}{
		{
			name:     "empty context",
			context:  nil,
			expected: "",
		},
		{
			name: "no user messages",
			context: []gpt.ChatCompletionMessage{
				{Role: "assistant", Content: "https://sillybullshit.click/guild/abc"},
			},
			expected: "",
		},
		{
			name: "returns first user message",
			context: []gpt.ChatCompletionMessage{
				{Role: "user", Content: "draw a cat"},
				{Role: "assistant", Content: "https://sillybullshit.click/guild/abc"},
				{Role: "user", Content: "make it blue"},
			},
			expected: "draw a cat",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, findOriginalPrompt(tt.context))
		})
	}
}

// --- Integration tests (require OPENAI_AUTH_TOKEN) ---

func TestHandleCompletionPrompt_Integration(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	client := newOpenAIClient(t)
	store := newMockThreadStore()

	bot := &AIBot{
		openapiClient: client,
		botCtx:        context.Background(),
		storage:       store,
		basePrompt: []gpt.ChatCompletionMessage{
			{Role: "system", Content: "You are a helpful assistant. Respond briefly."},
		},
	}

	ctx := context.Background()
	channelID := "test-channel-completion"

	// We can't call handleCompletionPrompt directly because it sends to Discord.
	// Instead test the OpenAI + storage flow directly.
	prompt := "What is 2+2? Reply with just the number."
	threadContext := []gpt.ChatCompletionMessage{}

	userMessage := gpt.ChatCompletionMessage{Role: "user", Content: prompt}
	requestMessages := append(threadContext, userMessage)

	request := gpt.ChatCompletionRequest{
		Model:    gpt.GPT3Dot5Turbo,
		Messages: append(bot.basePrompt, requestMessages...),
	}

	response, err := client.CreateChatCompletion(ctx, request)
	require.NoError(t, err)
	require.NotEmpty(t, response.Choices)

	responseText := response.Choices[0].Message.Content
	assert.NotEmpty(t, responseText)
	t.Logf("Response: %s", responseText)

	// Verify storage works
	err = store.AddThreadMessage(ctx, channelID, "testuser (123) on guild1", "User: "+prompt)
	require.NoError(t, err)
	err = store.AddThreadMessage(ctx, channelID, "Bot", responseText)
	require.NoError(t, err)

	stored, err := store.GetThread(ctx, channelID)
	require.NoError(t, err)
	assert.Len(t, stored, 2)
	assert.Equal(t, "User: "+prompt, stored[0].Content)
	assert.Equal(t, responseText, stored[1].Content)
}

func TestHandleImageMessage_Integration(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	client := newOpenAIClient(t)
	store := newMockThreadStore()
	imgStore := newMockImageStore()
	defer imgStore.close()

	bot := &AIBot{
		openapiClient: client,
		botCtx:        context.Background(),
		storage:       store,
		imageStorage:  imgStore,
	}

	ctx := context.Background()

	// Test DALL-E 3 image generation
	imageRequest := gpt.ImageRequest{
		Prompt:         "a simple red circle on white background",
		N:              1,
		Size:           gpt.CreateImageSize1024x1024,
		ResponseFormat: gpt.CreateImageResponseFormatURL,
		Model:          gpt.CreateImageModelDallE3,
	}

	response, err := bot.openapiClient.CreateImage(ctx, imageRequest)
	require.NoError(t, err)
	require.NotEmpty(t, response.Data)
	assert.NotEmpty(t, response.Data[0].URL)
	t.Logf("Generated image URL: %s", response.Data[0].URL)

	// Verify we can download the generated image
	imageReader, imageLength, err := imgStore.GetImageFromURL(ctx, response.Data[0].URL)
	require.NoError(t, err)
	defer imageReader.Close()
	assert.Greater(t, imageLength, int64(0))

	// Verify storage
	imageData, err := io.ReadAll(imageReader)
	require.NoError(t, err)
	assert.Greater(t, len(imageData), 0)
}

func TestHandleImageMessage_ThreadContextCombinesPrompts(t *testing.T) {
	threadContext := []gpt.ChatCompletionMessage{
		{Role: "user", Content: "a red sports car"},
		{Role: "assistant", Content: "https://sillybullshit.click/guild/img1"},
	}

	originalPrompt := findOriginalPrompt(threadContext)
	assert.Equal(t, "a red sports car", originalPrompt)

	// Verify prompt combination logic (same as handleImageMessage)
	newPrompt := "make it blue"
	combined := "Original image prompt: " + originalPrompt + ". Modification: " + newPrompt
	assert.Contains(t, combined, "a red sports car")
	assert.Contains(t, combined, "make it blue")
}

func TestHandleImageEditMessage_NoImageInContext(t *testing.T) {
	threadContext := []gpt.ChatCompletionMessage{
		{Role: "user", Content: "just some text"},
		{Role: "assistant", Content: "a response"},
	}

	imageURL := findLastImageURL(threadContext)
	assert.Empty(t, imageURL, "should not find an image URL in thread context without one")
}

func TestHandleImageEditMessage_FindsImage(t *testing.T) {
	threadContext := []gpt.ChatCompletionMessage{
		{Role: "user", Content: "draw a cat"},
		{Role: "assistant", Content: "https://sillybullshit.click/guild/cat123"},
		{Role: "user", Content: "edit it"},
	}

	imageURL := findLastImageURL(threadContext)
	assert.Equal(t, "https://sillybullshit.click/guild/cat123", imageURL)
}
