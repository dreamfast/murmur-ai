package client

import (
	"context"
	"os"
	"runtime"
	"strconv"
	"strings"
	"time"

	"murmur/internal/bus"
)

// register sends a registration message to the server via the bus.
func (c *Client) register() {
	toolDefs := make([]bus.ToolDef, len(c.tools))
	copy(toolDefs, c.tools)

	if err := c.sender.SendRegister(c.cfg.Client.ID, c.cfg.Client.Hostname, toolDefs, c.cfg.Client.Autonomy); err != nil {
		c.logger.Error("failed to send registration", "error", err)
		return
	}
	c.logger.Info("registered with server",
		"client_id", c.cfg.Client.ID,
		"tools", len(c.tools),
		"autonomy", c.cfg.Client.Autonomy,
	)
}

// deregister sends a deregistration message to the server via the bus.
func (c *Client) deregister() {
	if err := c.sender.SendDeregister(c.cfg.Client.ID); err != nil {
		c.logger.Error("failed to send deregistration", "error", err)
		return
	}
	c.logger.Info("deregistered from server", "client_id", c.cfg.Client.ID)
}

// startHeartbeat runs a goroutine that sends periodic heartbeat messages.
// It stops when the context is cancelled.
func (c *Client) startHeartbeat(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			uptime := int64(time.Since(c.startTime).Seconds())
			load := getSystemLoad()

			if err := c.sender.SendHeartbeat(c.cfg.Client.ID, uptime, load); err != nil {
				c.logger.Warn("failed to send heartbeat", "error", err)
			}
		}
	}
}

// getSystemLoad reads system load metrics. On Linux, it reads /proc/loadavg
// and /proc/meminfo. On other platforms, it returns zero values.
func getSystemLoad() bus.LoadInfo {
	if runtime.GOOS != "linux" {
		return bus.LoadInfo{}
	}

	load := bus.LoadInfo{}

	// Read CPU load average.
	if data, err := os.ReadFile("/proc/loadavg"); err == nil {
		fields := strings.Fields(string(data))
		if len(fields) >= 1 {
			if val, err := strconv.ParseFloat(fields[0], 64); err == nil {
				load.CPU = val
			}
		}
	}

	// Read memory usage percentage.
	if data, err := os.ReadFile("/proc/meminfo"); err == nil {
		var total, available float64
		var hasAvailable bool
		for _, line := range strings.Split(string(data), "\n") {
			fields := strings.Fields(line)
			if len(fields) < 2 {
				continue
			}
			val, err := strconv.ParseFloat(fields[1], 64)
			if err != nil {
				continue
			}
			switch {
			case strings.HasPrefix(line, "MemTotal:"):
				total = val
			case strings.HasPrefix(line, "MemAvailable:"):
				available = val
				hasAvailable = true
			}
		}
		if total > 0 && hasAvailable {
			load.Memory = ((total - available) / total) * 100
		}
	}

	return load
}
