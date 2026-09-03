package feishu

import (
	"testing"

	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
)

func TestMentionsBot(t *testing.T) {
	user, bot := "user", "bot"
	if mentionsBot([]*larkim.MentionEvent{{MentionedType: &user}}) {
		t.Fatal("user mention accepted")
	}
	if !mentionsBot([]*larkim.MentionEvent{{MentionedType: &bot}}) {
		t.Fatal("bot mention rejected")
	}
}

func TestDeliveryUUIDIsStable(t *testing.T) {
	a, b := deliveryUUID("message-1"), deliveryUUID("message-1")
	if a != b || len(a) != 36 {
		t.Fatalf("uuid=%q duplicate=%q", a, b)
	}
	if a == deliveryUUID("message-2") {
		t.Fatal("different messages share UUID")
	}
}
