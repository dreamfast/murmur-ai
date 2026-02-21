// Package main is the entry point for the murmur binary.
// It provides subcommands for running the server, client, and utility commands.
package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/lrstanley/girc"
	"golang.org/x/term"

	"murmur/internal/client"
	"murmur/internal/config"
	"murmur/internal/server"
	"murmur/internal/vault"
)

// version is set at build time via -ldflags.
var version = "dev"

func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	switch os.Args[1] {
	case "version":
		fmt.Printf("murmur %s\n", version)
	case "server":
		runServer()
	case "client":
		runClient()
	case "send":
		runSend()
	case "status":
		runStatus()
	case "vault":
		runVault()
	case "help", "-h", "--help":
		printUsage()
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n\n", os.Args[1])
		printUsage()
		os.Exit(1)
	}
}

// runServer starts the Murmur server.
func runServer() {
	configPath := parseConfigFlag("~/.murmur/server.toml")
	logger := newLogger()

	cfg, err := config.LoadServerConfig(configPath)
	if err != nil {
		logger.Error("failed to load server config", "error", err)
		os.Exit(1)
	}

	srv, err := server.New(cfg, logger)
	if err != nil {
		logger.Error("failed to create server", "error", err)
		os.Exit(1)
	}

	ctx := signalContext()

	if err := srv.Run(ctx); err != nil {
		logger.Error("server exited with error", "error", err)
		os.Exit(1)
	}
}

// runClient starts a Murmur client.
func runClient() {
	configPath := parseConfigFlag("~/.murmur/client.toml")
	logger := newLogger()

	cfg, err := config.LoadClientConfig(configPath)
	if err != nil {
		logger.Error("failed to load client config", "error", err)
		os.Exit(1)
	}

	cli, err := client.New(cfg, logger)
	if err != nil {
		logger.Error("failed to create client", "error", err)
		os.Exit(1)
	}

	ctx := signalContext()

	if err := cli.Run(ctx); err != nil {
		logger.Error("client exited with error", "error", err)
		os.Exit(1)
	}
}

// runSend connects to IRC, sends a message to the agent's main channel,
// collects the response, prints it to stdout, and disconnects.
func runSend() {
	configPath := parseConfigFlag("~/.murmur/server.toml")

	// Extract the message from remaining args (skip --config flag pair).
	message := parseSendMessage()
	if message == "" {
		fmt.Fprintln(os.Stderr, "usage: murmur send [--config path] \"message\"")
		os.Exit(1)
	}

	cfg, err := config.LoadServerConfig(configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error loading config: %v\n", err)
		os.Exit(1)
	}

	response, err := ircSendAndCollect(cfg, message, 60*time.Second)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	fmt.Println(response)
}

// runStatus connects to IRC, sends !status to the main channel, collects the
// response, prints it to stdout, and disconnects.
func runStatus() {
	configPath := parseConfigFlag("~/.murmur/server.toml")

	cfg, err := config.LoadServerConfig(configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error loading config: %v\n", err)
		os.Exit(1)
	}

	response, err := ircSendAndCollect(cfg, "!status", 30*time.Second)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	fmt.Println(response)
}

// ircSendAndCollect creates a temporary IRC connection, sends a message to the
// server's main channel, collects response lines from the server's nick, and
// returns the collected response. It disconnects after the response is collected
// or the timeout expires.
func ircSendAndCollect(cfg *config.ServerConfig, message string, timeout time.Duration) (string, error) {
	// Generate a unique temporary nick.
	nick := "murmur-cli-" + randomHex(4)

	gircCfg := girc.Config{
		Server: cfg.IRC.Server,
		Port:   cfg.IRC.Port,
		Nick:   nick,
		User:   "murmur-cli",
		Name:   "Murmur CLI",
	}

	if cfg.IRC.TLS {
		gircCfg.SSL = true
		gircCfg.TLSConfig = &tls.Config{
			ServerName: cfg.IRC.Server,
			MinVersion: tls.VersionTLS12,
		}
	}

	if cfg.IRC.Password != "" {
		gircCfg.ServerPass = cfg.IRC.Password
	}

	ircClient := girc.New(gircCfg)

	var (
		mu        sync.Mutex
		lines     []string
		silenceT  *time.Timer
		done      = make(chan struct{})
		closeOnce sync.Once
	)

	// closeDone safely closes the done channel exactly once.
	closeDone := func() {
		closeOnce.Do(func() { close(done) })
	}

	// Silence timeout: after receiving the first response line, wait 3s of
	// silence before considering the response complete.
	const silenceTimeout = 3 * time.Second

	serverNick := cfg.IRC.Nick
	mainChannel := cfg.IRC.Channels.Main

	// On connect: join the main channel and send the message.
	ircClient.Handlers.Add(girc.CONNECTED, func(c *girc.Client, e girc.Event) {
		c.Cmd.Join(mainChannel)
		// Small delay to ensure we've joined before sending.
		time.AfterFunc(500*time.Millisecond, func() {
			c.Cmd.Message(mainChannel, message)
		})
	})

	// Collect responses from the server's nick on the main channel.
	ircClient.Handlers.Add(girc.PRIVMSG, func(c *girc.Client, e girc.Event) {
		// Guard against malformed IRC messages.
		if e.Source == nil || len(e.Params) == 0 {
			return
		}
		if !strings.EqualFold(e.Source.Name, serverNick) {
			return
		}
		if !strings.EqualFold(e.Params[0], mainChannel) {
			return
		}

		mu.Lock()
		lines = append(lines, e.Last())
		// Reset or start the silence timer.
		if silenceT != nil {
			silenceT.Reset(silenceTimeout)
		} else {
			silenceT = time.AfterFunc(silenceTimeout, closeDone)
		}
		mu.Unlock()
	})

	// Connect in a goroutine.
	errCh := make(chan error, 1)
	go func() {
		errCh <- ircClient.Connect()
	}()

	// Wait for response, timeout, or connection error.
	select {
	case <-done:
		// Response collected.
	case err := <-errCh:
		if err != nil {
			return "", fmt.Errorf("irc connection failed: %w", err)
		}
		return "", fmt.Errorf("irc connection closed unexpectedly")
	case <-time.After(timeout):
		// Overall timeout.
	}

	// Stop the silence timer to prevent late callback activity.
	mu.Lock()
	if silenceT != nil {
		silenceT.Stop()
	}
	mu.Unlock()

	// Disconnect.
	ircClient.Close()

	mu.Lock()
	result := strings.Join(lines, "\n")
	mu.Unlock()

	if result == "" {
		return "", fmt.Errorf("no response received within %s", timeout)
	}

	return result, nil
}

// parseSendMessage extracts the message argument from os.Args, skipping
// the "send" subcommand and any --config flag pair.
func parseSendMessage() string {
	args := os.Args[2:]
	var remaining []string
	for i := 0; i < len(args); i++ {
		if args[i] == "--config" && i+1 < len(args) {
			i++ // skip --config and its value
		} else {
			remaining = append(remaining, args[i])
		}
	}
	return strings.Join(remaining, " ")
}

// randomHex returns n random bytes encoded as a hex string.
// It panics if the system's cryptographic random number generator fails,
// as this indicates a critical system issue.
func randomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic("crypto/rand failed: " + err.Error())
	}
	return hex.EncodeToString(b)
}

// runVault handles the vault subcommand for managing encrypted secrets.
// Subcommands: set, get, list, delete.
func runVault() {
	// Parse vault-specific flags from os.Args[2:].
	vaultArgs := os.Args[2:]
	if len(vaultArgs) == 0 {
		printVaultUsage()
		os.Exit(1)
	}

	dbPath := defaultVaultDBPath()
	var remaining []string

	// Extract --db flag from args.
	for i := 0; i < len(vaultArgs); i++ {
		if vaultArgs[i] == "--db" && i+1 < len(vaultArgs) {
			dbPath = vaultArgs[i+1]
			i++ // skip the value
		} else {
			remaining = append(remaining, vaultArgs[i])
		}
	}

	if len(remaining) == 0 {
		printVaultUsage()
		os.Exit(1)
	}

	subcmd := remaining[0]
	subArgs := remaining[1:]

	switch subcmd {
	case "set":
		vaultSet(dbPath, subArgs)
	case "get":
		vaultGet(dbPath, subArgs)
	case "list":
		vaultList(dbPath)
	case "delete":
		vaultDelete(dbPath, subArgs)
	case "help", "-h", "--help":
		printVaultUsage()
	default:
		fmt.Fprintf(os.Stderr, "unknown vault subcommand: %s\n\n", subcmd)
		printVaultUsage()
		os.Exit(1)
	}
}

// vaultSet encrypts and stores a secret.
// Usage: murmur vault set [<key>]
// When called without arguments, prompts for both key name and secret value.
// When called with a key, prompts only for the secret value.
// Secret values are read with echo suppression on interactive terminals.
// When stdin is piped, reads the value as a plain line from stdin.
func vaultSet(dbPath string, args []string) {
	if len(args) > 1 {
		fmt.Fprintln(os.Stderr, "usage: murmur vault set [<key>]")
		os.Exit(1)
	}

	v := openVaultOrDie(dbPath)
	defer v.Close()

	var key string
	if len(args) == 1 {
		key = args[0]
	} else {
		// Prompt for key name (visible — it's not a secret).
		key = readLine("Key: ")
		if key == "" {
			fmt.Fprintln(os.Stderr, "error: key must not be empty")
			os.Exit(1)
		}
	}

	// Prompt for secret value (hidden on interactive terminals).
	value := readSecret("Value: ")
	if value == "" {
		fmt.Fprintln(os.Stderr, "error: value must not be empty")
		os.Exit(1)
	}

	if err := v.Set(key, value); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	fmt.Fprintf(os.Stderr, "stored %q\n", key)
}

// vaultGet decrypts and prints a secret to stdout.
// Usage: murmur vault get <key>
func vaultGet(dbPath string, args []string) {
	if len(args) != 1 {
		fmt.Fprintln(os.Stderr, "usage: murmur vault get <key>")
		os.Exit(1)
	}
	key := args[0]

	v := openVaultOrDie(dbPath)
	defer v.Close()

	value, err := v.Get(key)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	fmt.Println(value)
}

// vaultList prints all vault keys to stdout, one per line.
// Usage: murmur vault list
func vaultList(dbPath string) {
	v := openVaultOrDie(dbPath)
	defer v.Close()

	keys, err := v.List()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	for _, k := range keys {
		fmt.Println(k)
	}
}

// vaultDelete removes a secret from the vault.
// Usage: murmur vault delete <key>
func vaultDelete(dbPath string, args []string) {
	if len(args) != 1 {
		fmt.Fprintln(os.Stderr, "usage: murmur vault delete <key>")
		os.Exit(1)
	}
	key := args[0]

	v := openVaultOrDie(dbPath)
	defer v.Close()

	if err := v.Delete(key); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	fmt.Fprintf(os.Stderr, "deleted %q\n", key)
}

// openVaultOrDie opens the vault with the passphrase from the environment
// or prompts on stderr with echo suppression. Exits on failure.
func openVaultOrDie(dbPath string) *vault.Vault {
	passphrase := os.Getenv("MURMUR_VAULT_PASS")
	if passphrase == "" {
		passphrase = readSecret("Vault passphrase: ")
	}

	if passphrase == "" {
		fmt.Fprintln(os.Stderr, "error: passphrase must not be empty (set MURMUR_VAULT_PASS or enter at prompt)")
		os.Exit(1)
	}

	v, err := vault.Open(dbPath, passphrase)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error opening vault: %v\n", err)
		os.Exit(1)
	}
	return v
}

// readSecret prompts on stderr and reads a secret with echo suppression.
// On interactive terminals, uses x/term.ReadPassword to hide input.
// When stdin is piped (non-TTY), falls back to reading a plain line.
func readSecret(prompt string) string {
	fmt.Fprint(os.Stderr, prompt)

	fd := int(os.Stdin.Fd())
	if term.IsTerminal(fd) {
		b, err := term.ReadPassword(fd)
		if err != nil {
			fmt.Fprintln(os.Stderr) // ensure prompt line ends
			fmt.Fprintf(os.Stderr, "error reading input: %v\n", err)
			os.Exit(1)
		}
		fmt.Fprintln(os.Stderr) // newline after hidden input
		return string(b)
	}

	// Non-TTY: read a line from stdin (for piped input).
	scanner := bufio.NewScanner(os.Stdin)
	if scanner.Scan() {
		return scanner.Text()
	}
	if err := scanner.Err(); err != nil {
		fmt.Fprintf(os.Stderr, "error reading stdin: %v\n", err)
		os.Exit(1)
	}
	return ""
}

// readLine prompts on stderr and reads a visible line from stdin.
// Used for non-secret input like key names.
func readLine(prompt string) string {
	fmt.Fprint(os.Stderr, prompt)
	scanner := bufio.NewScanner(os.Stdin)
	if scanner.Scan() {
		return scanner.Text()
	}
	if err := scanner.Err(); err != nil {
		fmt.Fprintf(os.Stderr, "error reading stdin: %v\n", err)
		os.Exit(1)
	}
	return ""
}

// defaultVaultDBPath returns the default vault database path (~/.murmur/vault.db).
func defaultVaultDBPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(".", ".murmur", "vault.db")
	}
	return filepath.Join(home, ".murmur", "vault.db")
}

// printVaultUsage prints usage for the vault subcommand.
func printVaultUsage() {
	fmt.Fprintf(os.Stderr, `murmur vault — manage encrypted secrets

Usage:
  murmur vault set [<key>]    Store a secret (prompts for key and value interactively)
  murmur vault get <key>      Retrieve and decrypt a secret
  murmur vault list           List all secret keys
  murmur vault delete <key>   Remove a secret

Secret values are never passed as command-line arguments. When running
interactively, input is hidden (echo suppression). Piped input is also
supported: echo "secret" | murmur vault set my-key

Flags:
  --db <path>    Vault database path (default: ~/.murmur/vault.db)

Environment:
  MURMUR_VAULT_PASS    Vault passphrase (prompted if not set)
`)
}

// parseConfigFlag extracts the --config flag value from os.Args, returning
// the default path if not specified.
func parseConfigFlag(defaultPath string) string {
	for i := 2; i < len(os.Args); i++ {
		if os.Args[i] == "--config" && i+1 < len(os.Args) {
			return os.Args[i+1]
		}
	}
	return defaultPath
}

// signalContext returns a context that is cancelled when SIGINT or SIGTERM
// is received.
func signalContext() context.Context {
	ctx, cancel := context.WithCancel(context.Background())
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		sig := <-sigCh
		slog.Info("received signal, shutting down", "signal", sig)
		cancel()
	}()

	return ctx
}

// newLogger creates a structured logger for the application.
func newLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))
}

func printUsage() {
	fmt.Fprintf(os.Stderr, `murmur %s — distributed personal AI agent over IRC

Usage:
  murmur server [--config path]    Start the server (agent brain)
  murmur client [--config path]    Start a client (tool provider)
  murmur send [--config path] msg  Send a message and print the response
  murmur status [--config path]    Send !status and print the response
  murmur vault <subcommand>        Manage encrypted secrets vault
  murmur version                   Print version info
  murmur help                      Show this help
`, version)
}
