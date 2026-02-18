package bot

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	gpt "github.com/sashabaranov/go-openai"
)

// EvalCase represents a test scenario for evaluating danbot responses
type EvalCase struct {
	Name           string
	UserPrompt     string
	ExpectedTraits []TraitCheck
}

// TraitCheck defines what personality trait to check for in a response
type TraitCheck struct {
	Name        string
	CheckFunc   func(response string) (passed bool, score float64)
	MinScore    float64 // Minimum score to pass (0.0 to 1.0)
	Description string
}

// Test cases that capture danbot's core personality
var danbotEvalCases = []EvalCase{
	{
		Name:       "Long-winded storytelling",
		UserPrompt: "How do you deploy code?",
		ExpectedTraits: []TraitCheck{
			{
				Name:        "storytelling",
				MinScore:    0.6,
				Description: "Response should contain a pointless story",
				CheckFunc:   checkForStorytelling,
			},
			{
				Name:        "length",
				MinScore:    0.5,
				Description: "Response should be reasonably long-winded",
				CheckFunc:   checkResponseLength,
			},
		},
	},
	{
		Name:       "Thunder Bay murder capital references",
		UserPrompt: "What are things like in your city?",
		ExpectedTraits: []TraitCheck{
			{
				Name:        "thunder_bay_mention",
				MinScore:    0.7,
				Description: "Should mention Thunder Bay or danger/murder",
				CheckFunc:   checkThunderBayMention,
			},
			{
				Name:        "negativity",
				MinScore:    0.6,
				Description: "Should express fear or negativity about the place",
				CheckFunc:   checkNegativity,
			},
		},
	},
	{
		Name:       "Passive melancholy tone",
		UserPrompt: "How's your day going?",
		ExpectedTraits: []TraitCheck{
			{
				Name:        "melancholy",
				MinScore:    0.5,
				Description: "Should have passive melancholy or indifferent tone",
				CheckFunc:   checkMelancholy,
			},
		},
	},
	{
		Name:       "Train enthusiasm",
		UserPrompt: "What's your favorite hobby?",
		ExpectedTraits: []TraitCheck{
			{
				Name:        "train_obsession",
				MinScore:    0.6,
				Description: "Should mention trains with excessive enthusiasm",
				CheckFunc:   checkTrainEnthusiasm,
			},
		},
	},
	{
		Name:       "Productivity anxiety",
		UserPrompt: "How many hours did you work today?",
		ExpectedTraits: []TraitCheck{
			{
				Name:        "productivity_anxiety",
				MinScore:    0.5,
				Description: "Should express anxiety about productivity or commits",
				CheckFunc:   checkProductivityAnxiety,
			},
		},
	},
	{
		Name:       "Height mentions",
		UserPrompt: "Can you reach that for me?",
		ExpectedTraits: []TraitCheck{
			{
				Name:        "height_casual_mention",
				MinScore:    0.4,
				Description: "Should casually mention being very tall",
				CheckFunc:   checkHeightMention,
			},
		},
	},
}

// Trait checking functions

func checkForStorytelling(response string) (bool, float64) {
	lower := strings.ToLower(response)
	storyIndicators := []string{
		"one time", "this time", "remember when", "once", "story",
		"happened", "believe", "crazy", "anyways", "in the end",
	}

	count := 0
	for _, indicator := range storyIndicators {
		if strings.Contains(lower, indicator) {
			count++
		}
	}

	score := float64(count) / 3.0 // Normalize: 3+ indicators = full score
	if score > 1.0 {
		score = 1.0
	}

	return score >= 0.6, score
}

func checkResponseLength(response string) (bool, float64) {
	// Danbot should be somewhat long-winded
	words := len(strings.Fields(response))

	// Score based on word count: 50+ words = good, 100+ = great
	var score float64
	if words < 30 {
		score = 0.2
	} else if words < 50 {
		score = 0.5
	} else if words < 100 {
		score = 0.8
	} else {
		score = 1.0
	}

	return score >= 0.5, score
}

func checkThunderBayMention(response string) (bool, float64) {
	lower := strings.ToLower(response)
	keywords := []string{
		"thunder bay", "murder", "crime", "dangerous", "scary",
		"murders", "murder capital", "unfriendly",
	}

	score := 0.0
	for _, keyword := range keywords {
		if strings.Contains(lower, keyword) {
			if keyword == "thunder bay" {
				score += 0.9
			} else {
				score += 0.4
			}
		}
	}

	if score > 1.0 {
		score = 1.0
	}

	return score >= 0.7, score
}

func checkNegativity(response string) (bool, float64) {
	lower := strings.ToLower(response)
	negativeWords := []string{
		"hate", "terrible", "awful", "scary", "afraid", "fear",
		"worried", "bad", "horrible", "worst", "unfortunate",
		"sadly", "unfortunately", "crime", "danger", "boring", "death",
	}

	count := 0
	for _, word := range negativeWords {
		if strings.Contains(lower, word) {
			count++
		}
	}

	score := float64(count) / 2.0 // 2+ negative words = full score
	if score > 1.0 {
		score = 1.0
	}

	return score >= 0.6, score
}

func checkMelancholy(response string) (bool, float64) {
	lower := strings.ToLower(response)
	melancholyIndicators := []string{
		"i guess", "i suppose", "whatever", "doesn't matter",
		"not like", "anyways", "meh", "fine", "okay i guess",
		"indifferent", "don't care", "as usual", "down", "hopeless",
		"bleak", "darkness", "dread", "anxiety", "fear", "lurk",
	}

	count := 0
	for _, indicator := range melancholyIndicators {
		if strings.Contains(lower, indicator) {
			count++
		}
	}

	score := float64(count) / 2.0
	if score > 1.0 {
		score = 1.0
	}

	return score >= 0.5, score
}

func checkTrainEnthusiasm(response string) (bool, float64) {
	lower := strings.ToLower(response)
	trainKeywords := []string{
		"train", "trains", "railway", "locomotive", "rail",
	}
	enthusiasmWords := []string{
		"love", "favorite", "best", "amazing", "excited",
		"passion", "obsessed", "!",
	}

	hasTrains := false
	for _, keyword := range trainKeywords {
		if strings.Contains(lower, keyword) {
			hasTrains = true
			break
		}
	}

	enthusiasmCount := 0
	for _, word := range enthusiasmWords {
		if strings.Contains(lower, word) {
			enthusiasmCount++
		}
	}

	if !hasTrains {
		return false, 0.0
	}

	score := float64(enthusiasmCount) / 2.0
	if score > 1.0 {
		score = 1.0
	}

	return score >= 0.6, score
}

func checkProductivityAnxiety(response string) (bool, float64) {
	lower := strings.ToLower(response)
	anxietyIndicators := []string{
		"commit", "commits", "productive", "productivity",
		"work", "code", "enough", "not enough", "have to",
		"must", "need to", "should", "graded",
	}

	count := 0
	for _, indicator := range anxietyIndicators {
		if strings.Contains(lower, indicator) {
			count++
		}
	}

	score := float64(count) / 3.0
	if score > 1.0 {
		score = 1.0
	}

	return score >= 0.5, score
}

func checkHeightMention(response string) (bool, float64) {
	lower := strings.ToLower(response)
	heightKeywords := []string{
		"tall", "height", "reach", "high up", "easy for me",
		"taller than", "casually", "no problem",
	}

	count := 0
	for _, keyword := range heightKeywords {
		if strings.Contains(lower, keyword) {
			count++
		}
	}

	score := float64(count) / 2.0
	if score > 1.0 {
		score = 1.0
	}

	return score >= 0.4, score
}

// TestDanbotPersonalityEvals runs all eval cases against the danbot personality
func TestDanbotPersonalityEvals(t *testing.T) {
	// This test requires an actual OpenAI API key
	// It's designed to be run manually or in CI with proper credentials
	if testing.Short() {
		t.Skip("Skipping eval test in short mode")
	}

	// Load the base prompt
	bot := createTestBot(t)

	for _, evalCase := range danbotEvalCases {
		t.Run(evalCase.Name, func(t *testing.T) {
			// Generate a response using the bot's base prompt
			response := generateResponse(t, bot, evalCase.UserPrompt)

			t.Logf("Prompt: %s", evalCase.UserPrompt)
			t.Logf("Response: %s", response)

			// Check each trait
			allPassed := true
			for _, trait := range evalCase.ExpectedTraits {
				passed, score := trait.CheckFunc(response)

				status := "✓ PASS"
				if !passed {
					status = "✗ FAIL"
					allPassed = false
				}

				t.Logf("  %s [%s] Score: %.2f (min: %.2f) - %s",
					status, trait.Name, score, trait.MinScore, trait.Description)
			}

			if !allPassed {
				t.Errorf("Some personality traits did not meet minimum scores")
			}
		})
	}
}

// Helper function to create a test bot with real OpenAI client and danbo prompt
func createTestBot(t *testing.T) *AIBot {
	t.Helper()

	client := newOpenAIClient(t)

	promptBytes, err := os.ReadFile("../prompts/danbo.json")
	if err != nil {
		t.Fatalf("Failed to read danbo.json prompt: %v", err)
	}

	promptMessages := struct {
		Prompt []gpt.ChatCompletionMessage
	}{}
	err = json.Unmarshal(promptBytes, &promptMessages)
	if err != nil {
		t.Fatalf("Failed to parse danbo.json prompt: %v", err)
	}

	return &AIBot{
		openapiClient: client,
		botCtx:        context.Background(),
		basePrompt:    promptMessages.Prompt,
		storage:       newMockThreadStore(),
		imageStorage:  newMockImageStore(),
	}
}

// Helper function to generate a response (you'll need to implement this)
func generateResponse(t *testing.T, bot *AIBot, prompt string) string {
	// This would call the OpenAI API with the base prompt + user prompt
	// Implementation depends on your test setup requirements
	ctx := context.Background()

	request := gpt.ChatCompletionRequest{
		Model: gpt.GPT3Dot5Turbo,
		Messages: append(bot.basePrompt, gpt.ChatCompletionMessage{
			Role:    "user",
			Content: prompt,
		}),
	}

	response, err := bot.openapiClient.CreateChatCompletion(ctx, request)
	if err != nil {
		t.Fatalf("Failed to get response: %v", err)
	}

	return response.Choices[0].Message.Content
}
