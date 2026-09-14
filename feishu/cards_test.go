package feishu

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestCardResponseUsesRawCardType(t *testing.T) {
	response := cardResponse(menuCard("", ""))
	if response == nil || response.Card == nil {
		t.Fatal("card response is missing its card payload")
	}
	if response.Card.Type != "raw" {
		t.Fatalf("card response type = %q, want raw", response.Card.Type)
	}
}

func TestMenuCardWithLinkOpensAuthorizationPage(t *testing.T) {
	link := "https://auth.example.com/authorize/tok123"
	body, err := json.Marshal(menuCard(link, "5m"))
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{"打开授权页面", `"url":"` + link, "5m", "我的授权", "撤销授权"} {
		if !strings.Contains(string(body), expected) {
			t.Errorf("menu card with link missing %q: %s", expected, body)
		}
	}
	if strings.Contains(string(body), `"action":"apply"`) {
		t.Errorf("menu card with link keeps manual apply fallback: %s", body)
	}
}

func TestApplyInstructionsUseDocumentationAddressAndIPLookupLink(t *testing.T) {
	encoded := fmt.Sprint(applyInstructionsCard("24h"))
	if contains := "118.25.93.30"; strings.Contains(encoded, contains) {
		t.Fatalf("apply instructions contain a real IP example: %s", encoded)
	}
	for _, expected := range []string{"203.0.113.10 24h", "默认 24h", "https://ipv4.icanhazip.com/", "https://www.cip.cc/"} {
		if !strings.Contains(encoded, expected) {
			t.Fatalf("apply instructions do not contain %q: %s", encoded, expected)
		}
	}
}

func TestApplyLinkCardContainsLinkAndLimits(t *testing.T) {
	body, err := json.Marshal(applyLinkCard("https://auth.example.com/authorize/tok123", "4h", "5m"))
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{"打开授权页面", `"url":"https://auth.example.com/authorize/tok123`, "4h", "5m", `"action":"menu"`} {
		if !strings.Contains(string(body), expected) {
			t.Errorf("apply link card missing %q: %s", expected, body)
		}
	}
}
