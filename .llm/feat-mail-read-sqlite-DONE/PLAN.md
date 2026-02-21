# Plan: SQLite-backed mail_read tool

## Goal
Replace the mbox file parser with Thunderbird's `global-messages-db.sqlite` as the primary query engine for the `mail_read` tool. This gives us indexed queries (10-20x faster), access to subject/author/body/read-status without parsing mbox files, and cross-account search.

## Background
Thunderbird maintains a `global-messages-db.sqlite` in the profile directory with:
- `messages` table — id, date, headerMessageID, folderID, jsonAttributes (JSON with read status at key `59`, from contact ID at `43`, to at `44`)
- `folderLocations` table — maps folderID to folder URI and name
- `messagesText_content` table — docid (= messages.id), c0body, c1subject, c2attachmentNames, c3author, c4recipients
- `contacts` + `identities` tables — contact info with email, name, popularity

The FTS3 virtual table `messagesText` uses Thunderbird's custom `mozporter` tokenizer which isn't available in `modernc.org/sqlite`, but we can query `messagesText_content` directly with LIKE for search.

## Key design decisions
1. **SQLite-first, mbox fallback** — Use SQLite for all actions. Keep mbox parsing code but only use it as fallback when the SQLite DB is unavailable.
2. **Read-only immutable mode** — Open with `?mode=ro&immutable=1` to avoid locking issues with a running Thunderbird.
3. **Account filtering via folderLocations** — The `folderURI` contains the account email (URL-encoded). We can filter by account by matching folder URIs.
4. **No new dependencies** — We already have `modernc.org/sqlite` in go.mod.

## Tasks

### Task 1: Add SQLite query layer (`mail_read_db.go`)
Create `internal/tools/mail_read_db.go` with:
- `type mailDB struct` wrapping `*sql.DB`
- `openMailDB(profilePath string) (*mailDB, error)` — opens read-only with immutable flag
- `func (db *mailDB) Close() error`
- `func (db *mailDB) unread(folderFilter string, limit int) ([]mailMessage, error)` — query unread messages using `json_extract(jsonAttributes, '$.59') = 0`, join messagesText_content for subject/author, join folderLocations for folder name, filter by folderURI LIKE pattern
- `func (db *mailDB) search(query string, folderFilter string, limit int) ([]mailMessage, error)` — LIKE search on c1subject, c3author, c0body
- `func (db *mailDB) readByID(headerMessageID string) (*mailMessage, error)` — exact match on headerMessageID, return full body from c0body
- `func (db *mailDB) folders() ([]folderInfo, error)` — list all folders with message counts
- `func folderURIPattern(accounts []thunderbirdAccount, selector string) string` — convert account selector to a folderURI LIKE pattern

The `mailMessage` struct is reused from mail_read.go. The `Date` field will be formatted from the unix timestamp in the DB.

### Task 2: Refactor `handleMailRead` to use SQLite with mbox fallback
Modify `internal/tools/mail_read.go`:
- In `NewMailReadTool`, try to open the SQLite DB at creation time. Store the `*mailDB` in the closure. If it fails, log a warning and fall through to mbox.
- In `handleMailRead`, check if `mailDB` is non-nil. If so, dispatch to SQLite query methods. If not, fall through to existing mbox code.
- Add a new `folders` action that lists all folders (only available with SQLite).
- Update the tool description and JSON schema to include the `folders` action.
- The `account` parameter now filters by folderURI pattern instead of resolving to a mail directory.
- The `folder` parameter filters by folder name within the account.

### Task 3: Write tests for the SQLite query layer
Create `internal/tools/mail_read_db_test.go` with:
- Helper to create an in-memory SQLite DB with the Thunderbird schema and seed data
- Test `unread()` — verify filtering by read status and folder
- Test `search()` — verify LIKE matching on subject, author, body
- Test `readByID()` — verify full message retrieval including body
- Test `folders()` — verify folder listing with counts
- Test `folderURIPattern()` — verify account selector to URI pattern conversion
- Test fallback behavior — when DB is nil, mbox code path is used

### Task 4: Update existing tests and integration
- Update `TestMailRead_AccountsAction` and related tests to work with the new code path
- Ensure all existing mbox tests still pass (they test the fallback path)
- Add integration-style test that creates both a SQLite DB and mbox files, verifies SQLite is preferred
- Run full quality gates

### Task 5: Update config and Docker setup
- Update `configs/client.docker.toml` comments to document SQLite auto-discovery
- No config changes needed — the tool auto-discovers `global-messages-db.sqlite` in the profile directory
- The Docker volume mount already includes the full Thunderbird profile (which contains the SQLite DB)
