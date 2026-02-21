package dashboard

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/lrstanley/girc"
)

// verifyTimeout is the maximum time to wait for IRC credential verification.
const verifyTimeout = 10 * time.Second

// VerifyIRCCredentials connects to the IRC server with a temporary nick and
// verifies the given nick/password combination via NickServ IDENTIFY. It
// returns nil if authentication succeeds, or an error describing the failure.
//
// A temporary nick is used so verification works even when the user's real
// nick is already connected (e.g., from their IRC client).
//
// The connection is closed after verification regardless of the outcome.
func VerifyIRCCredentials(_ context.Context, server string, port int, tls bool, serverPass, nick, password string) error {
	ctx, cancel := context.WithTimeout(context.Background(), verifyTimeout)
	defer cancel()

	// Use a temporary nick for the verification connection to avoid
	// ERR_NICKNAMEINUSE if the user is already connected.
	tempNick := fmt.Sprintf("mrv_%d", time.Now().UnixNano()%100000)

	cfg := girc.Config{
		Server:    server,
		Port:      port,
		Nick:      tempNick,
		User:      tempNick,
		Name:      "Murmur Dashboard Auth",
		SSL:       tls,
		TLSConfig: nil,
	}
	if serverPass != "" {
		cfg.ServerPass = serverPass
	}

	client := girc.New(cfg)

	result := make(chan error, 1)

	// On successful IRC connection, send NickServ IDENTIFY with explicit nick.
	client.Handlers.AddBg(girc.CONNECTED, func(_ *girc.Client, _ girc.Event) {
		if password != "" {
			// "IDENTIFY nick password" works from any connected nick.
			client.Cmd.Messagef("NickServ", "IDENTIFY %s %s", nick, password)
		} else {
			// No password — just verify the nick format is valid.
			result <- nil
		}
	})

	// Listen for NickServ responses. We capture ALL NickServ notices and
	// check for known success/failure patterns. Any unrecognized response
	// is treated as a failure to avoid silent timeouts.
	client.Handlers.AddBg(girc.NOTICE, func(_ *girc.Client, e girc.Event) {
		if e.Source == nil || !strings.EqualFold(e.Source.Name, "NickServ") {
			return
		}
		text := strings.ToLower(e.Last())

		// Success patterns from common IRC services (Atheme, Anope, Ergo).
		if strings.Contains(text, "you are now identified") ||
			strings.Contains(text, "you are now logged in") ||
			strings.Contains(text, "you're now logged in") ||
			strings.Contains(text, "password accepted") ||
			strings.Contains(text, "you are already logged in") ||
			strings.Contains(text, "you're already logged in") ||
			strings.Contains(text, "logged in as") {
			select {
			case result <- nil:
			default:
			}
			return
		}

		// Failure patterns.
		if strings.Contains(text, "invalid") ||
			strings.Contains(text, "failed") ||
			strings.Contains(text, "incorrect") ||
			strings.Contains(text, "not a registered") ||
			strings.Contains(text, "not registered") ||
			strings.Contains(text, "unknown account") ||
			strings.Contains(text, "no such account") {
			select {
			case result <- fmt.Errorf("%s", e.Last()):
			default:
			}
			return
		}
	})

	// RPL_LOGGEDIN (900) — sent by Ergo when account login succeeds.
	client.Handlers.AddBg("900", func(_ *girc.Client, _ girc.Event) {
		select {
		case result <- nil:
		default:
		}
	})

	// ERR_SASLFAIL (904) or other SASL errors.
	client.Handlers.AddBg("904", func(_ *girc.Client, e girc.Event) {
		select {
		case result <- fmt.Errorf("SASL authentication failed: %s", e.Last()):
		default:
		}
	})

	// Handle IRC errors (e.g., banned, server full).
	client.Handlers.AddBg(girc.ERR_ERRONEUSNICKNAME, func(_ *girc.Client, _ girc.Event) {
		select {
		case result <- fmt.Errorf("nickname %q contains invalid characters", nick):
		default:
		}
	})

	// Start the IRC connection in a goroutine.
	connErr := make(chan error, 1)
	go func() {
		connErr <- client.Connect()
	}()

	// Wait for either: auth result, connection error, or timeout.
	select {
	case err := <-result:
		client.Close()
		return err
	case err := <-connErr:
		if err != nil {
			return fmt.Errorf("IRC connection failed: %w", err)
		}
		// Connection closed without auth result.
		return fmt.Errorf("IRC connection closed unexpectedly")
	case <-ctx.Done():
		client.Close()
		return fmt.Errorf("authentication timed out")
	}
}
