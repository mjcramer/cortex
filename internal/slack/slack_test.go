package slack

import (
	"testing"

	"github.com/slack-go/slack/slackevents"
)

func TestSanitizeChannelName(t *testing.T) {
	cases := map[string]string{
		"Reviewer Bot":     "reviewer-bot",
		"planner.v2":       "planner-v2",
		"--weird--name--":  "weird-name",
		"":                 "unnamed",
		"already-good":     "already-good",
		"ALLCAPS":          "allcaps",
		"agent/with/slash": "agent-with-slash",
	}
	for in, want := range cases {
		if got := sanitizeChannelName(in); got != want {
			t.Errorf("sanitizeChannelName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestHumanThreadReply(t *testing.T) {
	event := &slackevents.MessageEvent{
		Type:            "message",
		Channel:         "C123",
		User:            "U123",
		Text:            "ship it",
		ThreadTimeStamp: "1710000000.1234",
		TimeStamp:       "1710000001.1234",
	}
	reply := HumanThreadReply(event)
	if reply == nil {
		t.Fatal("expected reply, got nil")
	}
	if reply.Thread.ChannelID != "C123" || reply.Thread.ThreadTS != "1710000000.1234" {
		t.Fatalf("unexpected thread: %+v", reply.Thread)
	}
	if reply.UserID != "U123" || reply.Text != "ship it" {
		t.Fatalf("unexpected reply payload: %+v", reply)
	}
}

func TestHumanThreadReplyIgnoresBotMessages(t *testing.T) {
	event := &slackevents.MessageEvent{
		Type:            "message",
		Channel:         "C123",
		User:            "U123",
		Text:            "hi",
		ThreadTimeStamp: "1.1",
		TimeStamp:       "1.2",
		BotID:           "B123",
	}
	if reply := HumanThreadReply(event); reply != nil {
		t.Fatalf("expected nil for bot message, got %+v", reply)
	}
}

func TestHumanThreadReplyIgnoresTopLevelMessages(t *testing.T) {
	event := &slackevents.MessageEvent{
		Type:      "message",
		Channel:   "C123",
		User:      "U123",
		Text:      "hi",
		TimeStamp: "1.2",
	}
	if reply := HumanThreadReply(event); reply != nil {
		t.Fatalf("expected nil for non-threaded message, got %+v", reply)
	}
}
