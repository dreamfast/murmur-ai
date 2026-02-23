package db

import (
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// ErrContainerNotFound is returned when a docker container record does not exist.
var ErrContainerNotFound = errors.New("container not found")

// ContainerRow represents a row in the docker_containers table.
type ContainerRow struct {
	// ID is the auto-increment primary key.
	ID int64 `json:"id"`
	// ContainerID is the Docker container ID (unique).
	ContainerID string `json:"container_id"`
	// Name is the human-readable container name (unique, prefixed with "murmur-").
	Name string `json:"name"`
	// Image is the Docker image used to create the container.
	Image string `json:"image"`
	// Status is the container status (created, running, stopped, removed, etc.).
	Status string `json:"status"`
	// Channel is the IRC channel where the container was created.
	Channel string `json:"channel"`
	// Nick is the IRC nick of the user who created the container.
	Nick string `json:"nick"`
	// Ports is a JSON array of port mappings (e.g., ["8080:80", "443:443"]).
	Ports StringSlice `json:"ports"`
	// Created is the timestamp when the record was created.
	Created time.Time `json:"created"`
	// Updated is the timestamp when the record was last modified.
	Updated time.Time `json:"updated"`
}

// containerColumns is the column list used in SELECT queries for the docker_containers table.
const containerColumns = `id, container_id, name, image, status, channel, nick, ports, created, updated`

// scanContainer scans a single container row from the given scanner (Row or Rows).
func scanContainer(scan func(dest ...any) error) (*ContainerRow, error) {
	var c ContainerRow
	var ports string

	err := scan(
		&c.ID, &c.ContainerID, &c.Name, &c.Image, &c.Status,
		&c.Channel, &c.Nick, &ports, &c.Created, &c.Updated,
	)
	if err != nil {
		return nil, err
	}

	if err := c.Ports.Scan(ports); err != nil {
		return nil, fmt.Errorf("scanContainer: ports: %w", err)
	}

	return &c, nil
}

// CreateContainer inserts a new container record into the database.
// On success, c.ID is set to the auto-generated primary key.
// Returns an error if a container with the same container_id or name already exists.
func (db *DB) CreateContainer(c *ContainerRow) error {
	ports, err := c.Ports.Value()
	if err != nil {
		return fmt.Errorf("CreateContainer: marshal ports: %w", err)
	}

	result, err := db.Exec(
		`INSERT INTO docker_containers (container_id, name, image, status, channel, nick, ports)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		c.ContainerID, c.Name, c.Image, c.Status, c.Channel, c.Nick, ports,
	)
	if err != nil {
		return fmt.Errorf("CreateContainer: %w", err)
	}

	id, err := result.LastInsertId()
	if err != nil {
		return fmt.Errorf("CreateContainer: last insert id: %w", err)
	}
	c.ID = id
	return nil
}

// UpdateContainerStatus updates the status of a container identified by its
// Docker container ID. Returns ErrContainerNotFound if no matching record exists.
func (db *DB) UpdateContainerStatus(containerID, status string) error {
	result, err := db.Exec(
		`UPDATE docker_containers SET status = ?, updated = CURRENT_TIMESTAMP
		 WHERE container_id = ?`,
		status, containerID,
	)
	if err != nil {
		return fmt.Errorf("UpdateContainerStatus: %w", err)
	}

	n, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("UpdateContainerStatus: rows affected: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("UpdateContainerStatus: %w: %q", ErrContainerNotFound, containerID)
	}
	return nil
}

// GetContainer retrieves a container by its Docker container ID.
// Returns ErrContainerNotFound if no matching record exists.
func (db *DB) GetContainer(containerID string) (*ContainerRow, error) {
	row := db.QueryRow(
		`SELECT `+containerColumns+` FROM docker_containers WHERE container_id = ?`,
		containerID,
	)
	c, err := scanContainer(row.Scan)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("GetContainer: %w: %q", ErrContainerNotFound, containerID)
		}
		return nil, fmt.Errorf("GetContainer: %w", err)
	}
	return c, nil
}

// GetContainerByName retrieves a container by its name.
// Returns ErrContainerNotFound if no matching record exists.
func (db *DB) GetContainerByName(name string) (*ContainerRow, error) {
	row := db.QueryRow(
		`SELECT `+containerColumns+` FROM docker_containers WHERE name = ?`,
		name,
	)
	c, err := scanContainer(row.Scan)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("GetContainerByName: %w: %q", ErrContainerNotFound, name)
		}
		return nil, fmt.Errorf("GetContainerByName: %w", err)
	}
	return c, nil
}

// ListContainers returns all container records ordered by creation time (newest first).
func (db *DB) ListContainers() ([]ContainerRow, error) {
	rows, err := db.Query(
		`SELECT ` + containerColumns + ` FROM docker_containers ORDER BY created DESC, id DESC`,
	)
	if err != nil {
		return nil, fmt.Errorf("ListContainers: %w", err)
	}
	defer rows.Close()

	var containers []ContainerRow
	for rows.Next() {
		c, err := scanContainer(rows.Scan)
		if err != nil {
			return nil, fmt.Errorf("ListContainers: scan row: %w", err)
		}
		containers = append(containers, *c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("ListContainers: iterate rows: %w", err)
	}
	return containers, nil
}

// RemoveContainer deletes a container record by its Docker container ID.
// Returns ErrContainerNotFound if no matching record exists.
func (db *DB) RemoveContainer(containerID string) error {
	result, err := db.Exec(
		`DELETE FROM docker_containers WHERE container_id = ?`,
		containerID,
	)
	if err != nil {
		return fmt.Errorf("RemoveContainer: %w", err)
	}

	n, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("RemoveContainer: rows affected: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("RemoveContainer: %w: %q", ErrContainerNotFound, containerID)
	}
	return nil
}

// DeleteRemovedContainers deletes all container records with status "removed".
// Returns the number of rows deleted.
func (db *DB) DeleteRemovedContainers() (int64, error) {
	result, err := db.Exec(`DELETE FROM docker_containers WHERE status = 'removed'`)
	if err != nil {
		return 0, fmt.Errorf("DeleteRemovedContainers: %w", err)
	}

	n, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("DeleteRemovedContainers: rows affected: %w", err)
	}
	return n, nil
}

// CountActiveContainers returns the number of containers that are not in
// "removed" or "exited" status. Used to enforce the max_containers limit.
func (db *DB) CountActiveContainers() (int, error) {
	var count int
	err := db.QueryRow(
		`SELECT COUNT(*) FROM docker_containers WHERE status NOT IN ('removed', 'exited')`,
	).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("CountActiveContainers: %w", err)
	}
	return count, nil
}
