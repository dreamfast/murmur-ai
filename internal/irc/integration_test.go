//go:build integration

package irc

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"murmur/internal/config"
)

// Integration tests for the IRC package. These tests require a running Ergo
// IRC server and are gated behind the "integration" build tag.
//
// Run with:
//   MURMUR_IRC_TEST_SERVER=localhost:6667 go test -tags integration -v ./internal/irc/
//
// The tests expect:
//   - An Ergo IRC server on the specified host:port (plaintext)
//   - NickServ account registration enabled
//   - No server password required (or set MURMUR_IRC_TEST_PASSWORD)

// testIRCConfig returns an IRCConfig for integration tests, reading the server
// address from the MURMUR_IRC_TEST_SERVER environment variable.
func testIRCConfig(t *testing.T, nick string) config.IRCConfig {
	t.Helper()

	server := os.Getenv("MURMUR_IRC_TEST_SERVER")
	if server == "" {
		t.Skip("MURMUR_IRC_TEST_SERVER not set, skipping integration test")
	}

	host := server
	port := 6667
	if parts := strings.SplitN(server, ":", 2); len(parts) == 2 {
		host = parts[0]
		if p, err := strconv.Atoi(parts[1]); err == nil {
			port = p
		}
	}

	return config.IRCConfig{
		Server:   host,
		Port:     port,
		Nick:     nick,
		User:     nick,
		Realname: "integration test",
		Password: os.Getenv("MURMUR_IRC_TEST_PASSWORD"),
	}
}

// testLogger returns a logger that writes to stderr for integration test visibility.
func testIntegrationLogger(t *testing.T) *slog.Logger {
	t.Helper()
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))
}

// waitForConnect creates a connection, starts it in a goroutine, and waits
// for the CONNECTED event. Returns the connection and a cancel function.
func waitForConnect(t *testing.T, cfg config.IRCConfig, channels []string, logger *slog.Logger) (*Connection, context.CancelFunc) {
	t.Helper()

	conn, err := NewConnection(cfg, channels, logger)
	if err != nil {
		t.Fatalf("NewConnection: %v", err)
	}

	connected := make(chan struct{}, 1)
	conn.OnConnect(func() {
		select {
		case connected <- struct{}{}:
		default:
		}
	})

	ctx, cancel := context.WithCancel(context.Background())

	go func() {
		if err := conn.Connect(ctx); err != nil && ctx.Err() == nil {
			t.Errorf("Connect error: %v", err)
		}
	}()

	select {
	case <-connected:
		// Give the server a moment to process the connection fully.
		time.Sleep(100 * time.Millisecond)
	case <-time.After(10 * time.Second):
		cancel()
		t.Fatal("timed out waiting for IRC connection")
	}

	return conn, cancel
}

func TestIntegration_ConnectAndJoinChannel(t *testing.T) {
	logger := testIntegrationLogger(t)
	cfg := testIRCConfig(t, "testbot1")

	conn, cancel := waitForConnect(t, cfg, []string{"#integration-test"}, logger)
	defer cancel()
	defer conn.Close()

	// Verify we're connected.
	if !conn.IsConnected() {
		t.Fatal("expected IsConnected() = true after connect")
	}

	// Wait for channel join to be tracked.
	time.Sleep(200 * time.Millisecond)

	channels := conn.Channels()
	found := false
	for _, ch := range channels {
		if ch == "#integration-test" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected to be in #integration-test, joined channels: %v", channels)
	}
}

func TestIntegration_SendAndReceiveMessage(t *testing.T) {
	logger := testIntegrationLogger(t)

	// Create two connections: sender and receiver.
	senderCfg := testIRCConfig(t, "sender1")
	receiverCfg := testIRCConfig(t, "receiver1")

	sender, senderCancel := waitForConnect(t, senderCfg, []string{"#msg-test"}, logger)
	defer senderCancel()
	defer sender.Close()

	receiver, receiverCancel := waitForConnect(t, receiverCfg, []string{"#msg-test"}, logger)
	defer receiverCancel()
	defer receiver.Close()

	// Wait for both to join.
	time.Sleep(500 * time.Millisecond)

	// Set up message listener on receiver.
	var mu sync.Mutex
	var received []string
	receiver.OnMessage(func(channel, nick, message string) {
		if nick == "sender1" && channel == "#msg-test" {
			mu.Lock()
			received = append(received, message)
			mu.Unlock()
		}
	})

	// Send a message.
	testMsg := fmt.Sprintf("hello from integration test %d", time.Now().UnixNano())
	sender.Send("#msg-test", testMsg)

	// Wait for delivery.
	deadline := time.After(5 * time.Second)
	for {
		mu.Lock()
		got := len(received)
		mu.Unlock()
		if got > 0 {
			break
		}
		select {
		case <-deadline:
			t.Fatal("timed out waiting for message delivery")
		case <-time.After(50 * time.Millisecond):
		}
	}

	mu.Lock()
	defer mu.Unlock()
	if len(received) == 0 || received[0] != testMsg {
		t.Errorf("expected message %q, got %v", testMsg, received)
	}
}

func TestIntegration_WhoisIdentified(t *testing.T) {
	logger := testIntegrationLogger(t)

	// Create a user, register with NickServ, then WHOIS them.
	userCfg := testIRCConfig(t, "whoisuser")
	user, userCancel := waitForConnect(t, userCfg, nil, logger)
	defer userCancel()
	defer user.Close()

	// Register with NickServ (Ergo allows immediate registration).
	user.client.Cmd.Message("NickServ", "REGISTER testpass123 test@example.com")
	time.Sleep(1 * time.Second)

	// Create a second connection to perform the WHOIS.
	checkerCfg := testIRCConfig(t, "checker1")
	checker, checkerCancel := waitForConnect(t, checkerCfg, nil, logger)
	defer checkerCancel()
	defer checker.Close()

	result, err := checker.Whois("whoisuser")
	if err != nil {
		t.Fatalf("Whois error: %v", err)
	}

	// The user should be identified (registered + auto-logged-in on Ergo).
	if result.Account == "" {
		t.Error("expected whoisuser to have a NickServ account, got empty")
	}
}

func TestIntegration_WhoisNotIdentified(t *testing.T) {
	logger := testIntegrationLogger(t)

	// Create an unregistered user.
	userCfg := testIRCConfig(t, "anonuser")
	user, userCancel := waitForConnect(t, userCfg, nil, logger)
	defer userCancel()
	defer user.Close()

	// Create a checker.
	checkerCfg := testIRCConfig(t, "checker2")
	checker, checkerCancel := waitForConnect(t, checkerCfg, nil, logger)
	defer checkerCancel()
	defer checker.Close()

	result, err := checker.Whois("anonuser")
	if err != nil {
		t.Fatalf("Whois error: %v", err)
	}

	if result.Account != "" {
		t.Errorf("expected anonuser to have no account, got %q", result.Account)
	}
}

func TestIntegration_OnQuitCallback(t *testing.T) {
	logger := testIntegrationLogger(t)

	// Observer stays connected and watches for QUIT events.
	observerCfg := testIRCConfig(t, "observer1")
	observer, observerCancel := waitForConnect(t, observerCfg, []string{"#quit-test"}, logger)
	defer observerCancel()
	defer observer.Close()

	// Quitter joins the same channel so the observer sees the QUIT.
	quitterCfg := testIRCConfig(t, "quitter1")
	quitter, quitterCancel := waitForConnect(t, quitterCfg, []string{"#quit-test"}, logger)

	// Wait for both to join.
	time.Sleep(500 * time.Millisecond)

	// Set up QUIT listener.
	quitCh := make(chan string, 1)
	observer.OnQuit(func(nick string) {
		if strings.EqualFold(nick, "quitter1") {
			select {
			case quitCh <- nick:
			default:
			}
		}
	})

	// Disconnect the quitter.
	quitterCancel()
	quitter.Close()

	select {
	case nick := <-quitCh:
		if !strings.EqualFold(nick, "quitter1") {
			t.Errorf("expected quit nick 'quitter1', got %q", nick)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for QUIT event")
	}
}

func TestIntegration_OnNickCallback(t *testing.T) {
	logger := testIntegrationLogger(t)

	// Observer watches for NICK changes.
	observerCfg := testIRCConfig(t, "observer2")
	observer, observerCancel := waitForConnect(t, observerCfg, []string{"#nick-test"}, logger)
	defer observerCancel()
	defer observer.Close()

	// Changer joins the same channel.
	changerCfg := testIRCConfig(t, "changer1")
	changer, changerCancel := waitForConnect(t, changerCfg, []string{"#nick-test"}, logger)
	defer changerCancel()
	defer changer.Close()

	// Wait for both to join.
	time.Sleep(500 * time.Millisecond)

	// Set up NICK listener.
	nickCh := make(chan [2]string, 1)
	observer.OnNick(func(oldNick, newNick string) {
		if strings.EqualFold(oldNick, "changer1") {
			select {
			case nickCh <- [2]string{oldNick, newNick}:
			default:
			}
		}
	})

	// Change nick.
	changer.client.Cmd.Nick("changer1_new")

	select {
	case pair := <-nickCh:
		if !strings.EqualFold(pair[0], "changer1") {
			t.Errorf("expected old nick 'changer1', got %q", pair[0])
		}
		if !strings.EqualFold(pair[1], "changer1_new") {
			t.Errorf("expected new nick 'changer1_new', got %q", pair[1])
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for NICK event")
	}
}

func TestIntegration_PartAndRejoin(t *testing.T) {
	logger := testIntegrationLogger(t)
	cfg := testIRCConfig(t, "partbot1")

	conn, cancel := waitForConnect(t, cfg, []string{"#part-test"}, logger)
	defer cancel()
	defer conn.Close()

	// Wait for join.
	time.Sleep(300 * time.Millisecond)

	// Verify we're in the channel.
	channels := conn.Channels()
	found := false
	for _, ch := range channels {
		if ch == "#part-test" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected to be in #part-test, channels: %v", channels)
	}

	// Part the channel.
	if err := conn.Part("#part-test"); err != nil {
		t.Fatalf("Part error: %v", err)
	}
	time.Sleep(300 * time.Millisecond)

	channels = conn.Channels()
	for _, ch := range channels {
		if ch == "#part-test" {
			t.Error("expected to NOT be in #part-test after PART")
		}
	}

	// Rejoin.
	if err := conn.Join("#part-test"); err != nil {
		t.Fatalf("Join error: %v", err)
	}
	time.Sleep(300 * time.Millisecond)

	channels = conn.Channels()
	found = false
	for _, ch := range channels {
		if ch == "#part-test" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected to be in #part-test after rejoin, channels: %v", channels)
	}
}

func TestIntegration_DirectMessage(t *testing.T) {
	logger := testIntegrationLogger(t)

	// Two users, no shared channel — test DM delivery.
	senderCfg := testIRCConfig(t, "dmsender")
	receiverCfg := testIRCConfig(t, "dmrecv")

	sender, senderCancel := waitForConnect(t, senderCfg, nil, logger)
	defer senderCancel()
	defer sender.Close()

	receiver, receiverCancel := waitForConnect(t, receiverCfg, nil, logger)
	defer receiverCancel()
	defer receiver.Close()

	time.Sleep(300 * time.Millisecond)

	// Listen for DMs on receiver.
	dmCh := make(chan string, 1)
	receiver.OnMessage(func(channel, nick, message string) {
		// DMs arrive with channel = receiver's nick.
		if strings.EqualFold(nick, "dmsender") {
			select {
			case dmCh <- message:
			default:
			}
		}
	})

	testMsg := "hello via DM"
	sender.Send("dmrecv", testMsg)

	select {
	case msg := <-dmCh:
		if msg != testMsg {
			t.Errorf("expected DM %q, got %q", testMsg, msg)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for DM delivery")
	}
}
