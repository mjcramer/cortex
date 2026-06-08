package slack

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/slack-go/slack/slackevents"

	"github.com/mjcramer/cortex/internal/config"
)

// slackMock is a minimal, configurable stand-in for the Slack conversations.*
// API used by App. Toggle the behavior fields before driving the App.
type slackMock struct {
	mu sync.Mutex

	// behavior toggles (set before use)
	createNameTaken bool   // create returns name_taken
	listArchived    bool   // list reports the channel as archived
	unarchiveErr    string // if set, unarchive returns this error code

	// recorded calls
	createCalls int
	renamed     []string
	archived    []string
	unarchived  []string
	joined      []string
	listed      bool
}

func newSlackMock(t *testing.T) (*App, *slackMock) {
	t.Helper()
	m := &slackMock{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		m.mu.Lock()
		defer m.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/conversations.create":
			m.createCalls++
			if m.createNameTaken {
				_, _ = io.WriteString(w, `{"ok":false,"error":"name_taken"}`)
				return
			}
			_, _ = fmt.Fprintf(w, `{"ok":true,"channel":{"id":"C%d","name":"agent-demo","is_archived":false}}`, m.createCalls)
		case "/conversations.rename":
			m.renamed = append(m.renamed, r.PostForm.Get("channel"))
			_, _ = io.WriteString(w, `{"ok":true,"channel":{"id":"C1","name":"archived-c1"}}`)
		case "/conversations.archive":
			m.archived = append(m.archived, r.PostForm.Get("channel"))
			_, _ = io.WriteString(w, `{"ok":true}`)
		case "/conversations.list":
			m.listed = true
			_, _ = fmt.Fprintf(w, `{"ok":true,"channels":[{"id":"C1","name":"agent-demo","is_archived":%t}],"response_metadata":{"next_cursor":""}}`, m.listArchived)
		case "/conversations.unarchive":
			m.unarchived = append(m.unarchived, r.PostForm.Get("channel"))
			if m.unarchiveErr != "" {
				_, _ = fmt.Fprintf(w, `{"ok":false,"error":"%s"}`, m.unarchiveErr)
				return
			}
			_, _ = io.WriteString(w, `{"ok":true}`)
		case "/conversations.join":
			m.joined = append(m.joined, r.PostForm.Get("channel"))
			_, _ = io.WriteString(w, `{"ok":true,"channel":{"id":"C1","name":"agent-demo"}}`)
		default:
			_, _ = io.WriteString(w, `{"ok":true}`)
		}
	}))
	t.Cleanup(srv.Close)

	app := NewApp(&config.SlackConfig{
		BotToken:      "xoxb-test",
		SigningSecret: "secret",
		ChannelPrefix: "agent-",
		APIBaseURL:    srv.URL,
	}, nil)
	return app, m
}

func TestEnsureChannelCachesActiveChannel(t *testing.T) {
	app, m := newSlackMock(t)
	ctx := context.Background()

	if _, err := app.EnsureChannelForAgent(ctx, "demo"); err != nil {
		t.Fatalf("first ensure: %v", err)
	}
	if _, err := app.EnsureChannelForAgent(ctx, "demo"); err != nil {
		t.Fatalf("second ensure: %v", err)
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if m.createCalls != 1 {
		t.Fatalf("createCalls = %d, want 1 (second ensure should hit the cache)", m.createCalls)
	}
}

func TestDestroyRenamesThenArchivesAndRecreateIsFresh(t *testing.T) {
	app, m := newSlackMock(t)
	ctx := context.Background()

	id1, err := app.EnsureChannelForAgent(ctx, "demo")
	if err != nil || id1 != "C1" {
		t.Fatalf("create: id=%q err=%v", id1, err)
	}

	// Destroy: should rename the channel aside (freeing the name) and archive
	// it, and invalidate the cache.
	if err := app.ArchiveChannel(ctx, id1); err != nil {
		t.Fatalf("archive: %v", err)
	}

	// Recreate the same name: the name is free, so a brand-new channel is made
	// (no unarchive needed).
	id2, err := app.EnsureChannelForAgent(ctx, "demo")
	if err != nil {
		t.Fatalf("recreate: %v", err)
	}
	if id2 != "C2" {
		t.Fatalf("recreate id = %q, want C2 (a fresh channel)", id2)
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.renamed) != 1 || m.renamed[0] != "C1" {
		t.Fatalf("renamed = %v, want [C1] (channel moved aside before archive)", m.renamed)
	}
	if len(m.archived) != 1 || m.archived[0] != "C1" {
		t.Fatalf("archived = %v, want [C1]", m.archived)
	}
	if len(m.unarchived) != 0 {
		t.Fatalf("unarchived = %v, want none (rename frees the name, no reopen)", m.unarchived)
	}
	if m.createCalls != 2 {
		t.Fatalf("createCalls = %d, want 2 (recreate makes a fresh channel)", m.createCalls)
	}
}

func TestEnsureChannelJoinsActiveExistingChannel(t *testing.T) {
	app, m := newSlackMock(t)
	m.createNameTaken = true // a channel with this name already exists...
	m.listArchived = false   // ...and it is active

	id, err := app.EnsureChannelForAgent(context.Background(), "demo")
	if err != nil {
		t.Fatalf("ensure: %v", err)
	}
	if id != "C1" {
		t.Fatalf("id = %q, want C1", id)
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.joined) != 1 || m.joined[0] != "C1" {
		t.Fatalf("joined = %v, want [C1]", m.joined)
	}
}

func TestEnsureChannelErrorsOnUnreopenableArchived(t *testing.T) {
	app, m := newSlackMock(t)
	m.createNameTaken = true          // an existing channel holds the name...
	m.listArchived = true             // ...and it is archived...
	m.unarchiveErr = "not_in_channel" // ...and the bot can't reopen it

	_, err := app.EnsureChannelForAgent(context.Background(), "demo")
	if err == nil {
		t.Fatal("expected an error for an unreopenable archived channel, got nil")
	}
	if !strings.Contains(err.Error(), "archived") || !strings.Contains(err.Error(), "not_in_channel") {
		t.Fatalf("error = %q, want it to mention archived + the slack cause", err)
	}
}

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
