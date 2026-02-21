package dashboard

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/lrstanley/girc"
)

// WSMessage is a JSON message sent between the browser and the server
// over the WebSocket connection.
type WSMessage struct {
	// Type identifies the message kind.
	Type string `json:"type"`
	// Channel is the IRC channel this message relates to (if applicable).
	Channel string `json:"channel,omitempty"`
	// Nick is the sender's IRC nick (server→client only).
	Nick string `json:"nick,omitempty"`
	// Text is the message content.
	Text string `json:"text,omitempty"`
	// Topic is the channel topic (for topic events).
	Topic string `json:"topic,omitempty"`
	// Users is a list of nicks (for names events).
	Users []string `json:"users,omitempty"`
	// Mode is the mode string (for mode events).
	Mode string `json:"mode,omitempty"`
	// Error is an error message (for error events).
	Error string `json:"error,omitempty"`
	// Channels is the list of joined channels (for connected events).
	Channels []string `json:"channels,omitempty"`
}

// Bridge connects a single WebSocket client to an IRC connection.
// It relays messages bidirectionally: IRC events are forwarded to the
// WebSocket as JSON, and WebSocket commands are sent to IRC.
type Bridge struct {
	ws     *websocket.Conn
	irc    *girc.Client
	nick   string
	logger *slog.Logger

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	// writeMu serializes WebSocket writes since coder/websocket
	// does not allow concurrent writers.
	writeMu sync.Mutex
}

// BridgeConfig holds the parameters needed to create a new Bridge.
type BridgeConfig struct {
	// Nick is the IRC nick for this bridge's connection.
	Nick string
	// Password is the NickServ password for authentication.
	Password string
	// IRCServer is the IRC server address (host:port).
	IRCServer string
	// IRCPort is the IRC server port.
	IRCPort int
	// IRCTLS enables TLS for the IRC connection.
	IRCTLS bool
	// ServerPassword is the IRC server password (PASS command).
	ServerPassword string
	// Channels is the list of channels to auto-join.
	Channels []string
}

// NewBridge creates a Bridge that connects a WebSocket to a new IRC session.
// The IRC connection is established immediately. Call Run() to start the
// message relay loop.
func NewBridge(ctx context.Context, ws *websocket.Conn, cfg BridgeConfig, logger *slog.Logger) (*Bridge, error) {
	bridgeCtx, cancel := context.WithCancel(ctx)

	ircCfg := girc.Config{
		Server:    cfg.IRCServer,
		Port:      cfg.IRCPort,
		Nick:      cfg.Nick,
		User:      cfg.Nick,
		Name:      "Murmur Dashboard",
		SSL:       cfg.IRCTLS,
		TLSConfig: nil,
	}
	if cfg.ServerPassword != "" {
		ircCfg.ServerPass = cfg.ServerPassword
	}
	// Use SASL PLAIN for authentication instead of post-connect NickServ
	// IDENTIFY. SASL happens during CAP negotiation before CONNECTED fires,
	// so the user is already identified when channels are joined.
	if cfg.Password != "" {
		ircCfg.SASL = &girc.SASLPlain{User: cfg.Nick, Pass: cfg.Password}
		if !cfg.IRCTLS {
			logger.Warn("SASL PLAIN enabled without TLS — credentials visible on network")
		}
	}

	client := girc.New(ircCfg)

	b := &Bridge{
		ws:     ws,
		irc:    client,
		nick:   cfg.Nick,
		logger: logger.With("component", "bridge", "nick", cfg.Nick),
		ctx:    bridgeCtx,
		cancel: cancel,
	}

	// Register IRC event handlers.
	client.Handlers.AddBg(girc.CONNECTED, func(_ *girc.Client, _ girc.Event) {
		// Authentication is handled via SASL during CAP negotiation,
		// so the user is already identified when this fires.
		for _, ch := range cfg.Channels {
			client.Cmd.Join(ch)
		}
		b.sendWS(WSMessage{Type: "connected", Nick: cfg.Nick, Channels: cfg.Channels})
	})

	client.Handlers.AddBg(girc.PRIVMSG, func(_ *girc.Client, e girc.Event) {
		if len(e.Params) < 1 {
			return
		}
		b.sendWS(WSMessage{
			Type:    "message",
			Channel: e.Params[0],
			Nick:    e.Source.Name,
			Text:    e.Last(),
		})
	})

	client.Handlers.AddBg(girc.JOIN, func(_ *girc.Client, e girc.Event) {
		if len(e.Params) < 1 {
			return
		}
		b.sendWS(WSMessage{
			Type:    "join",
			Channel: e.Params[0],
			Nick:    e.Source.Name,
		})
	})

	client.Handlers.AddBg(girc.PART, func(_ *girc.Client, e girc.Event) {
		if len(e.Params) < 1 {
			return
		}
		b.sendWS(WSMessage{
			Type:    "part",
			Channel: e.Params[0],
			Nick:    e.Source.Name,
			Text:    e.Last(),
		})
	})

	client.Handlers.AddBg(girc.TOPIC, func(_ *girc.Client, e girc.Event) {
		if len(e.Params) < 1 {
			return
		}
		b.sendWS(WSMessage{
			Type:    "topic",
			Channel: e.Params[0],
			Nick:    e.Source.Name,
			Topic:   e.Last(),
		})
	})

	client.Handlers.AddBg(girc.MODE, func(_ *girc.Client, e girc.Event) {
		if len(e.Params) < 2 {
			return
		}
		b.sendWS(WSMessage{
			Type:    "mode",
			Channel: e.Params[0],
			Nick:    e.Source.Name,
			Mode:    e.Params[1],
		})
	})

	client.Handlers.AddBg(girc.RPL_NAMREPLY, func(_ *girc.Client, e girc.Event) {
		// RPL_NAMREPLY: <nick> = <channel> :<names>
		if len(e.Params) >= 3 {
			channel := e.Params[2]
			lookup := client.LookupChannel(channel)
			if lookup != nil {
				b.sendWS(WSMessage{
					Type:    "names",
					Channel: channel,
					Users:   lookup.UserList,
				})
			}
		}
	})

	return b, nil
}

// Run starts the IRC connection and WebSocket read loop. It blocks until
// the bridge is closed or an error occurs.
func (b *Bridge) Run() {
	// Start IRC connection in background.
	b.wg.Add(1)
	go func() {
		defer b.wg.Done()
		if err := b.irc.Connect(); err != nil {
			b.logger.Error("IRC connection error", "error", err)
			b.sendWS(WSMessage{Type: "error", Error: "IRC connection failed: " + err.Error()})
		}
		b.cancel()
	}()

	// Read WebSocket messages and dispatch to IRC.
	b.wg.Add(1)
	go func() {
		defer b.wg.Done()
		b.readLoop()
	}()

	// Wait for context cancellation, then clean up.
	<-b.ctx.Done()
	b.irc.Close()
	b.ws.Close(websocket.StatusNormalClosure, "session ended")
	b.wg.Wait()
}

// Close terminates the bridge, disconnecting from IRC and closing the
// WebSocket.
func (b *Bridge) Close() {
	b.cancel()
}

// readLoop reads JSON messages from the WebSocket and dispatches them
// to the IRC connection.
func (b *Bridge) readLoop() {
	for {
		_, data, err := b.ws.Read(b.ctx)
		if err != nil {
			if b.ctx.Err() == nil {
				b.logger.Debug("WebSocket read error", "error", err)
			}
			b.cancel()
			return
		}

		var msg WSMessage
		if err := json.Unmarshal(data, &msg); err != nil {
			b.logger.Debug("invalid WebSocket message", "error", err)
			continue
		}

		switch msg.Type {
		case "message":
			if msg.Channel != "" && msg.Text != "" {
				b.irc.Cmd.Message(msg.Channel, msg.Text)
			}
		case "join":
			if msg.Channel != "" {
				b.irc.Cmd.Join(msg.Channel)
			}
		case "part":
			if msg.Channel != "" {
				b.irc.Cmd.Part(msg.Channel)
			}
		default:
			b.logger.Debug("unknown WebSocket message type", "type", msg.Type)
		}
	}
}

// sendWS marshals a message to JSON and writes it to the WebSocket.
// Errors are logged but not returned since IRC events are fire-and-forget.
func (b *Bridge) sendWS(msg WSMessage) {
	data, err := json.Marshal(msg)
	if err != nil {
		b.logger.Error("failed to marshal WebSocket message", "error", err)
		return
	}

	b.writeMu.Lock()
	defer b.writeMu.Unlock()

	ctx, cancel := context.WithTimeout(b.ctx, 5*time.Second)
	defer cancel()

	if err := b.ws.Write(ctx, websocket.MessageText, data); err != nil {
		if b.ctx.Err() == nil {
			b.logger.Debug("WebSocket write error", "error", err)
		}
	}
}
