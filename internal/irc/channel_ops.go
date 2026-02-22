package irc

import (
	"fmt"
	"time"
)

// samodeTimeout is the maximum time to wait for the IRC server to process
// a SAMODE command and for girc to update its internal permissions state.
const samodeTimeout = 500 * time.Millisecond

// samodePollInterval is how often EnsureChanOp checks girc's Perms state
// after sending SAMODE.
const samodePollInterval = 50 * time.Millisecond

// validateChannel checks that a channel name is non-empty and starts with '#'.
// The caller parameter is used in error messages to identify the calling method.
func validateChannel(channel, caller string) error {
	if channel == "" {
		return fmt.Errorf("%s: channel name is required", caller)
	}
	if channel[0] != '#' {
		return fmt.Errorf("%s: channel name must start with '#': %q", caller, channel)
	}
	return nil
}

// Join joins an IRC channel. The channel name must start with '#'.
// The actual join is tracked asynchronously via the JOIN handler.
func (c *Connection) Join(channel string) error {
	if err := validateChannel(channel, "join"); err != nil {
		return err
	}
	c.client.Cmd.Join(channel)
	c.logger.Info("requested join", "channel", channel)
	return nil
}

// Part leaves an IRC channel. The channel name must start with '#'.
// The actual part is tracked asynchronously via the PART handler.
func (c *Connection) Part(channel string) error {
	if err := validateChannel(channel, "part"); err != nil {
		return err
	}
	c.client.Cmd.Part(channel)
	c.logger.Info("requested part", "channel", channel)
	return nil
}

// EnsureChanOp ensures the bot has channel operator (+o) status in the given
// channel. If the bot already has +o, this is a no-op. If the bot is a server
// operator (OPER), it uses SAMODE to grant itself +o. Returns an error if the
// bot is not a server operator and does not have channel op.
func (c *Connection) EnsureChanOp(channel string) error {
	if err := validateChannel(channel, "ensureChanOp"); err != nil {
		return err
	}

	nick := c.client.GetNick()
	user := c.client.LookupUser(nick)
	if user != nil {
		perms, ok := user.Perms.Lookup(channel)
		if ok && perms.IsAdmin() {
			return nil // already have op or higher
		}
	}

	// Not a chanop — try SAMODE if we're a server operator.
	if !c.IsOper() {
		return fmt.Errorf("ensureChanOp: not a channel operator in %s and not a server operator", channel)
	}

	if err := c.client.Cmd.SendRaw("SAMODE " + channel + " +o " + nick); err != nil {
		return fmt.Errorf("ensureChanOp: SAMODE failed for %s: %w", channel, err)
	}

	// Poll girc's Perms state until we see +o or the timeout expires.
	deadline := time.Now().Add(samodeTimeout)
	for time.Now().Before(deadline) {
		time.Sleep(samodePollInterval)
		u := c.client.LookupUser(nick)
		if u != nil {
			perms, ok := u.Perms.Lookup(channel)
			if ok && perms.IsAdmin() {
				c.logger.Info("granted self chanop via SAMODE", "channel", channel)
				return nil
			}
		}
	}

	// Timeout — log a warning but don't fail. The SAMODE was sent and the
	// server will process it; subsequent commands in the same TCP stream
	// will be handled after the mode change.
	c.logger.Warn("SAMODE sent but chanop not confirmed within timeout (proceeding anyway)",
		"channel", channel, "timeout", samodeTimeout)
	return nil
}

// SetTopic sets the topic for an IRC channel. If the bot does not have channel
// operator status, it attempts to acquire it via EnsureChanOp first. The
// channel name must start with '#'.
func (c *Connection) SetTopic(channel, topic string) error {
	if err := validateChannel(channel, "setTopic"); err != nil {
		return err
	}
	if err := c.EnsureChanOp(channel); err != nil {
		c.logger.Warn("SetTopic: could not ensure chanop, attempting topic anyway",
			"channel", channel, "error", err)
	}
	c.client.Cmd.Topic(channel, topic)
	c.logger.Info("set topic", "channel", channel, "topic", topic)
	return nil
}

// Kick removes a user from an IRC channel with an optional reason. The bot
// must have channel operator status; EnsureChanOp is called automatically.
func (c *Connection) Kick(channel, user, reason string) error {
	if err := validateChannel(channel, "kick"); err != nil {
		return err
	}
	if user == "" {
		return fmt.Errorf("kick: user is required")
	}
	if err := c.EnsureChanOp(channel); err != nil {
		return fmt.Errorf("kick: %w", err)
	}
	// girc's Kick has a bug: it always sends a no-reason KICK after the
	// with-reason KICK, resulting in duplicate commands. We use SendRaw to
	// avoid this.
	raw := "KICK " + channel + " " + user
	if reason != "" {
		raw += " :" + reason
	}
	if err := c.client.Cmd.SendRaw(raw); err != nil {
		return fmt.Errorf("kick: send failed: %w", err)
	}
	c.logger.Info("kicked user", "channel", channel, "user", user, "reason", reason)
	return nil
}

// Ban sets +b on a hostmask in an IRC channel. The bot must have channel
// operator status; EnsureChanOp is called automatically.
func (c *Connection) Ban(channel, mask string) error {
	if err := validateChannel(channel, "ban"); err != nil {
		return err
	}
	if mask == "" {
		return fmt.Errorf("ban: mask is required")
	}
	if err := c.EnsureChanOp(channel); err != nil {
		return fmt.Errorf("ban: %w", err)
	}
	c.client.Cmd.Ban(channel, mask)
	c.logger.Info("banned mask", "channel", channel, "mask", mask)
	return nil
}

// Unban removes +b on a hostmask in an IRC channel. The bot must have channel
// operator status; EnsureChanOp is called automatically.
func (c *Connection) Unban(channel, mask string) error {
	if err := validateChannel(channel, "unban"); err != nil {
		return err
	}
	if mask == "" {
		return fmt.Errorf("unban: mask is required")
	}
	if err := c.EnsureChanOp(channel); err != nil {
		return fmt.Errorf("unban: %w", err)
	}
	c.client.Cmd.Unban(channel, mask)
	c.logger.Info("unbanned mask", "channel", channel, "mask", mask)
	return nil
}

// SetMode sets a channel mode. The bot must have channel operator status;
// EnsureChanOp is called automatically. The mode string should be like "+o",
// "-v", "+b", etc. Params are the mode parameters (e.g., nick for +o).
func (c *Connection) SetMode(channel, mode string, params ...string) error {
	if err := validateChannel(channel, "setMode"); err != nil {
		return err
	}
	if mode == "" {
		return fmt.Errorf("setMode: mode is required")
	}
	if err := c.EnsureChanOp(channel); err != nil {
		return fmt.Errorf("setMode: %w", err)
	}
	c.client.Cmd.Mode(channel, mode, params...)
	c.logger.Info("set mode", "channel", channel, "mode", mode, "params", params)
	return nil
}
