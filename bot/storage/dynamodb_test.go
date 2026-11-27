package storage

import (
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
)

func TestThreadToConversationReversesNewestFirst(t *testing.T) {
	got := threadToConversation([]threadMessage{
		{Message: "c", MessageSource: "Bot"},
		{Message: "b"},
		{Message: "a"},
	})
	want := []ConversationMessage{{"user", "a"}, {"user", "b"}, {"assistant", "c"}}
	if len(got) != len(want) {
		t.Fatalf("got %v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v want %v", got, want)
		}
	}
}

func TestNewStorageRequiresTable(t *testing.T) {
	if _, err := NewStorage(aws.Config{}, ""); err == nil {
		t.Fatal("expected an error when CONVERSATION_TABLE is unset")
	}
}
