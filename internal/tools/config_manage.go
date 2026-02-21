package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"
)

// configDenyPatterns is the list of key patterns that cannot be written via the
// config_manage tool. These protect sensitive configuration values from being
// modified by the LLM.
var configDenyPatterns = []string{
	`^security\.`,
	`^vault\.`,
	`^irc\.password$`,
	`^irc\.nickserv_password$`,
	`^api\.api_key$`,
	`^llm\.providers\.[^.]+\.api_key$`,
}

// compiledDenyPatterns is the compiled version of configDenyPatterns.
var compiledDenyPatterns []*regexp.Regexp

// vaultMaskRe matches vault: prefixed values in TOML content for masking.
var vaultMaskRe = regexp.MustCompile(`("vault:[^"]*"|'vault:[^']*')`)

// nextSectionRe matches the start of any TOML section header.
var nextSectionRe = regexp.MustCompile(`(?m)^\s*\[`)

func init() {
	for _, p := range configDenyPatterns {
		compiledDenyPatterns = append(compiledDenyPatterns, regexp.MustCompile(p))
	}
}

// ConfigManageToolConfig holds the configuration for the config_manage tool.
type ConfigManageToolConfig struct {
	// ConfigPath is the path to the TOML configuration file to manage.
	ConfigPath string
}

// NewConfigManageTool creates the config_manage tool for reading and writing
// the TOML configuration file at runtime. The tool supports reading the full
// config, reading specific sections, setting values, and listing sections.
// Sensitive keys are protected by a deny-list and vault: values are masked.
func NewConfigManageTool(cfg ConfigManageToolConfig) Tool {
	return Tool{
		Name:        "config_manage",
		Description: "Read and write the TOML configuration file. Supports reading the full config or specific sections, setting individual values, and listing available sections. Sensitive keys (security, vault, passwords, API keys) are protected and cannot be modified. Values prefixed with 'vault:' are masked in output.",
		Parameters: json.RawMessage(`{
			"type": "object",
			"properties": {
				"action": {
					"type": "string",
					"description": "Action to perform: 'read', 'read_section', 'set', 'list_sections'",
					"enum": ["read", "read_section", "set", "list_sections"]
				},
				"section": {
					"type": "string",
					"description": "TOML section path to read (e.g., 'irc', 'llm.providers', 'tools.shell'). Required for 'read_section'."
				},
				"key": {
					"type": "string",
					"description": "Dotted key path to set (e.g., 'irc.nick', 'tools.shell.enabled', 'memory.max_history'). Required for 'set'."
				},
				"value": {
					"type": "string",
					"description": "Value to set. Strings are set as-is, 'true'/'false' as booleans, numeric strings as numbers. Required for 'set'."
				}
			},
			"required": ["action"]
		}`),
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			return handleConfigManage(ctx, args, cfg.ConfigPath)
		},
	}
}

// handleConfigManage dispatches to the appropriate config management action.
func handleConfigManage(_ context.Context, args map[string]any, configPath string) (string, error) {
	action, err := RequireStringArg(args, "action")
	if err != nil {
		return "", err
	}

	switch action {
	case "read":
		return configRead(configPath)
	case "read_section":
		section, err := RequireStringArg(args, "section")
		if err != nil {
			return "", fmt.Errorf("handleConfigManage: %w", err)
		}
		return configReadSection(configPath, section)
	case "set":
		key, err := RequireStringArg(args, "key")
		if err != nil {
			return "", fmt.Errorf("handleConfigManage: %w", err)
		}
		value, err := RequireStringArg(args, "value")
		if err != nil {
			return "", fmt.Errorf("handleConfigManage: %w", err)
		}
		return configSet(configPath, key, value)
	case "list_sections":
		return configListSections(configPath)
	default:
		return "", fmt.Errorf("handleConfigManage: unknown action %q, must be one of: read, read_section, set, list_sections", action)
	}
}

// configRead reads the full TOML config and returns it with vault: values masked.
func configRead(configPath string) (string, error) {
	data, err := os.ReadFile(configPath)
	if err != nil {
		return "", fmt.Errorf("configRead: %w", err)
	}
	return TruncateOutput(maskVaultValues(string(data))), nil
}

// configReadSection reads a specific section from the TOML config.
func configReadSection(configPath, section string) (string, error) {
	data, err := os.ReadFile(configPath)
	if err != nil {
		return "", fmt.Errorf("configReadSection: %w", err)
	}

	// Parse the TOML into a generic map.
	var parsed map[string]any
	if _, err := toml.Decode(string(data), &parsed); err != nil {
		return "", fmt.Errorf("configReadSection: parse TOML: %w", err)
	}

	// Navigate to the requested section.
	parts := strings.Split(section, ".")
	var current any = parsed
	for _, part := range parts {
		m, ok := current.(map[string]any)
		if !ok {
			return "", fmt.Errorf("configReadSection: section %q not found", section)
		}
		current, ok = m[part]
		if !ok {
			return "", fmt.Errorf("configReadSection: section %q not found", section)
		}
	}

	// Format the section as TOML.
	var b strings.Builder
	fmt.Fprintf(&b, "[%s]\n", section)
	formatTOMLValue(&b, current, section)

	return TruncateOutput(maskVaultValues(b.String())), nil
}

// configSet sets a value in the TOML config file using targeted string replacement.
// Uses atomic write (temp file + os.Rename) for safety.
func configSet(configPath, key, value string) (string, error) {
	// Check deny-list.
	if isConfigKeyDenied(key) {
		return "", fmt.Errorf("configSet: key %q is protected and cannot be modified", key)
	}

	// Check if value contains vault: prefix.
	if strings.HasPrefix(value, "vault:") {
		return "", fmt.Errorf("configSet: cannot set vault: prefixed values via config_manage")
	}

	// Read current config.
	data, err := os.ReadFile(configPath)
	if err != nil {
		return "", fmt.Errorf("configSet: read config: %w", err)
	}

	content := string(data)

	// Find and replace the key's value in the TOML file.
	newContent, found := replaceConfigValue(content, key, value)
	if !found {
		return "", fmt.Errorf("configSet: key %q not found in config file", key)
	}

	// Atomic write: write to temp file, then rename.
	dir := filepath.Dir(configPath)
	tmpFile, err := os.CreateTemp(dir, ".murmur-config-*.tmp")
	if err != nil {
		return "", fmt.Errorf("configSet: create temp file: %w", err)
	}
	tmpPath := tmpFile.Name()

	if _, err := tmpFile.WriteString(newContent); err != nil {
		tmpFile.Close()
		os.Remove(tmpPath)
		return "", fmt.Errorf("configSet: write temp file: %w", err)
	}
	if err := tmpFile.Sync(); err != nil {
		tmpFile.Close()
		os.Remove(tmpPath)
		return "", fmt.Errorf("configSet: sync temp file: %w", err)
	}
	if err := tmpFile.Close(); err != nil {
		os.Remove(tmpPath)
		return "", fmt.Errorf("configSet: close temp file: %w", err)
	}

	// Validate the new content is valid TOML before committing.
	var check map[string]any
	if _, err := toml.Decode(newContent, &check); err != nil {
		os.Remove(tmpPath)
		return "", fmt.Errorf("configSet: new config is invalid TOML: %w", err)
	}

	if err := os.Rename(tmpPath, configPath); err != nil {
		os.Remove(tmpPath)
		return "", fmt.Errorf("configSet: rename temp file: %w", err)
	}

	return fmt.Sprintf("Set %s = %s\nNote: A restart is required for changes to take effect.", key, value), nil
}

// configListSections lists the top-level sections in the TOML config.
func configListSections(configPath string) (string, error) {
	data, err := os.ReadFile(configPath)
	if err != nil {
		return "", fmt.Errorf("configListSections: %w", err)
	}

	var parsed map[string]any
	if _, err := toml.Decode(string(data), &parsed); err != nil {
		return "", fmt.Errorf("configListSections: parse TOML: %w", err)
	}

	if len(parsed) == 0 {
		return "No sections found.", nil
	}

	keys := make([]string, 0, len(parsed))
	for key := range parsed {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	var b strings.Builder
	b.WriteString("Configuration sections:\n")
	for _, key := range keys {
		fmt.Fprintf(&b, "  - %s\n", key)
	}

	return b.String(), nil
}

// isConfigKeyDenied checks if a key matches any deny pattern.
func isConfigKeyDenied(key string) bool {
	for _, re := range compiledDenyPatterns {
		if re.MatchString(key) {
			return true
		}
	}
	return false
}

// maskVaultValues replaces vault: prefixed values in TOML content with
// "vault:****" to prevent leaking secret references.
func maskVaultValues(content string) string {
	return vaultMaskRe.ReplaceAllString(content, `"vault:****"`)
}

// replaceConfigValue performs targeted string replacement of a TOML key's value.
// It handles the dotted key path by finding the correct section and key line.
// Uses anchored regex matching to avoid matching section names in comments or strings.
// Returns the new content and whether the key was found.
func replaceConfigValue(content, key, value string) (string, bool) {
	parts := strings.Split(key, ".")
	if len(parts) < 2 {
		// Top-level key — search for key = value at the start of the file.
		return replaceKeyInContent(content, key, value, 0)
	}

	// Split into section path and leaf key.
	sectionParts := parts[:len(parts)-1]
	leafKey := parts[len(parts)-1]

	// Build an anchored regex to find the section header at line start.
	sectionName := strings.Join(sectionParts, ".")
	sectionPattern := regexp.MustCompile(`(?m)^\s*\[` + regexp.QuoteMeta(sectionName) + `\]\s*$`)

	// Find the section in the content.
	loc := sectionPattern.FindStringIndex(content)
	if loc == nil {
		return content, false
	}

	// Search for the key within this section (after the header).
	sectionStart := loc[1]
	return replaceKeyInContent(content, leafKey, value, sectionStart)
}

// replaceKeyInContent replaces the value of a key in the content starting from
// the given offset. It searches for the pattern: key = value (with optional
// whitespace and quotes). Only searches within the current section scope
// (up to the next section header).
func replaceKeyInContent(content, key, value string, startOffset int) (string, bool) {
	// Build a regex to match the key = value line.
	// Handles: key = "string", key = 123, key = true, key = 'string'
	keyPattern := regexp.MustCompile(`(?m)^(\s*` + regexp.QuoteMeta(key) + `\s*=\s*)(.+)$`)

	searchContent := content[startOffset:]

	// Find the next section header to limit our search scope.
	nextSectionLoc := nextSectionRe.FindStringIndex(searchContent)
	var searchEnd int
	if nextSectionLoc != nil {
		searchEnd = nextSectionLoc[0]
	} else {
		searchEnd = len(searchContent)
	}

	scopedContent := searchContent[:searchEnd]

	loc := keyPattern.FindStringIndex(scopedContent)
	if loc == nil {
		return content, false
	}

	// Format the value appropriately for TOML.
	formattedValue := formatTOMLSetValue(value)

	// Replace the matched line.
	newLine := keyPattern.ReplaceAllString(scopedContent[loc[0]:loc[1]], "${1}"+formattedValue)

	result := content[:startOffset+loc[0]] + newLine + content[startOffset+loc[1]:]
	return result, true
}

// formatTOMLSetValue formats a value for TOML assignment.
// Booleans and numbers are unquoted, strings are quoted.
func formatTOMLSetValue(value string) string {
	// Boolean values.
	if value == "true" || value == "false" {
		return value
	}

	// Integer values.
	if isInteger(value) {
		return value
	}

	// Float values.
	if isFloat(value) {
		return value
	}

	// String values — quote them.
	return fmt.Sprintf("%q", value)
}

// isInteger checks if a string represents an integer.
func isInteger(s string) bool {
	if s == "" {
		return false
	}
	start := 0
	if s[0] == '-' || s[0] == '+' {
		start = 1
	}
	if start >= len(s) {
		return false
	}
	for i := start; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// isFloat checks if a string represents a floating-point number.
func isFloat(s string) bool {
	if s == "" {
		return false
	}
	hasDot := false
	start := 0
	if s[0] == '-' || s[0] == '+' {
		start = 1
	}
	if start >= len(s) {
		return false
	}
	for i := start; i < len(s); i++ {
		if s[i] == '.' {
			if hasDot {
				return false
			}
			hasDot = true
			continue
		}
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return hasDot // Must have at least one dot to be a float.
}

// formatTOMLValue formats a generic value for TOML output. The parentPath
// parameter is used to build fully qualified section headers for nested tables.
func formatTOMLValue(b *strings.Builder, v any, parentPath string) {
	switch val := v.(type) {
	case map[string]any:
		// Sort keys for deterministic output.
		keys := make([]string, 0, len(val))
		for k := range val {
			keys = append(keys, k)
		}
		sort.Strings(keys)

		for _, k := range keys {
			v2 := val[k]
			switch inner := v2.(type) {
			case map[string]any:
				fullPath := k
				if parentPath != "" {
					fullPath = parentPath + "." + k
				}
				fmt.Fprintf(b, "[%s]\n", fullPath)
				formatTOMLValue(b, inner, fullPath)
			default:
				fmt.Fprintf(b, "%s = %s\n", k, formatScalarTOML(v2))
			}
		}
	default:
		fmt.Fprintf(b, "%s\n", formatScalarTOML(v))
	}
}

// formatScalarTOML formats a scalar value for TOML output.
func formatScalarTOML(v any) string {
	switch val := v.(type) {
	case string:
		return fmt.Sprintf("%q", val)
	case bool:
		if val {
			return "true"
		}
		return "false"
	case int64:
		return fmt.Sprintf("%d", val)
	case float64:
		return fmt.Sprintf("%g", val)
	default:
		return fmt.Sprintf("%v", val)
	}
}
