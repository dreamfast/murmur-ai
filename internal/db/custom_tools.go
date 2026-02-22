package db

import (
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// CustomTool represents a user-defined tool stored in the database.
type CustomTool struct {
	// Name is the unique tool name (primary key).
	Name string
	// Description is a human-readable description of what the tool does.
	Description string
	// Parameters is a JSON schema string defining the tool's parameters.
	Parameters string
	// Backend is the execution backend: "shell", "http", "code_exec", or "pipeline".
	Backend string
	// BackendConfig is a JSON string with backend-specific configuration
	// (e.g., command template, URL, language).
	BackendConfig string
	// Enabled controls whether the tool is active.
	Enabled bool
	// Created is the timestamp when the tool was created.
	Created time.Time
	// Updated is the timestamp when the tool was last modified.
	Updated time.Time
}

// InsertCustomTool inserts a new custom tool into the database.
// Returns an error if a tool with the same name already exists.
func (db *DB) InsertCustomTool(tool *CustomTool) error {
	_, err := db.Exec(
		`INSERT INTO custom_tools (name, description, parameters, backend, backend_config, enabled)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		tool.Name, tool.Description, tool.Parameters, tool.Backend, tool.BackendConfig, tool.Enabled,
	)
	if err != nil {
		return fmt.Errorf("InsertCustomTool: %w", err)
	}
	return nil
}

// GetCustomTool retrieves a custom tool by name.
// Returns sql.ErrNoRows if the tool does not exist.
func (db *DB) GetCustomTool(name string) (*CustomTool, error) {
	var tool CustomTool
	err := db.QueryRow(
		`SELECT name, description, parameters, backend, backend_config, enabled, created, updated
		 FROM custom_tools WHERE name = ?`,
		name,
	).Scan(&tool.Name, &tool.Description, &tool.Parameters, &tool.Backend,
		&tool.BackendConfig, &tool.Enabled, &tool.Created, &tool.Updated)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
		return nil, fmt.Errorf("GetCustomTool: %w", err)
	}
	return &tool, nil
}

// ListCustomTools returns all custom tools, optionally filtered by enabled status.
// If enabledOnly is true, only enabled tools are returned.
func (db *DB) ListCustomTools(enabledOnly bool) ([]CustomTool, error) {
	query := `SELECT name, description, parameters, backend, backend_config, enabled, created, updated
		 FROM custom_tools`
	if enabledOnly {
		query += ` WHERE enabled = 1`
	}
	query += ` ORDER BY name`

	rows, err := db.Query(query)
	if err != nil {
		return nil, fmt.Errorf("ListCustomTools: %w", err)
	}
	defer rows.Close()

	var tools []CustomTool
	for rows.Next() {
		var tool CustomTool
		if err := rows.Scan(&tool.Name, &tool.Description, &tool.Parameters, &tool.Backend,
			&tool.BackendConfig, &tool.Enabled, &tool.Created, &tool.Updated); err != nil {
			return nil, fmt.Errorf("ListCustomTools: scan row: %w", err)
		}
		tools = append(tools, tool)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("ListCustomTools: iterate rows: %w", err)
	}

	return tools, nil
}

// UpdateCustomTool updates an existing custom tool's description, parameters,
// backend, backend_config, and enabled status. The updated timestamp is set
// automatically. Returns an error if the tool does not exist.
func (db *DB) UpdateCustomTool(tool *CustomTool) error {
	result, err := db.Exec(
		`UPDATE custom_tools
		 SET description = ?, parameters = ?, backend = ?, backend_config = ?,
		     enabled = ?, updated = CURRENT_TIMESTAMP
		 WHERE name = ?`,
		tool.Description, tool.Parameters, tool.Backend, tool.BackendConfig,
		tool.Enabled, tool.Name,
	)
	if err != nil {
		return fmt.Errorf("UpdateCustomTool: %w", err)
	}

	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("UpdateCustomTool: rows affected: %w", err)
	}
	if rows == 0 {
		return fmt.Errorf("UpdateCustomTool: tool %q not found", tool.Name)
	}

	return nil
}

// DeleteCustomTool deletes a custom tool by name.
// Returns an error if the tool does not exist.
func (db *DB) DeleteCustomTool(name string) error {
	result, err := db.Exec(`DELETE FROM custom_tools WHERE name = ?`, name)
	if err != nil {
		return fmt.Errorf("DeleteCustomTool: %w", err)
	}

	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("DeleteCustomTool: rows affected: %w", err)
	}
	if rows == 0 {
		return fmt.Errorf("DeleteCustomTool: tool %q not found", name)
	}

	return nil
}

// SetCustomToolEnabled enables or disables a custom tool by name.
// Returns an error if the tool does not exist.
func (db *DB) SetCustomToolEnabled(name string, enabled bool) error {
	result, err := db.Exec(
		`UPDATE custom_tools SET enabled = ?, updated = CURRENT_TIMESTAMP WHERE name = ?`,
		enabled, name,
	)
	if err != nil {
		return fmt.Errorf("SetCustomToolEnabled: %w", err)
	}

	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("SetCustomToolEnabled: rows affected: %w", err)
	}
	if rows == 0 {
		return fmt.Errorf("SetCustomToolEnabled: tool %q not found", name)
	}

	return nil
}
