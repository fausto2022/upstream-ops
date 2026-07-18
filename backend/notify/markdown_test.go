package notify

import (
	"strings"
	"testing"

	"github.com/fausto2022/relaydeck/backend/storage"
)

func TestMarkdownDetailsFormatsReadableFields(t *testing.T) {
	body := MarkdownDetails(
		"检测到异常。",
		Detail("渠道", "open_ai"),
		Detail("错误", "first line\nsecond `line`"),
	) + MarkdownNote("处理建议", "检查配置。")

	for _, want := range []string{
		"> 检测到异常。",
		"- *渠道：* `open_ai`",
		"- *错误：* `first line second 'line'`",
		"> *处理建议：* 检查配置。",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("body missing %q: %s", want, body)
		}
	}
}

func TestDispatcherUsesFixedRelayDeckPrefix(t *testing.T) {
	dispatcher := &Dispatcher{}
	got := dispatcher.withNotificationPrefix(Message{Subject: "余额不足"})
	if got.Subject != "[RelayDeck] 余额不足" {
		t.Fatalf("subject = %q", got.Subject)
	}

	got = dispatcher.withNotificationPrefix(got)
	if got.Subject != "[RelayDeck] 余额不足" {
		t.Fatalf("prefix duplicated: %q", got.Subject)
	}
}

func TestBuildRateBatchMessageUsesMarkdown(t *testing.T) {
	message := BuildRateBatchMessage(&storage.Channel{ID: 1, Name: "OpenAI"}, storage.EventRateChanged, []RateChange{
		{GroupName: "default", OldRatio: 1, NewRatio: 1.2, OldComp: 1, NewComp: 1.1},
	})

	if message.Subject != "倍率上涨 · OpenAI · default" {
		t.Fatalf("subject = %q", message.Subject)
	}
	for _, want := range []string{
		"> 分组倍率发生上涨。",
		"- *渠道：* `OpenAI`",
		"- *倍率：* `1 -> 1.2`",
		"- *补全倍率：* `1 -> 1.1`",
	} {
		if !strings.Contains(message.Body, want) {
			t.Fatalf("body missing %q: %s", want, message.Body)
		}
	}
}
