package irc

import (
	"strings"
	"sync"
)

// MessageHandler routes incoming IRC messages to appropriate handlers based
// on the channel they arrive on. Bus messages go to the bus handler, and
// user messages go to the user handler.
type MessageHandler struct {
	conn       *Connection
	busChannel string

	mu          sync.RWMutex
	busHandler  func(nick, raw string)
	userHandler func(channel, nick, message string)
}

// NewMessageHandler creates a new message handler that routes messages from
// the given connection based on channel.
func NewMessageHandler(conn *Connection, busChannel string) *MessageHandler {
	h := &MessageHandler{
		conn:       conn,
		busChannel: busChannel,
	}

	conn.OnMessage(h.route)

	return h
}

// RegisterBusHandler sets the handler for messages received on the bus channel.
// The handler receives the sender's nick and the raw message text (JSON).
// This method is safe for concurrent use.
func (h *MessageHandler) RegisterBusHandler(handler func(nick, raw string)) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.busHandler = handler
}

// RegisterUserHandler sets the handler for messages received on user-facing
// channels (anything that is not the bus channel).
// This method is safe for concurrent use.
func (h *MessageHandler) RegisterUserHandler(handler func(channel, nick, message string)) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.userHandler = handler
}

// route dispatches an incoming message to the appropriate handler.
func (h *MessageHandler) route(channel, nick, message string) {
	// Ignore messages from ourselves (case-insensitive per IRC convention).
	if strings.EqualFold(nick, h.conn.Nick()) {
		return
	}

	h.mu.RLock()
	busHandler := h.busHandler
	userHandler := h.userHandler
	h.mu.RUnlock()

	if channel == h.busChannel {
		if busHandler != nil {
			busHandler(nick, message)
		}
		return
	}

	if userHandler != nil {
		userHandler(channel, nick, message)
	}
}
