package irc

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/lrstanley/girc"
)

// IRC numeric reply constants for WHOIS responses.
const (
	rplWhoisAccount = "330" // RPL_WHOISACCOUNT — logged-in account name
	rplEndOfWhois   = "318" // RPL_ENDOFWHOIS — end of WHOIS reply
)

// whoisTimeout is the maximum time to wait for a WHOIS response.
const whoisTimeout = 5 * time.Second

// WhoisResult holds the result of a WHOIS query for a single nick.
type WhoisResult struct {
	// Nick is the queried nick.
	Nick string
	// Account is the NickServ/SASL account name the user is logged in as.
	// Empty if the user is not identified.
	Account string
}

// Whois sends a WHOIS query for the given nick and blocks until the server
// responds or the timeout expires. It returns the account name the user is
// logged in as (via RPL_WHOISACCOUNT / 330). If the user is not identified,
// Account will be empty. Returns an error on timeout or if not connected.
func (c *Connection) Whois(nick string) (*WhoisResult, error) {
	if !c.client.IsConnected() {
		return nil, fmt.Errorf("whois: not connected")
	}

	var mu sync.Mutex
	result := &WhoisResult{Nick: nick}
	// Buffered so the 318 handler never blocks even if it fires before
	// the caller enters the select.
	done := make(chan struct{}, 1)

	// Register temporary handlers for this WHOIS exchange. Both handlers
	// run in background goroutines (AddBg), so we protect result.Account
	// with a mutex to prevent a data race between the 330 write and the
	// 318 signal.
	//
	// RPL_WHOISACCOUNT (330): params = [our_nick, target_nick, account_name, "is logged in as"]
	accountCUID := c.client.Handlers.AddBg(rplWhoisAccount, func(_ *girc.Client, e girc.Event) {
		if len(e.Params) >= 3 && strings.EqualFold(e.Params[1], nick) {
			mu.Lock()
			result.Account = e.Params[2]
			mu.Unlock()
		}
	})

	// RPL_ENDOFWHOIS (318): params = [our_nick, target_nick, "End of /WHOIS list"]
	// The IRC server always sends 330 before 318, so Account is set by the
	// time we receive this. We acquire the mutex to ensure the 330 handler's
	// write is visible before signalling completion.
	endCUID := c.client.Handlers.AddBg(rplEndOfWhois, func(_ *girc.Client, e girc.Event) {
		if len(e.Params) >= 2 && strings.EqualFold(e.Params[1], nick) {
			mu.Lock()
			// Read Account under lock to establish happens-before with the
			// 330 handler's write. The value itself is not used here — the
			// lock/unlock pair acts as a memory barrier.
			_ = result.Account //nolint:staticcheck // intentional memory barrier
			mu.Unlock()
			select {
			case done <- struct{}{}:
			default:
			}
		}
	})

	defer func() {
		c.client.Handlers.Remove(accountCUID)
		c.client.Handlers.Remove(endCUID)
	}()

	// Send the WHOIS command.
	c.client.Cmd.Whois(nick)

	// Wait for the response or timeout.
	select {
	case <-done:
		mu.Lock()
		defer mu.Unlock()
		return result, nil
	case <-time.After(whoisTimeout):
		mu.Lock()
		defer mu.Unlock()
		return result, fmt.Errorf("whois: timeout waiting for response for %q", nick)
	}
}
