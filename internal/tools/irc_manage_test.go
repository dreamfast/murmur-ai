package tools

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

// mockIRCManager is a test double for the IRCManager interface.
type mockIRCManager struct {
	mu       sync.Mutex
	joined   map[string]struct{}
	sent     []sentMessage
	topics   map[string]string
	kicks    []kickRecord
	bans     map[string][]string // channel -> list of masks
	modes    []modeRecord
	joinErr  error
	partErr  error
	topicErr error
	kickErr  error
	banErr   error
	unbanErr error
	modeErr  error
}

type sentMessage struct {
	channel string
	message string
}

type kickRecord struct {
	channel string
	user    string
	reason  string
}

type modeRecord struct {
	channel string
	mode    string
	params  []string
}

func newMockIRC(channels ...string) *mockIRCManager {
	m := &mockIRCManager{
		joined: make(map[string]struct{}),
		topics: make(map[string]string),
		bans:   make(map[string][]string),
	}
	for _, ch := range channels {
		m.joined[ch] = struct{}{}
	}
	return m
}

func (m *mockIRCManager) Join(channel string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.joinErr != nil {
		return m.joinErr
	}
	m.joined[channel] = struct{}{}
	return nil
}

func (m *mockIRCManager) Part(channel string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.partErr != nil {
		return m.partErr
	}
	delete(m.joined, channel)
	return nil
}

func (m *mockIRCManager) Send(channel, message string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sent = append(m.sent, sentMessage{channel, message})
}

func (m *mockIRCManager) SetTopic(channel, topic string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.topicErr != nil {
		return m.topicErr
	}
	m.topics[channel] = topic
	return nil
}

func (m *mockIRCManager) Kick(channel, user, reason string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.kickErr != nil {
		return m.kickErr
	}
	m.kicks = append(m.kicks, kickRecord{channel, user, reason})
	return nil
}

func (m *mockIRCManager) Ban(channel, mask string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.banErr != nil {
		return m.banErr
	}
	m.bans[channel] = append(m.bans[channel], mask)
	return nil
}

func (m *mockIRCManager) Unban(channel, mask string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.unbanErr != nil {
		return m.unbanErr
	}
	// Remove mask from bans list.
	masks := m.bans[channel]
	for i, b := range masks {
		if b == mask {
			m.bans[channel] = append(masks[:i], masks[i+1:]...)
			break
		}
	}
	return nil
}

func (m *mockIRCManager) SetMode(channel, mode string, params ...string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.modeErr != nil {
		return m.modeErr
	}
	m.modes = append(m.modes, modeRecord{channel, mode, params})
	return nil
}

func (m *mockIRCManager) Channels() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	result := make([]string, 0, len(m.joined))
	for ch := range m.joined {
		result = append(result, ch)
	}
	return result
}

// mockMemoryReader is a test double for the MemoryReader interface.
type mockMemoryReader struct {
	history      map[string][]MemoryMessage
	historyCount map[string]int
	historyErr   error
}

func newMockMemory() *mockMemoryReader {
	return &mockMemoryReader{
		history:      make(map[string][]MemoryMessage),
		historyCount: make(map[string]int),
	}
}

func (m *mockMemoryReader) GetHistory(channel string, limit int) ([]MemoryMessage, error) {
	if m.historyErr != nil {
		return nil, m.historyErr
	}
	msgs := m.history[channel]
	if limit > 0 && limit < len(msgs) {
		msgs = msgs[len(msgs)-limit:]
	}
	return msgs, nil
}

func (m *mockMemoryReader) GetHistoryCount(channel string) (int, error) {
	if m.historyErr != nil {
		return 0, m.historyErr
	}
	if count, ok := m.historyCount[channel]; ok {
		return count, nil
	}
	return len(m.history[channel]), nil
}

// mockChannelPersister is a test double for the ChannelPersister interface.
type mockChannelPersister struct {
	mu       sync.Mutex
	channels map[string]bool // channel -> auto_join state
	err      error           // if set, all calls return this error
}

func newMockPersister() *mockChannelPersister {
	return &mockChannelPersister{
		channels: make(map[string]bool),
	}
}

func (p *mockChannelPersister) SetAutoJoin(channel string, autoJoin bool) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.err != nil {
		return p.err
	}
	p.channels[channel] = autoJoin
	return nil
}

func (p *mockChannelPersister) getAutoJoin(channel string) (bool, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	v, ok := p.channels[channel]
	return v, ok
}

func TestIRCManage_Join(t *testing.T) {
	t.Parallel()

	irc := newMockIRC("#main")
	tool := NewIRCManageTool(irc, nil, "#murmur-bus", nil)

	result, err := tool.Handler(context.Background(), map[string]any{
		"action":  "join",
		"channel": "#new-channel",
	})
	if err != nil {
		t.Fatalf("join: %v", err)
	}
	if !strings.Contains(result, "#new-channel") {
		t.Errorf("result = %q, want mention of #new-channel", result)
	}

	// Verify the channel was joined.
	channels := irc.Channels()
	found := false
	for _, ch := range channels {
		if ch == "#new-channel" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected #new-channel in joined channels: %v", channels)
	}
}

func TestIRCManage_JoinRateLimit(t *testing.T) {
	t.Parallel()

	irc := newMockIRC("#main")
	tool := NewIRCManageTool(irc, nil, "#murmur-bus", nil)

	// First join should succeed.
	_, err := tool.Handler(context.Background(), map[string]any{
		"action":  "join",
		"channel": "#channel1",
	})
	if err != nil {
		t.Fatalf("first join: %v", err)
	}

	// Immediate second join should be rate limited.
	_, err = tool.Handler(context.Background(), map[string]any{
		"action":  "join",
		"channel": "#channel2",
	})
	if err == nil {
		t.Fatal("expected rate limit error on immediate second join")
	}
	if !strings.Contains(err.Error(), "rate limited") {
		t.Errorf("error = %q, want rate limit message", err.Error())
	}
}

func TestIRCManage_Part(t *testing.T) {
	t.Parallel()

	irc := newMockIRC("#main", "#other")
	tool := NewIRCManageTool(irc, nil, "#murmur-bus", nil)

	result, err := tool.Handler(context.Background(), map[string]any{
		"action":  "part",
		"channel": "#other",
	})
	if err != nil {
		t.Fatalf("part: %v", err)
	}
	if !strings.Contains(result, "#other") {
		t.Errorf("result = %q, want mention of #other", result)
	}
}

func TestIRCManage_PartBusChannel(t *testing.T) {
	t.Parallel()

	irc := newMockIRC("#main", "#murmur-bus")
	tool := NewIRCManageTool(irc, nil, "#murmur-bus", nil)

	_, err := tool.Handler(context.Background(), map[string]any{
		"action":  "part",
		"channel": "#murmur-bus",
	})
	if err == nil {
		t.Fatal("expected error when parting bus channel")
	}
	if !strings.Contains(err.Error(), "bus channel") {
		t.Errorf("error = %q, want bus channel protection message", err.Error())
	}
}

func TestIRCManage_Send(t *testing.T) {
	t.Parallel()

	irc := newMockIRC("#main")
	tool := NewIRCManageTool(irc, nil, "#murmur-bus", nil)

	result, err := tool.Handler(context.Background(), map[string]any{
		"action":  "send",
		"channel": "#main",
		"message": "hello world",
	})
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	if !strings.Contains(result, "#main") {
		t.Errorf("result = %q, want mention of #main", result)
	}

	irc.mu.Lock()
	defer irc.mu.Unlock()
	if len(irc.sent) != 1 {
		t.Fatalf("expected 1 sent message, got %d", len(irc.sent))
	}
	if irc.sent[0].channel != "#main" || irc.sent[0].message != "hello world" {
		t.Errorf("sent = %+v, want {#main, hello world}", irc.sent[0])
	}
}

func TestIRCManage_SendNotJoined(t *testing.T) {
	t.Parallel()

	irc := newMockIRC("#main")
	tool := NewIRCManageTool(irc, nil, "#murmur-bus", nil)

	_, err := tool.Handler(context.Background(), map[string]any{
		"action":  "send",
		"channel": "#other",
		"message": "hello",
	})
	if err == nil {
		t.Fatal("expected error when sending to non-joined channel")
	}
	if !strings.Contains(err.Error(), "not joined") {
		t.Errorf("error = %q, want not-joined message", err.Error())
	}
}

func TestIRCManage_Topic(t *testing.T) {
	t.Parallel()

	irc := newMockIRC("#main")
	tool := NewIRCManageTool(irc, nil, "#murmur-bus", nil)

	result, err := tool.Handler(context.Background(), map[string]any{
		"action":  "topic",
		"channel": "#main",
		"message": "New topic!",
	})
	if err != nil {
		t.Fatalf("topic: %v", err)
	}
	if !strings.Contains(result, "#main") {
		t.Errorf("result = %q, want mention of #main", result)
	}

	irc.mu.Lock()
	defer irc.mu.Unlock()
	if irc.topics["#main"] != "New topic!" {
		t.Errorf("topic = %q, want %q", irc.topics["#main"], "New topic!")
	}
}

func TestIRCManage_ListChannels(t *testing.T) {
	t.Parallel()

	irc := newMockIRC("#alpha", "#bravo")
	tool := NewIRCManageTool(irc, nil, "#murmur-bus", nil)

	result, err := tool.Handler(context.Background(), map[string]any{
		"action": "list_channels",
	})
	if err != nil {
		t.Fatalf("list_channels: %v", err)
	}
	if !strings.Contains(result, "#alpha") || !strings.Contains(result, "#bravo") {
		t.Errorf("result = %q, want both channels listed", result)
	}
}

func TestIRCManage_ListChannelsEmpty(t *testing.T) {
	t.Parallel()

	irc := newMockIRC()
	tool := NewIRCManageTool(irc, nil, "#murmur-bus", nil)

	result, err := tool.Handler(context.Background(), map[string]any{
		"action": "list_channels",
	})
	if err != nil {
		t.Fatalf("list_channels: %v", err)
	}
	if !strings.Contains(result, "Not joined") {
		t.Errorf("result = %q, want 'Not joined' message", result)
	}
}

func TestIRCManage_ReadHistory(t *testing.T) {
	t.Parallel()

	irc := newMockIRC("#dev")
	mem := newMockMemory()
	mem.history["#dev"] = []MemoryMessage{
		{Role: "user", Content: "hello"},
		{Role: "assistant", Content: "hi there"},
		{Role: "user", Content: "how are you?"},
	}
	tool := NewIRCManageTool(irc, mem, "#murmur-bus", nil)

	result, err := tool.Handler(context.Background(), map[string]any{
		"action":  "read_history",
		"channel": "#dev",
	})
	if err != nil {
		t.Fatalf("read_history: %v", err)
	}
	if !strings.Contains(result, "hello") {
		t.Errorf("result missing 'hello': %q", result)
	}
	if !strings.Contains(result, "hi there") {
		t.Errorf("result missing 'hi there': %q", result)
	}
	if !strings.Contains(result, "3 messages") {
		t.Errorf("result missing message count: %q", result)
	}
}

func TestIRCManage_ReadHistoryNotJoined(t *testing.T) {
	t.Parallel()

	irc := newMockIRC("#main")
	mem := newMockMemory()
	tool := NewIRCManageTool(irc, mem, "#murmur-bus", nil)

	_, err := tool.Handler(context.Background(), map[string]any{
		"action":  "read_history",
		"channel": "#other",
	})
	if err == nil {
		t.Fatal("expected error when reading history of non-joined channel")
	}
	if !strings.Contains(err.Error(), "not joined") {
		t.Errorf("error = %q, want not-joined message", err.Error())
	}
}

func TestIRCManage_ReadHistoryNoMemory(t *testing.T) {
	t.Parallel()

	irc := newMockIRC("#main")
	tool := NewIRCManageTool(irc, nil, "#murmur-bus", nil)

	_, err := tool.Handler(context.Background(), map[string]any{
		"action":  "read_history",
		"channel": "#main",
	})
	if err == nil {
		t.Fatal("expected error when memory reader is nil")
	}
	if !strings.Contains(err.Error(), "memory reader not available") {
		t.Errorf("error = %q, want memory reader message", err.Error())
	}
}

func TestIRCManage_ReadHistoryWithLimit(t *testing.T) {
	t.Parallel()

	irc := newMockIRC("#dev")
	mem := newMockMemory()
	for i := 0; i < 50; i++ {
		mem.history["#dev"] = append(mem.history["#dev"], MemoryMessage{
			Role:    "user",
			Content: fmt.Sprintf("message %d", i),
		})
	}
	tool := NewIRCManageTool(irc, mem, "#murmur-bus", nil)

	result, err := tool.Handler(context.Background(), map[string]any{
		"action":  "read_history",
		"channel": "#dev",
		"limit":   float64(5),
	})
	if err != nil {
		t.Fatalf("read_history with limit: %v", err)
	}
	if !strings.Contains(result, "5 messages") {
		t.Errorf("result = %q, want 5 messages", result)
	}
}

func TestIRCManage_SummarizeChannel(t *testing.T) {
	t.Parallel()

	irc := newMockIRC("#dev")
	mem := newMockMemory()
	mem.history["#dev"] = []MemoryMessage{
		{Role: "user", Content: "let's discuss the API"},
		{Role: "assistant", Content: "sure, what about it?"},
	}
	mem.historyCount["#dev"] = 2
	tool := NewIRCManageTool(irc, mem, "#murmur-bus", nil)

	result, err := tool.Handler(context.Background(), map[string]any{
		"action":  "summarize_channel",
		"channel": "#dev",
	})
	if err != nil {
		t.Fatalf("summarize_channel: %v", err)
	}
	if !strings.Contains(result, "#dev") {
		t.Errorf("result missing channel name: %q", result)
	}
	if !strings.Contains(result, "2 messages total") {
		t.Errorf("result missing message count: %q", result)
	}
}

func TestIRCManage_InvalidChannel(t *testing.T) {
	t.Parallel()

	irc := newMockIRC()
	tool := NewIRCManageTool(irc, nil, "#murmur-bus", nil)

	tests := []struct {
		name    string
		channel string
	}{
		{"empty", ""},
		{"no hash", "channel"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := tool.Handler(context.Background(), map[string]any{
				"action":  "join",
				"channel": tt.channel,
			})
			if err == nil {
				t.Errorf("expected error for channel %q", tt.channel)
			}
		})
	}
}

func TestIRCManage_UnknownAction(t *testing.T) {
	t.Parallel()

	irc := newMockIRC()
	tool := NewIRCManageTool(irc, nil, "#murmur-bus", nil)

	_, err := tool.Handler(context.Background(), map[string]any{
		"action": "explode",
	})
	if err == nil {
		t.Fatal("expected error for unknown action")
	}
	if !strings.Contains(err.Error(), "unknown action") {
		t.Errorf("error = %q, want unknown action message", err.Error())
	}
}

func TestIRCManage_SendMissingMessage(t *testing.T) {
	t.Parallel()

	irc := newMockIRC("#main")
	tool := NewIRCManageTool(irc, nil, "#murmur-bus", nil)

	_, err := tool.Handler(context.Background(), map[string]any{
		"action":  "send",
		"channel": "#main",
	})
	if err == nil {
		t.Fatal("expected error when message is missing")
	}
	if !strings.Contains(err.Error(), "message is required") {
		t.Errorf("error = %q, want message required error", err.Error())
	}
}

func TestIRCManage_TopicNotJoined(t *testing.T) {
	t.Parallel()

	irc := newMockIRC("#main")
	tool := NewIRCManageTool(irc, nil, "#murmur-bus", nil)

	_, err := tool.Handler(context.Background(), map[string]any{
		"action":  "topic",
		"channel": "#other",
		"message": "New topic",
	})
	if err == nil {
		t.Fatal("expected error when setting topic on non-joined channel")
	}
	if !strings.Contains(err.Error(), "not joined") {
		t.Errorf("error = %q, want not-joined message", err.Error())
	}
}

func TestIRCManage_JoinAfterRateLimitExpires(t *testing.T) {
	t.Parallel()

	irc := newMockIRC("#main")
	// Create tool with a very short rate limit for testing.
	mgr := &ircManageTool{
		irc:        irc,
		busChannel: "#murmur-bus",
	}
	// Set lastJoin to the past so rate limit is expired.
	mgr.lastJoin = time.Now().Add(-5 * time.Second)

	tool := Tool{
		Name:       "irc_manage",
		Handler:    mgr.handle,
		Parameters: ircManageParams,
	}

	_, err := tool.Handler(context.Background(), map[string]any{
		"action":  "join",
		"channel": "#new",
	})
	if err != nil {
		t.Fatalf("join after rate limit expired: %v", err)
	}
}

func TestIRCManage_ReadHistoryEmpty(t *testing.T) {
	t.Parallel()

	irc := newMockIRC("#dev")
	mem := newMockMemory()
	tool := NewIRCManageTool(irc, mem, "#murmur-bus", nil)

	result, err := tool.Handler(context.Background(), map[string]any{
		"action":  "read_history",
		"channel": "#dev",
	})
	if err != nil {
		t.Fatalf("read_history empty: %v", err)
	}
	if !strings.Contains(result, "No history") {
		t.Errorf("result = %q, want 'No history' message", result)
	}
}

func TestIRCManage_SummarizeChannelEmpty(t *testing.T) {
	t.Parallel()

	irc := newMockIRC("#dev")
	mem := newMockMemory()
	tool := NewIRCManageTool(irc, mem, "#murmur-bus", nil)

	result, err := tool.Handler(context.Background(), map[string]any{
		"action":  "summarize_channel",
		"channel": "#dev",
	})
	if err != nil {
		t.Fatalf("summarize_channel empty: %v", err)
	}
	if !strings.Contains(result, "No history to summarize") {
		t.Errorf("result = %q, want 'No history to summarize' message", result)
	}
}

func TestIRCManage_JoinPersistsAutoJoin(t *testing.T) {
	t.Parallel()

	irc := newMockIRC("#main")
	persister := newMockPersister()
	tool := NewIRCManageTool(irc, nil, "#murmur-bus", persister)

	_, err := tool.Handler(context.Background(), map[string]any{
		"action":  "join",
		"channel": "#dev",
	})
	if err != nil {
		t.Fatalf("join: %v", err)
	}

	autoJoin, ok := persister.getAutoJoin("#dev")
	if !ok {
		t.Fatal("expected persister to have #dev entry")
	}
	if !autoJoin {
		t.Error("expected auto_join=true after join, got false")
	}
}

func TestIRCManage_PartClearsAutoJoin(t *testing.T) {
	t.Parallel()

	irc := newMockIRC("#main", "#dev")
	persister := newMockPersister()
	// Pre-set auto_join for #dev.
	_ = persister.SetAutoJoin("#dev", true)

	tool := NewIRCManageTool(irc, nil, "#murmur-bus", persister)

	_, err := tool.Handler(context.Background(), map[string]any{
		"action":  "part",
		"channel": "#dev",
	})
	if err != nil {
		t.Fatalf("part: %v", err)
	}

	autoJoin, ok := persister.getAutoJoin("#dev")
	if !ok {
		t.Fatal("expected persister to have #dev entry")
	}
	if autoJoin {
		t.Error("expected auto_join=false after part, got true")
	}
}

func TestIRCManage_JoinPersisterErrorNonFatal(t *testing.T) {
	t.Parallel()

	irc := newMockIRC("#main")
	persister := newMockPersister()
	persister.err = fmt.Errorf("db write failed")

	tool := NewIRCManageTool(irc, nil, "#murmur-bus", persister)

	// Join should succeed even though persister fails.
	result, err := tool.Handler(context.Background(), map[string]any{
		"action":  "join",
		"channel": "#dev",
	})
	if err != nil {
		t.Fatalf("join should succeed despite persister error: %v", err)
	}
	if !strings.Contains(result, "#dev") {
		t.Errorf("result = %q, want mention of #dev", result)
	}

	// Verify the channel was still joined on IRC.
	found := false
	for _, ch := range irc.Channels() {
		if ch == "#dev" {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected #dev in joined channels despite persister error")
	}
}

func TestIRCManage_PartPersisterErrorNonFatal(t *testing.T) {
	t.Parallel()

	irc := newMockIRC("#main", "#dev")
	persister := newMockPersister()
	persister.err = fmt.Errorf("db write failed")

	tool := NewIRCManageTool(irc, nil, "#murmur-bus", persister)

	// Part should succeed even though persister fails.
	result, err := tool.Handler(context.Background(), map[string]any{
		"action":  "part",
		"channel": "#dev",
	})
	if err != nil {
		t.Fatalf("part should succeed despite persister error: %v", err)
	}
	if !strings.Contains(result, "#dev") {
		t.Errorf("result = %q, want mention of #dev", result)
	}
}

func TestIRCManage_Kick(t *testing.T) {
	t.Parallel()

	irc := newMockIRC("#main")
	tool := NewIRCManageTool(irc, nil, "#murmur-bus", nil)

	result, err := tool.Handler(context.Background(), map[string]any{
		"action":  "kick",
		"channel": "#main",
		"nick":    "baduser",
		"message": "spamming",
	})
	if err != nil {
		t.Fatalf("kick: %v", err)
	}
	if !strings.Contains(result, "baduser") || !strings.Contains(result, "#main") {
		t.Errorf("result = %q, want mention of baduser and #main", result)
	}
	if !strings.Contains(result, "spamming") {
		t.Errorf("result = %q, want mention of reason", result)
	}

	irc.mu.Lock()
	defer irc.mu.Unlock()
	if len(irc.kicks) != 1 {
		t.Fatalf("expected 1 kick, got %d", len(irc.kicks))
	}
	if irc.kicks[0].user != "baduser" || irc.kicks[0].reason != "spamming" {
		t.Errorf("kick = %+v, want {baduser, spamming}", irc.kicks[0])
	}
}

func TestIRCManage_KickNoReason(t *testing.T) {
	t.Parallel()

	irc := newMockIRC("#main")
	tool := NewIRCManageTool(irc, nil, "#murmur-bus", nil)

	result, err := tool.Handler(context.Background(), map[string]any{
		"action":  "kick",
		"channel": "#main",
		"nick":    "baduser",
	})
	if err != nil {
		t.Fatalf("kick no reason: %v", err)
	}
	if strings.Contains(result, "(") {
		t.Errorf("result = %q, should not contain reason parenthetical", result)
	}
}

func TestIRCManage_KickMissingNick(t *testing.T) {
	t.Parallel()

	irc := newMockIRC("#main")
	tool := NewIRCManageTool(irc, nil, "#murmur-bus", nil)

	_, err := tool.Handler(context.Background(), map[string]any{
		"action":  "kick",
		"channel": "#main",
	})
	if err == nil {
		t.Fatal("expected error when nick is missing")
	}
	if !strings.Contains(err.Error(), "nick is required") {
		t.Errorf("error = %q, want nick required message", err.Error())
	}
}

func TestIRCManage_KickNotJoined(t *testing.T) {
	t.Parallel()

	irc := newMockIRC("#main")
	tool := NewIRCManageTool(irc, nil, "#murmur-bus", nil)

	_, err := tool.Handler(context.Background(), map[string]any{
		"action":  "kick",
		"channel": "#other",
		"nick":    "baduser",
	})
	if err == nil {
		t.Fatal("expected error when not joined")
	}
	if !strings.Contains(err.Error(), "not joined") {
		t.Errorf("error = %q, want not-joined message", err.Error())
	}
}

func TestIRCManage_Ban(t *testing.T) {
	t.Parallel()

	irc := newMockIRC("#main")
	tool := NewIRCManageTool(irc, nil, "#murmur-bus", nil)

	result, err := tool.Handler(context.Background(), map[string]any{
		"action":  "ban",
		"channel": "#main",
		"mask":    "baduser!*@*.evil.com",
	})
	if err != nil {
		t.Fatalf("ban: %v", err)
	}
	if !strings.Contains(result, "baduser!*@*.evil.com") {
		t.Errorf("result = %q, want mention of mask", result)
	}

	irc.mu.Lock()
	defer irc.mu.Unlock()
	if len(irc.bans["#main"]) != 1 || irc.bans["#main"][0] != "baduser!*@*.evil.com" {
		t.Errorf("bans = %v, want [baduser!*@*.evil.com]", irc.bans["#main"])
	}
}

func TestIRCManage_BanFromNick(t *testing.T) {
	t.Parallel()

	irc := newMockIRC("#main")
	tool := NewIRCManageTool(irc, nil, "#murmur-bus", nil)

	result, err := tool.Handler(context.Background(), map[string]any{
		"action":  "ban",
		"channel": "#main",
		"nick":    "baduser",
	})
	if err != nil {
		t.Fatalf("ban from nick: %v", err)
	}
	if !strings.Contains(result, "baduser!*@*") {
		t.Errorf("result = %q, want auto-generated mask", result)
	}

	irc.mu.Lock()
	defer irc.mu.Unlock()
	if len(irc.bans["#main"]) != 1 || irc.bans["#main"][0] != "baduser!*@*" {
		t.Errorf("bans = %v, want [baduser!*@*]", irc.bans["#main"])
	}
}

func TestIRCManage_BanMissingMaskAndNick(t *testing.T) {
	t.Parallel()

	irc := newMockIRC("#main")
	tool := NewIRCManageTool(irc, nil, "#murmur-bus", nil)

	_, err := tool.Handler(context.Background(), map[string]any{
		"action":  "ban",
		"channel": "#main",
	})
	if err == nil {
		t.Fatal("expected error when both mask and nick are missing")
	}
	if !strings.Contains(err.Error(), "mask or nick is required") {
		t.Errorf("error = %q, want mask/nick required message", err.Error())
	}
}

func TestIRCManage_Unban(t *testing.T) {
	t.Parallel()

	irc := newMockIRC("#main")
	// Pre-set a ban.
	irc.bans["#main"] = []string{"baduser!*@*"}
	tool := NewIRCManageTool(irc, nil, "#murmur-bus", nil)

	result, err := tool.Handler(context.Background(), map[string]any{
		"action":  "unban",
		"channel": "#main",
		"mask":    "baduser!*@*",
	})
	if err != nil {
		t.Fatalf("unban: %v", err)
	}
	if !strings.Contains(result, "Unbanned") {
		t.Errorf("result = %q, want Unbanned message", result)
	}
}

func TestIRCManage_UnbanFromNick(t *testing.T) {
	t.Parallel()

	irc := newMockIRC("#main")
	tool := NewIRCManageTool(irc, nil, "#murmur-bus", nil)

	result, err := tool.Handler(context.Background(), map[string]any{
		"action":  "unban",
		"channel": "#main",
		"nick":    "baduser",
	})
	if err != nil {
		t.Fatalf("unban from nick: %v", err)
	}
	if !strings.Contains(result, "baduser!*@*") {
		t.Errorf("result = %q, want auto-generated mask", result)
	}
}

func TestIRCManage_Op(t *testing.T) {
	t.Parallel()

	irc := newMockIRC("#main")
	tool := NewIRCManageTool(irc, nil, "#murmur-bus", nil)

	result, err := tool.Handler(context.Background(), map[string]any{
		"action":  "op",
		"channel": "#main",
		"nick":    "trusteduser",
	})
	if err != nil {
		t.Fatalf("op: %v", err)
	}
	if !strings.Contains(result, "trusteduser") || !strings.Contains(result, "op") {
		t.Errorf("result = %q, want mention of trusteduser and op", result)
	}

	irc.mu.Lock()
	defer irc.mu.Unlock()
	if len(irc.modes) != 1 {
		t.Fatalf("expected 1 mode change, got %d", len(irc.modes))
	}
	if irc.modes[0].mode != "+o" || len(irc.modes[0].params) != 1 || irc.modes[0].params[0] != "trusteduser" {
		t.Errorf("mode = %+v, want {+o, [trusteduser]}", irc.modes[0])
	}
}

func TestIRCManage_Deop(t *testing.T) {
	t.Parallel()

	irc := newMockIRC("#main")
	tool := NewIRCManageTool(irc, nil, "#murmur-bus", nil)

	result, err := tool.Handler(context.Background(), map[string]any{
		"action":  "deop",
		"channel": "#main",
		"nick":    "someuser",
	})
	if err != nil {
		t.Fatalf("deop: %v", err)
	}
	if !strings.Contains(result, "someuser") {
		t.Errorf("result = %q, want mention of someuser", result)
	}

	irc.mu.Lock()
	defer irc.mu.Unlock()
	if len(irc.modes) != 1 || irc.modes[0].mode != "-o" {
		t.Errorf("mode = %+v, want {-o, [someuser]}", irc.modes[0])
	}
}

func TestIRCManage_Voice(t *testing.T) {
	t.Parallel()

	irc := newMockIRC("#main")
	tool := NewIRCManageTool(irc, nil, "#murmur-bus", nil)

	result, err := tool.Handler(context.Background(), map[string]any{
		"action":  "voice",
		"channel": "#main",
		"nick":    "newuser",
	})
	if err != nil {
		t.Fatalf("voice: %v", err)
	}
	if !strings.Contains(result, "newuser") {
		t.Errorf("result = %q, want mention of newuser", result)
	}

	irc.mu.Lock()
	defer irc.mu.Unlock()
	if len(irc.modes) != 1 || irc.modes[0].mode != "+v" {
		t.Errorf("mode = %+v, want {+v, [newuser]}", irc.modes[0])
	}
}

func TestIRCManage_Devoice(t *testing.T) {
	t.Parallel()

	irc := newMockIRC("#main")
	tool := NewIRCManageTool(irc, nil, "#murmur-bus", nil)

	result, err := tool.Handler(context.Background(), map[string]any{
		"action":  "devoice",
		"channel": "#main",
		"nick":    "someuser",
	})
	if err != nil {
		t.Fatalf("devoice: %v", err)
	}
	if !strings.Contains(result, "someuser") {
		t.Errorf("result = %q, want mention of someuser", result)
	}

	irc.mu.Lock()
	defer irc.mu.Unlock()
	if len(irc.modes) != 1 || irc.modes[0].mode != "-v" {
		t.Errorf("mode = %+v, want {-v, [someuser]}", irc.modes[0])
	}
}

func TestIRCManage_ModeMissingNick(t *testing.T) {
	t.Parallel()

	irc := newMockIRC("#main")
	tool := NewIRCManageTool(irc, nil, "#murmur-bus", nil)

	actions := []string{"op", "deop", "voice", "devoice"}
	for _, action := range actions {
		t.Run(action, func(t *testing.T) {
			t.Parallel()
			_, err := tool.Handler(context.Background(), map[string]any{
				"action":  action,
				"channel": "#main",
			})
			if err == nil {
				t.Fatalf("expected error for %s without nick", action)
			}
			if !strings.Contains(err.Error(), "nick is required") {
				t.Errorf("error = %q, want nick required message", err.Error())
			}
		})
	}
}

func TestIRCManage_ModeNotJoined(t *testing.T) {
	t.Parallel()

	irc := newMockIRC("#main")
	tool := NewIRCManageTool(irc, nil, "#murmur-bus", nil)

	actions := []string{"op", "deop", "voice", "devoice", "kick", "ban", "unban"}
	for _, action := range actions {
		t.Run(action, func(t *testing.T) {
			t.Parallel()
			args := map[string]any{
				"action":  action,
				"channel": "#other",
				"nick":    "someuser",
			}
			if action == "ban" || action == "unban" {
				args["mask"] = "someuser!*@*"
			}
			_, err := tool.Handler(context.Background(), args)
			if err == nil {
				t.Fatalf("expected error for %s on non-joined channel", action)
			}
			if !strings.Contains(err.Error(), "not joined") {
				t.Errorf("error = %q, want not-joined message", err.Error())
			}
		})
	}
}

func TestIRCManage_KickError(t *testing.T) {
	t.Parallel()

	irc := newMockIRC("#main")
	irc.kickErr = fmt.Errorf("no chanop")
	tool := NewIRCManageTool(irc, nil, "#murmur-bus", nil)

	_, err := tool.Handler(context.Background(), map[string]any{
		"action":  "kick",
		"channel": "#main",
		"nick":    "baduser",
	})
	if err == nil {
		t.Fatal("expected error when kick fails")
	}
	if !strings.Contains(err.Error(), "no chanop") {
		t.Errorf("error = %q, want underlying error", err.Error())
	}
}

func TestIRCManage_BanNotJoined(t *testing.T) {
	t.Parallel()

	irc := newMockIRC("#main")
	tool := NewIRCManageTool(irc, nil, "#murmur-bus", nil)

	_, err := tool.Handler(context.Background(), map[string]any{
		"action":  "ban",
		"channel": "#other",
		"mask":    "bad!*@*",
	})
	if err == nil {
		t.Fatal("expected error when not joined")
	}
	if !strings.Contains(err.Error(), "not joined") {
		t.Errorf("error = %q, want not-joined message", err.Error())
	}
}

func TestIRCManage_NilPersisterNoError(t *testing.T) {
	t.Parallel()

	irc := newMockIRC("#main", "#dev")
	// nil persister — join and part should work without persistence.
	tool := NewIRCManageTool(irc, nil, "#murmur-bus", nil)

	_, err := tool.Handler(context.Background(), map[string]any{
		"action":  "join",
		"channel": "#new",
	})
	if err != nil {
		t.Fatalf("join with nil persister: %v", err)
	}

	_, err = tool.Handler(context.Background(), map[string]any{
		"action":  "part",
		"channel": "#dev",
	})
	if err != nil {
		t.Fatalf("part with nil persister: %v", err)
	}
}
