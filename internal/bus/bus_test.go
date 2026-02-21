package bus

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

// testMaxLineLen is the IRC max-line-len used by tests. Tests were written
// for Ergo's 8192-byte lines, so we keep that value for backward compat.
const testMaxLineLen = 8192

// testMaxBusMsg is the computed max bus message length for tests.
var testMaxBusMsg = MaxBusMessageLen(testMaxLineLen)

// testChunkSize is the computed chunk size for tests.
var testChunkSize = (testMaxBusMsg - partOverhead) / 2

func TestMarshalRegister(t *testing.T) {
	t.Parallel()

	msg := &RegisterMessage{
		Type:     TypeRegister,
		ClientID: "laptop",
		Hostname: "thinkpad",
		Tools: []ToolDef{
			{
				Name:        "shell",
				Description: "Run shell commands",
				Parameters:  json.RawMessage(`{"type":"object"}`),
			},
		},
	}

	data, err := MarshalMessage(msg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.Contains(data, `"type":"register"`) {
		t.Errorf("missing type field in: %s", data)
	}
	if !strings.Contains(data, `"client_id":"laptop"`) {
		t.Errorf("missing client_id in: %s", data)
	}
}

func TestParseRegister(t *testing.T) {
	t.Parallel()

	raw := `{"type":"register","client_id":"laptop","hostname":"thinkpad","tools":[{"name":"shell","description":"Run commands","parameters":{"type":"object"}}]}`

	msgType, msg, err := ParseMessage(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if msgType != TypeRegister {
		t.Errorf("type = %q, want %q", msgType, TypeRegister)
	}

	reg, ok := msg.(*RegisterMessage)
	if !ok {
		t.Fatalf("expected *RegisterMessage, got %T", msg)
	}
	if reg.ClientID != "laptop" {
		t.Errorf("ClientID = %q, want %q", reg.ClientID, "laptop")
	}
	if reg.Hostname != "thinkpad" {
		t.Errorf("Hostname = %q, want %q", reg.Hostname, "thinkpad")
	}
	if len(reg.Tools) != 1 {
		t.Fatalf("len(Tools) = %d, want 1", len(reg.Tools))
	}
	if reg.Tools[0].Name != "shell" {
		t.Errorf("Tools[0].Name = %q, want %q", reg.Tools[0].Name, "shell")
	}
}

func TestParseDeregister(t *testing.T) {
	t.Parallel()

	raw := `{"type":"deregister","client_id":"laptop"}`
	msgType, msg, err := ParseMessage(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if msgType != TypeDeregister {
		t.Errorf("type = %q, want %q", msgType, TypeDeregister)
	}
	dereg, ok := msg.(*DeregisterMessage)
	if !ok {
		t.Fatalf("expected *DeregisterMessage, got %T", msg)
	}
	if dereg.ClientID != "laptop" {
		t.Errorf("ClientID = %q, want %q", dereg.ClientID, "laptop")
	}
}

func TestParseHeartbeat(t *testing.T) {
	t.Parallel()

	raw := `{"type":"heartbeat","client_id":"laptop","uptime":3600,"load":{"cpu":12.5,"memory":45.2}}`
	msgType, msg, err := ParseMessage(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if msgType != TypeHeartbeat {
		t.Errorf("type = %q, want %q", msgType, TypeHeartbeat)
	}
	hb, ok := msg.(*HeartbeatMessage)
	if !ok {
		t.Fatalf("expected *HeartbeatMessage, got %T", msg)
	}
	if hb.Uptime != 3600 {
		t.Errorf("Uptime = %d, want 3600", hb.Uptime)
	}
	if hb.Load.CPU != 12.5 {
		t.Errorf("Load.CPU = %f, want 12.5", hb.Load.CPU)
	}
	if hb.Load.Memory != 45.2 {
		t.Errorf("Load.Memory = %f, want 45.2", hb.Load.Memory)
	}
}

func TestParseToolRequest(t *testing.T) {
	t.Parallel()

	raw := `{"type":"tool_request","request_id":"req-123","tool":"shell","arguments":{"command":"ls"}}`
	msgType, msg, err := ParseMessage(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if msgType != TypeToolRequest {
		t.Errorf("type = %q, want %q", msgType, TypeToolRequest)
	}
	req, ok := msg.(*ToolRequestMessage)
	if !ok {
		t.Fatalf("expected *ToolRequestMessage, got %T", msg)
	}
	if req.RequestID != "req-123" {
		t.Errorf("RequestID = %q, want %q", req.RequestID, "req-123")
	}
	if req.Tool != "shell" {
		t.Errorf("Tool = %q, want %q", req.Tool, "shell")
	}
}

func TestParseToolResponse(t *testing.T) {
	t.Parallel()

	raw := `{"type":"tool_response","request_id":"req-123","status":"success","result":"file1.txt\nfile2.txt"}`
	msgType, msg, err := ParseMessage(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if msgType != TypeToolResponse {
		t.Errorf("type = %q, want %q", msgType, TypeToolResponse)
	}
	resp, ok := msg.(*ToolResponseMessage)
	if !ok {
		t.Fatalf("expected *ToolResponseMessage, got %T", msg)
	}
	if resp.Status != "success" {
		t.Errorf("Status = %q, want %q", resp.Status, "success")
	}
	if resp.Result != "file1.txt\nfile2.txt" {
		t.Errorf("Result = %q, want %q", resp.Result, "file1.txt\nfile2.txt")
	}
}

func TestParseUnknownType(t *testing.T) {
	t.Parallel()

	raw := `{"type":"unknown_thing","data":"test"}`
	_, _, err := ParseMessage(raw)
	if err == nil {
		t.Fatal("expected error for unknown type, got nil")
	}
	if !errors.Is(err, ErrUnknownMessageType) {
		t.Errorf("expected ErrUnknownMessageType, got: %v", err)
	}
}

func TestParseInvalidJSON(t *testing.T) {
	t.Parallel()

	raw := `not json at all`
	_, _, err := ParseMessage(raw)
	if err == nil {
		t.Fatal("expected error for invalid JSON, got nil")
	}
	if !errors.Is(err, ErrInvalidJSON) {
		t.Errorf("expected ErrInvalidJSON, got: %v", err)
	}
}

func TestParseEmptyType(t *testing.T) {
	t.Parallel()

	raw := `{"data":"test"}`
	_, _, err := ParseMessage(raw)
	if err == nil {
		t.Fatal("expected error for empty type, got nil")
	}
	if !errors.Is(err, ErrUnknownMessageType) {
		t.Errorf("expected ErrUnknownMessageType, got: %v", err)
	}
}

func TestRoundTrip_Register(t *testing.T) {
	t.Parallel()

	original := &RegisterMessage{
		Type:     TypeRegister,
		ClientID: "vps1",
		Hostname: "nyc-server",
		Tools:    []ToolDef{},
	}

	data, err := MarshalMessage(original)
	if err != nil {
		t.Fatalf("marshal error: %v", err)
	}

	msgType, msg, err := ParseMessage(data)
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	if msgType != TypeRegister {
		t.Errorf("type = %q, want %q", msgType, TypeRegister)
	}

	parsed, ok := msg.(*RegisterMessage)
	if !ok {
		t.Fatalf("expected *RegisterMessage, got %T", msg)
	}
	if parsed.ClientID != original.ClientID {
		t.Errorf("ClientID = %q, want %q", parsed.ClientID, original.ClientID)
	}
	if parsed.Hostname != original.Hostname {
		t.Errorf("Hostname = %q, want %q", parsed.Hostname, original.Hostname)
	}
}

func TestRoundTrip_Heartbeat(t *testing.T) {
	t.Parallel()

	original := &HeartbeatMessage{
		Type:     TypeHeartbeat,
		ClientID: "laptop",
		Uptime:   7200,
		Load:     LoadInfo{CPU: 5.5, Memory: 30.0},
	}

	data, err := MarshalMessage(original)
	if err != nil {
		t.Fatalf("marshal error: %v", err)
	}

	_, msg, err := ParseMessage(data)
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}

	parsed := msg.(*HeartbeatMessage)
	if parsed.Uptime != original.Uptime {
		t.Errorf("Uptime = %d, want %d", parsed.Uptime, original.Uptime)
	}
	if parsed.Load.CPU != original.Load.CPU {
		t.Errorf("Load.CPU = %f, want %f", parsed.Load.CPU, original.Load.CPU)
	}
}

func TestRoundTrip_ToolRequest(t *testing.T) {
	t.Parallel()

	original := &ToolRequestMessage{
		Type:      TypeToolRequest,
		RequestID: "req-abc",
		Tool:      "mail_read",
		Arguments: json.RawMessage(`{"action":"unread","limit":5}`),
	}

	data, err := MarshalMessage(original)
	if err != nil {
		t.Fatalf("marshal error: %v", err)
	}

	_, msg, err := ParseMessage(data)
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}

	parsed := msg.(*ToolRequestMessage)
	if parsed.RequestID != original.RequestID {
		t.Errorf("RequestID = %q, want %q", parsed.RequestID, original.RequestID)
	}
	if parsed.Tool != original.Tool {
		t.Errorf("Tool = %q, want %q", parsed.Tool, original.Tool)
	}
}

func TestRoundTrip_ToolResponse(t *testing.T) {
	t.Parallel()

	original := &ToolResponseMessage{
		Type:      TypeToolResponse,
		RequestID: "req-abc",
		Status:    "success",
		Result:    "3 unread emails",
	}

	data, err := MarshalMessage(original)
	if err != nil {
		t.Fatalf("marshal error: %v", err)
	}

	_, msg, err := ParseMessage(data)
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}

	parsed := msg.(*ToolResponseMessage)
	if parsed.Status != original.Status {
		t.Errorf("Status = %q, want %q", parsed.Status, original.Status)
	}
	if parsed.Result != original.Result {
		t.Errorf("Result = %q, want %q", parsed.Result, original.Result)
	}
}

func TestParseCronResult(t *testing.T) {
	t.Parallel()

	raw := `{"type":"cron_result","client_id":"vps","job_name":"disk","status":"success","exit_code":0,"output":"ok","changed":true,"timestamp":"2026-02-19T08:00:00Z"}`
	msgType, msg, err := ParseMessage(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if msgType != TypeCronResult {
		t.Errorf("type = %q, want %q", msgType, TypeCronResult)
	}
	cr, ok := msg.(*CronResultMessage)
	if !ok {
		t.Fatalf("expected *CronResultMessage, got %T", msg)
	}
	if cr.JobName != "disk" {
		t.Errorf("JobName = %q, want %q", cr.JobName, "disk")
	}
	if !cr.Changed {
		t.Error("Changed = false, want true")
	}
}

func TestParseCronAdd(t *testing.T) {
	t.Parallel()

	raw := `{"type":"cron_add","client_id":"vps","job":{"name":"check","schedule":"* * * * *","command":"df","tool":"shell","notify":true}}`
	msgType, msg, err := ParseMessage(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if msgType != TypeCronAdd {
		t.Errorf("type = %q, want %q", msgType, TypeCronAdd)
	}
	ca, ok := msg.(*CronAddMessage)
	if !ok {
		t.Fatalf("expected *CronAddMessage, got %T", msg)
	}
	if ca.Job.Name != "check" {
		t.Errorf("Job.Name = %q, want %q", ca.Job.Name, "check")
	}
}

func TestParseCronRemove(t *testing.T) {
	t.Parallel()

	raw := `{"type":"cron_remove","client_id":"vps","job_name":"old-job"}`
	msgType, _, err := ParseMessage(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if msgType != TypeCronRemove {
		t.Errorf("type = %q, want %q", msgType, TypeCronRemove)
	}
}

func TestParseCronList(t *testing.T) {
	t.Parallel()

	raw := `{"type":"cron_list","client_id":"vps"}`
	msgType, _, err := ParseMessage(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if msgType != TypeCronList {
		t.Errorf("type = %q, want %q", msgType, TypeCronList)
	}
}

func TestParseCronListResponse(t *testing.T) {
	t.Parallel()

	raw := `{"type":"cron_list_response","client_id":"vps","jobs":[{"name":"disk","schedule":"0 * * * *","last_run":"2026-02-19T07:00:00Z","next_run":"2026-02-19T08:00:00Z","last_status":"success","last_changed":false}]}`
	msgType, msg, err := ParseMessage(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if msgType != TypeCronListResponse {
		t.Errorf("type = %q, want %q", msgType, TypeCronListResponse)
	}
	clr, ok := msg.(*CronListResponseMessage)
	if !ok {
		t.Fatalf("expected *CronListResponseMessage, got %T", msg)
	}
	if len(clr.Jobs) != 1 {
		t.Fatalf("len(Jobs) = %d, want 1", len(clr.Jobs))
	}
	if clr.Jobs[0].Name != "disk" {
		t.Errorf("Jobs[0].Name = %q, want %q", clr.Jobs[0].Name, "disk")
	}
}

func TestParseEvent(t *testing.T) {
	t.Parallel()

	raw := `{"type":"event","client_id":"vps","source":"backup","event_type":"completed","summary":"Daily backup finished","data":"{\"size\":\"1.2GB\"}","event_id":"evt-123","timestamp":"2026-02-20T08:00:00Z"}`
	msgType, msg, err := ParseMessage(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if msgType != TypeEvent {
		t.Errorf("type = %q, want %q", msgType, TypeEvent)
	}
	ev, ok := msg.(*EventMessage)
	if !ok {
		t.Fatalf("expected *EventMessage, got %T", msg)
	}
	if ev.ClientID != "vps" {
		t.Errorf("ClientID = %q, want %q", ev.ClientID, "vps")
	}
	if ev.Source != "backup" {
		t.Errorf("Source = %q, want %q", ev.Source, "backup")
	}
	if ev.EventType != "completed" {
		t.Errorf("EventType = %q, want %q", ev.EventType, "completed")
	}
	if ev.Summary != "Daily backup finished" {
		t.Errorf("Summary = %q, want %q", ev.Summary, "Daily backup finished")
	}
	if ev.EventID != "evt-123" {
		t.Errorf("EventID = %q, want %q", ev.EventID, "evt-123")
	}
	if ev.Timestamp != "2026-02-20T08:00:00Z" {
		t.Errorf("Timestamp = %q, want %q", ev.Timestamp, "2026-02-20T08:00:00Z")
	}
}

func TestRoundTrip_Event(t *testing.T) {
	t.Parallel()

	original := &EventMessage{
		Type:      TypeEvent,
		ClientID:  "laptop",
		Source:    "ci",
		EventType: "deploy_success",
		Summary:   "Deployed v1.2.3 to production",
		Data:      `{"commit":"abc123"}`,
		EventID:   "evt-456",
		Timestamp: "2026-02-20T10:30:00Z",
	}

	data, err := MarshalMessage(original)
	if err != nil {
		t.Fatalf("marshal error: %v", err)
	}

	msgType, msg, err := ParseMessage(data)
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	if msgType != TypeEvent {
		t.Errorf("type = %q, want %q", msgType, TypeEvent)
	}

	parsed, ok := msg.(*EventMessage)
	if !ok {
		t.Fatalf("expected *EventMessage, got %T", msg)
	}
	if parsed.ClientID != original.ClientID {
		t.Errorf("ClientID = %q, want %q", parsed.ClientID, original.ClientID)
	}
	if parsed.Source != original.Source {
		t.Errorf("Source = %q, want %q", parsed.Source, original.Source)
	}
	if parsed.EventType != original.EventType {
		t.Errorf("EventType = %q, want %q", parsed.EventType, original.EventType)
	}
	if parsed.Summary != original.Summary {
		t.Errorf("Summary = %q, want %q", parsed.Summary, original.Summary)
	}
	if parsed.Data != original.Data {
		t.Errorf("Data = %q, want %q", parsed.Data, original.Data)
	}
	if parsed.EventID != original.EventID {
		t.Errorf("EventID = %q, want %q", parsed.EventID, original.EventID)
	}
}

func TestParseEvent_MinimalFields(t *testing.T) {
	t.Parallel()

	raw := `{"type":"event","client_id":"vps","source":"monitor","event_type":"alert","summary":"Disk 90% full"}`
	msgType, msg, err := ParseMessage(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if msgType != TypeEvent {
		t.Errorf("type = %q, want %q", msgType, TypeEvent)
	}
	ev := msg.(*EventMessage)
	if ev.Data != "" {
		t.Errorf("Data = %q, want empty", ev.Data)
	}
	if ev.EventID != "" {
		t.Errorf("EventID = %q, want empty", ev.EventID)
	}
	if ev.Timestamp != "" {
		t.Errorf("Timestamp = %q, want empty", ev.Timestamp)
	}
}

func TestMessageTooLarge_MarshalStillWorks(t *testing.T) {
	t.Parallel()

	// MarshalMessage itself has no size limit — splitting is the Sender's job.
	longResult := strings.Repeat("x", testMaxBusMsg+100)
	msg := &ToolResponseMessage{
		Type:      TypeToolResponse,
		RequestID: "req-123",
		Status:    "success",
		Result:    longResult,
	}

	data, err := MarshalMessage(msg)
	if err != nil {
		t.Fatalf("unexpected marshal error: %v", err)
	}

	if len(data) <= testMaxBusMsg {
		t.Fatalf("expected message to exceed maxBusMessageLen, got %d bytes", len(data))
	}
}

// ---- Multi-part message tests ----

func TestSplitString_Empty(t *testing.T) {
	t.Parallel()
	chunks := splitString("", 10)
	// Empty string produces no chunks (nothing to split).
	if len(chunks) != 0 {
		t.Errorf("splitString(\"\", 10) = %v, want []", chunks)
	}
}

func TestSplitString_FitsInOne(t *testing.T) {
	t.Parallel()
	chunks := splitString("hello", 10)
	if len(chunks) != 1 || chunks[0] != "hello" {
		t.Errorf("splitString(\"hello\", 10) = %v, want [\"hello\"]", chunks)
	}
}

func TestSplitString_ExactFit(t *testing.T) {
	t.Parallel()
	chunks := splitString("hello", 5)
	if len(chunks) != 1 || chunks[0] != "hello" {
		t.Errorf("splitString(\"hello\", 5) = %v, want [\"hello\"]", chunks)
	}
}

func TestSplitString_MultipleChunks(t *testing.T) {
	t.Parallel()
	chunks := splitString("abcdefghij", 3)
	expected := []string{"abc", "def", "ghi", "j"}
	if len(chunks) != len(expected) {
		t.Fatalf("got %d chunks, want %d: %v", len(chunks), len(expected), chunks)
	}
	for i, c := range chunks {
		if c != expected[i] {
			t.Errorf("chunk[%d] = %q, want %q", i, c, expected[i])
		}
	}
}

func TestSplitString_Reassembly(t *testing.T) {
	t.Parallel()
	payload := strings.Repeat("abcde", 100)
	chunks := splitString(payload, 7)
	reassembled := strings.Join(chunks, "")
	if reassembled != payload {
		t.Errorf("reassembled payload does not match original")
	}
}

func TestReceiver_MultiPart_Reassembly(t *testing.T) {
	t.Parallel()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	recv := NewReceiver("", logger)

	var received []string
	recv.On(TypeToolResponse, func(nick string, msg any) {
		resp, ok := msg.(*ToolResponseMessage)
		if !ok {
			t.Errorf("expected *ToolResponseMessage, got %T", msg)
			return
		}
		received = append(received, resp.Result)
	})

	// Build a large payload and split it manually.
	longResult := strings.Repeat("z", 800)
	fullMsg := &ToolResponseMessage{
		Type:      TypeToolResponse,
		RequestID: "req-mp-1",
		Status:    "success",
		Result:    longResult,
	}
	payload, err := MarshalMessage(fullMsg)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	chunks := splitString(payload, testChunkSize)
	mid := "testmid1"
	total := len(chunks)

	for i, chunk := range chunks {
		part := &PartMessage{
			Type:      TypePart,
			PartIndex: i,
			PartTotal: total,
			MessageID: mid,
			Data:      chunk,
		}
		partJSON, err := MarshalMessage(part)
		if err != nil {
			t.Fatalf("marshal part %d: %v", i, err)
		}
		recv.HandleRaw("testclient", partJSON)
	}

	if len(received) != 1 {
		t.Fatalf("expected 1 dispatched message, got %d", len(received))
	}
	if received[0] != longResult {
		t.Errorf("result mismatch: got %d chars, want %d chars", len(received[0]), len(longResult))
	}
}

func TestReceiver_MultiPart_OutOfOrder(t *testing.T) {
	t.Parallel()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	recv := NewReceiver("", logger)

	var received string
	recv.On(TypeToolResponse, func(nick string, msg any) {
		resp := msg.(*ToolResponseMessage)
		received = resp.Result
	})

	// 3 parts, send in reverse order.
	payload := `{"type":"tool_response","request_id":"req-oo","status":"success","result":"hello world"}`
	chunks := splitString(payload, 30)
	if len(chunks) < 2 {
		// Pad to force at least 2 chunks.
		t.Skip("payload too small to split with size 30")
	}
	mid := "testmid2"
	total := len(chunks)

	// Send in reverse order.
	for i := total - 1; i >= 0; i-- {
		part := &PartMessage{
			Type:      TypePart,
			PartIndex: i,
			PartTotal: total,
			MessageID: mid,
			Data:      chunks[i],
		}
		partJSON, _ := MarshalMessage(part)
		recv.HandleRaw("testclient", partJSON)
	}

	if received != "hello world" {
		t.Errorf("result = %q, want %q", received, "hello world")
	}
}

func TestReceiver_MultiPart_Timeout(t *testing.T) {
	t.Parallel()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	recv := NewReceiver("", logger)

	dispatched := 0
	recv.On(TypeToolResponse, func(nick string, msg any) {
		dispatched++
	})

	// Send only the first part of a 3-part message.
	mid := "testmid3"
	part := &PartMessage{
		Type:      TypePart,
		PartIndex: 0,
		PartTotal: 3,
		MessageID: mid,
		Data:      "chunk0",
	}
	partJSON, _ := MarshalMessage(part)
	recv.HandleRaw("testclient", partJSON)

	// Verify buffer exists.
	if recv.partBufferCount() != 1 {
		t.Fatalf("expected 1 part buffer, got %d", recv.partBufferCount())
	}

	// Manually expire the buffer by setting its deadline to the past.
	// Buffer key is "nick:mid".
	bufKey := "testclient:" + mid
	recv.partMu.Lock()
	if buf, ok := recv.partBuf[bufKey]; ok {
		buf.deadline = time.Now().Add(-time.Second)
	}
	recv.partMu.Unlock()

	// Trigger eviction by sending another (unrelated) part.
	part2 := &PartMessage{
		Type:      TypePart,
		PartIndex: 0,
		PartTotal: 1,
		MessageID: "other-mid",
		Data:      `{"type":"tool_response","request_id":"r","status":"success","result":"x"}`,
	}
	part2JSON, _ := MarshalMessage(part2)
	recv.HandleRaw("testclient", part2JSON)

	// The stale buffer should have been evicted.
	recv.partMu.Lock()
	_, stillPresent := recv.partBuf[bufKey]
	recv.partMu.Unlock()

	if stillPresent {
		t.Error("expected stale part buffer to be evicted")
	}
	// The dispatched count should be 1 (from the single-part "other-mid" message).
	if dispatched != 1 {
		t.Errorf("dispatched = %d, want 1", dispatched)
	}
}

func TestSplitString_RuneBoundary(t *testing.T) {
	t.Parallel()
	// Each CJK character is 3 bytes. Splitting at byte boundaries would corrupt them.
	s := "日本語テスト" // 6 runes × 3 bytes = 18 bytes
	chunks := splitString(s, 5)
	reassembled := strings.Join(chunks, "")
	if reassembled != s {
		t.Errorf("splitString corrupted UTF-8: got %q, want %q", reassembled, s)
	}
	// Each chunk must be valid UTF-8.
	for i, c := range chunks {
		if !isValidUTF8(c) {
			t.Errorf("chunk[%d] is not valid UTF-8: %q", i, c)
		}
	}
}

func TestReceiver_MultiPart_Unicode(t *testing.T) {
	t.Parallel()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	recv := NewReceiver("", logger)

	var received string
	recv.On(TypeToolResponse, func(nick string, msg any) {
		resp := msg.(*ToolResponseMessage)
		received = resp.Result
	})

	// Unicode content: emoji and CJK characters.
	unicodeResult := "🎉 日本語テスト 🚀 " + strings.Repeat("αβγδ", 50)
	fullMsg := &ToolResponseMessage{
		Type:      TypeToolResponse,
		RequestID: "req-unicode",
		Status:    "success",
		Result:    unicodeResult,
	}
	payload, err := MarshalMessage(fullMsg)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	chunks := splitString(payload, testChunkSize)
	mid := "unicode-mid"
	total := len(chunks)

	for i, chunk := range chunks {
		part := &PartMessage{
			Type:      TypePart,
			PartIndex: i,
			PartTotal: total,
			MessageID: mid,
			Data:      chunk,
		}
		partJSON, err := MarshalMessage(part)
		if err != nil {
			t.Fatalf("marshal part %d: %v", i, err)
		}
		if len(partJSON) > testMaxBusMsg {
			t.Errorf("part %d/%d is %d bytes, exceeds maxBusMessageLen %d", i, total, len(partJSON), testMaxBusMsg)
		}
		recv.HandleRaw("testclient", partJSON)
	}

	if received != unicodeResult {
		t.Errorf("unicode result mismatch: got %d chars, want %d chars", len([]rune(received)), len([]rune(unicodeResult)))
	}
}

// isValidUTF8 checks if a string is valid UTF-8.
func isValidUTF8(s string) bool {
	return utf8.ValidString(s)
}

func TestReceiver_SinglePart_Unchanged(t *testing.T) {
	t.Parallel()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	recv := NewReceiver("", logger)

	var received string
	recv.On(TypeToolResponse, func(nick string, msg any) {
		resp := msg.(*ToolResponseMessage)
		received = resp.Result
	})

	// A small message that fits in one IRC message — no _part wrapping.
	raw := `{"type":"tool_response","request_id":"req-sp","status":"success","result":"ok"}`
	recv.HandleRaw("testclient", raw)

	if received != "ok" {
		t.Errorf("result = %q, want %q", received, "ok")
	}
}

// ---- Bus Authentication (HMAC-SHA256) tests ----

func TestSignVerify_RoundTrip(t *testing.T) {
	t.Parallel()

	key := []byte("test-secret-key")
	msg := `{"type":"heartbeat","client_id":"laptop","uptime":100}`

	signed, err := signMessage(msg, key)
	if err != nil {
		t.Fatalf("signMessage: %v", err)
	}

	// Signed message should contain a signature field.
	if !strings.Contains(signed, `"signature"`) {
		t.Errorf("signed message missing signature field: %s", signed)
	}

	// Verify should succeed with the same key.
	if err := verifyMessage(signed, key); err != nil {
		t.Fatalf("verifyMessage: %v", err)
	}
}

func TestSignVerify_TamperedMessage(t *testing.T) {
	t.Parallel()

	key := []byte("test-secret-key")
	msg := `{"type":"heartbeat","client_id":"laptop","uptime":100}`

	signed, err := signMessage(msg, key)
	if err != nil {
		t.Fatalf("signMessage: %v", err)
	}

	// Tamper with the message by changing the uptime value.
	tampered := strings.Replace(signed, `"laptop"`, `"hacked"`, 1)

	err = verifyMessage(tampered, key)
	if err == nil {
		t.Fatal("expected error for tampered message, got nil")
	}
	if !errors.Is(err, ErrInvalidSignature) {
		t.Errorf("expected ErrInvalidSignature, got: %v", err)
	}
}

func TestSignVerify_WrongKey(t *testing.T) {
	t.Parallel()

	key1 := []byte("key-one")
	key2 := []byte("key-two")
	msg := `{"type":"heartbeat","client_id":"laptop","uptime":100}`

	signed, err := signMessage(msg, key1)
	if err != nil {
		t.Fatalf("signMessage: %v", err)
	}

	err = verifyMessage(signed, key2)
	if err == nil {
		t.Fatal("expected error for wrong key, got nil")
	}
	if !errors.Is(err, ErrInvalidSignature) {
		t.Errorf("expected ErrInvalidSignature, got: %v", err)
	}
}

func TestSignVerify_MissingSignature(t *testing.T) {
	t.Parallel()

	key := []byte("test-key")
	msg := `{"type":"heartbeat","client_id":"laptop","uptime":100}`

	err := verifyMessage(msg, key)
	if err == nil {
		t.Fatal("expected error for missing signature, got nil")
	}
	if !errors.Is(err, ErrInvalidSignature) {
		t.Errorf("expected ErrInvalidSignature, got: %v", err)
	}
}

func TestReceiver_WithBusKey_AcceptsSignedMessage(t *testing.T) {
	t.Parallel()

	busKey := "shared-secret"
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	recv := NewReceiver(busKey, logger)

	var received string
	recv.On(TypeToolResponse, func(nick string, msg any) {
		resp := msg.(*ToolResponseMessage)
		received = resp.Result
	})

	// Sign a message with the correct key.
	raw := `{"type":"tool_response","request_id":"req-1","status":"success","result":"signed-ok"}`
	signed, err := signMessage(raw, []byte(busKey))
	if err != nil {
		t.Fatalf("signMessage: %v", err)
	}

	recv.HandleRaw("testclient", signed)

	if received != "signed-ok" {
		t.Errorf("result = %q, want %q", received, "signed-ok")
	}
}

func TestReceiver_WithBusKey_RejectsUnsignedMessage(t *testing.T) {
	t.Parallel()

	busKey := "shared-secret"
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	recv := NewReceiver(busKey, logger)

	dispatched := 0
	recv.On(TypeToolResponse, func(nick string, msg any) {
		dispatched++
	})

	// Send an unsigned message — should be rejected.
	raw := `{"type":"tool_response","request_id":"req-1","status":"success","result":"unsigned"}`
	recv.HandleRaw("testclient", raw)

	if dispatched != 0 {
		t.Errorf("expected 0 dispatched (unsigned rejected), got %d", dispatched)
	}
}

func TestReceiver_WithBusKey_RejectsTamperedMessage(t *testing.T) {
	t.Parallel()

	busKey := "shared-secret"
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	recv := NewReceiver(busKey, logger)

	dispatched := 0
	recv.On(TypeToolResponse, func(nick string, msg any) {
		dispatched++
	})

	// Sign a message, then tamper with it.
	raw := `{"type":"tool_response","request_id":"req-1","status":"success","result":"original"}`
	signed, err := signMessage(raw, []byte(busKey))
	if err != nil {
		t.Fatalf("signMessage: %v", err)
	}
	tampered := strings.Replace(signed, `"original"`, `"hacked"`, 1)

	recv.HandleRaw("testclient", tampered)

	if dispatched != 0 {
		t.Errorf("expected 0 dispatched (tampered rejected), got %d", dispatched)
	}
}

func TestReceiver_NoBusKey_AcceptsUnsignedMessage(t *testing.T) {
	t.Parallel()

	// No busKey = backward compatible, accept all messages.
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	recv := NewReceiver("", logger)

	var received string
	recv.On(TypeToolResponse, func(nick string, msg any) {
		resp := msg.(*ToolResponseMessage)
		received = resp.Result
	})

	raw := `{"type":"tool_response","request_id":"req-1","status":"success","result":"no-auth"}`
	recv.HandleRaw("testclient", raw)

	if received != "no-auth" {
		t.Errorf("result = %q, want %q", received, "no-auth")
	}
}

func TestReceiver_WithBusKey_MultiPart_SignedParts(t *testing.T) {
	t.Parallel()

	busKey := "mp-secret"
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	recv := NewReceiver(busKey, logger)

	var received string
	recv.On(TypeToolResponse, func(nick string, msg any) {
		resp := msg.(*ToolResponseMessage)
		received = resp.Result
	})

	// Build a large payload, split, sign each part.
	longResult := strings.Repeat("a", 500)
	fullMsg := &ToolResponseMessage{
		Type:      TypeToolResponse,
		RequestID: "req-mp-signed",
		Status:    "success",
		Result:    longResult,
	}
	payload, err := MarshalMessage(fullMsg)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	chunks := splitString(payload, testChunkSize)
	mid := "signed-mid"
	total := len(chunks)

	for i, chunk := range chunks {
		part := &PartMessage{
			Type:      TypePart,
			PartIndex: i,
			PartTotal: total,
			MessageID: mid,
			Data:      chunk,
		}
		partJSON, err := MarshalMessage(part)
		if err != nil {
			t.Fatalf("marshal part %d: %v", i, err)
		}
		// Sign each part individually.
		signedPart, err := signMessage(partJSON, []byte(busKey))
		if err != nil {
			t.Fatalf("sign part %d: %v", i, err)
		}
		recv.HandleRaw("testclient", signedPart)
	}

	if received != longResult {
		t.Errorf("result mismatch: got %d chars, want %d chars", len(received), len(longResult))
	}
}
