package server

import (
	"log/slog"
	"sync"
	"time"
)

// Default flood protection settings.
const (
	// floodMaxPerWindow is the maximum number of messages a single nick can
	// send within floodWindow before being rate-limited.
	floodMaxPerWindow = 3

	// floodWindow is the sliding window duration for per-nick rate limiting.
	floodWindow = 10 * time.Second

	// floodCooldown is how long a nick is silenced after exceeding the rate
	// limit. During cooldown, all messages from the nick are dropped silently.
	floodCooldown = 30 * time.Second

	// channelQueueSize is the maximum number of pending messages per channel.
	// When the queue is full, new messages are dropped. This prevents
	// unbounded goroutine accumulation during floods.
	channelQueueSize = 5
)

// pendingMsg is a message waiting to be processed by the agent loop.
type pendingMsg struct {
	channel string
	nick    string
	message string
}

// floodGuard provides per-nick rate limiting and per-channel bounded message
// queuing. It replaces the previous unbounded goroutine-per-message approach
// with a single worker goroutine per channel that reads from a buffered channel.
type floodGuard struct {
	mu       sync.Mutex
	nicks    map[string]*nickState // per-nick rate state
	channels map[string]chan pendingMsg
	logger   *slog.Logger
}

// nickState tracks message timestamps and cooldown for a single nick.
type nickState struct {
	timestamps []time.Time // message timestamps within the sliding window
	cooldownAt time.Time   // if set, nick is silenced until this time
}

// newFloodGuard creates a new flood guard.
func newFloodGuard(logger *slog.Logger) *floodGuard {
	return &floodGuard{
		nicks:    make(map[string]*nickState),
		channels: make(map[string]chan pendingMsg),
		logger:   logger,
	}
}

// allow checks whether a message from nick should be processed. Returns false
// if the nick is rate-limited or in cooldown. Thread-safe.
func (fg *floodGuard) allow(nick string) bool {
	fg.mu.Lock()
	defer fg.mu.Unlock()

	now := time.Now()
	ns, ok := fg.nicks[nick]
	if !ok {
		ns = &nickState{}
		fg.nicks[nick] = ns
	}

	// Check cooldown.
	if now.Before(ns.cooldownAt) {
		return false
	}

	// Prune timestamps outside the window.
	cutoff := now.Add(-floodWindow)
	valid := ns.timestamps[:0]
	for _, ts := range ns.timestamps {
		if ts.After(cutoff) {
			valid = append(valid, ts)
		}
	}
	ns.timestamps = valid

	// Check rate.
	if len(ns.timestamps) >= floodMaxPerWindow {
		ns.cooldownAt = now.Add(floodCooldown)
		ns.timestamps = nil // reset for next window after cooldown
		fg.logger.Warn("flood detected, nick rate-limited",
			"nick", nick,
			"cooldown", floodCooldown,
		)
		return false
	}

	ns.timestamps = append(ns.timestamps, now)
	return true
}

// enqueue adds a message to the channel's bounded queue. Returns false if the
// queue is full (message dropped). The handler function is called by the
// channel's worker goroutine to process each message.
func (fg *floodGuard) enqueue(msg pendingMsg, handler func(pendingMsg)) bool {
	ch := fg.getOrCreateChannel(msg.channel, handler)

	select {
	case ch <- msg:
		return true
	default:
		fg.logger.Warn("channel queue full, message dropped",
			"channel", msg.channel,
			"nick", msg.nick,
		)
		return false
	}
}

// flush drains all pending messages from a channel's queue without processing
// them. Returns the number of messages drained.
func (fg *floodGuard) flush(channel string) int {
	fg.mu.Lock()
	ch, ok := fg.channels[channel]
	fg.mu.Unlock()

	if !ok {
		return 0
	}

	count := 0
	for {
		select {
		case <-ch:
			count++
		default:
			return count
		}
	}
}

// getOrCreateChannel returns the buffered channel for a given IRC channel,
// creating it and starting a worker goroutine if it doesn't exist yet.
func (fg *floodGuard) getOrCreateChannel(channel string, handler func(pendingMsg)) chan pendingMsg {
	fg.mu.Lock()
	defer fg.mu.Unlock()

	ch, ok := fg.channels[channel]
	if ok {
		return ch
	}

	ch = make(chan pendingMsg, channelQueueSize)
	fg.channels[channel] = ch

	// Start a single worker goroutine for this channel. It processes
	// messages sequentially, which naturally serializes agent loops
	// without needing the per-channel mutex in the Agent.
	go func() {
		for msg := range ch {
			handler(msg)
		}
	}()

	return ch
}
