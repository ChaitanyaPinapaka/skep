package index

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	_ "modernc.org/sqlite"
)

// sanitizeFTS makes a user query safe for FTS5 MATCH.
// If the query looks like natural language or contains special chars (-,+,*,"),
// split into words and join with OR so each word matches independently.
func sanitizeFTS(query string) string {
	// If user used explicit FTS5 syntax (AND, OR, NOT, *, quotes), pass through
	for _, kw := range []string{" AND ", " OR ", " NOT ", "*", `"`} {
		if strings.Contains(query, kw) {
			return query
		}
	}

	// Split into words, quote each to prevent FTS5 operator interpretation
	words := strings.Fields(query)
	if len(words) == 0 {
		return query
	}
	var quoted []string
	for _, w := range words {
		// Quote the word to escape special chars like -
		quoted = append(quoted, `"`+strings.ReplaceAll(w, `"`, ``)+`"`)
	}
	return strings.Join(quoted, " OR ")
}

// Store wraps SQLite operations for the repo index.
type Store struct {
	db   *sql.DB
	path string

	// Prepared statements for hot-path operations (3x faster at scale)
	stmtUpsertFile   *sql.Stmt
	stmtInsertSymbol *sql.Stmt
	stmtDeleteFTS    *sql.Stmt
	stmtInsertFTS    *sql.Stmt
	stmtInsertEdge   *sql.Stmt
	stmtUpdateRank   *sql.Stmt
}

// OpenStore opens or creates the index database at the given path.
func OpenStore(dbPath string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
		return nil, fmt.Errorf("create db dir: %w", err)
	}

	db, err := sql.Open("sqlite", dbPath+"?_pragma=journal_mode(wal)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(on)")
	if err != nil {
		return nil, fmt.Errorf("open db: %w", err)
	}

	// Allow a small pool of connections so MCP read tools
	// (search_symbols, get_file_context, get_call_graph) can run in
	// parallel with task writes. SQLite WAL supports many readers +
	// one writer concurrently; modernc.org/sqlite serializes writes
	// internally. A cap of 4 gives the read path headroom without
	// opening enough connections to matter for memory.
	db.SetMaxOpenConns(4)

	s := &Store{db: db, path: dbPath}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	if err := s.prepareStatements(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

// prepareStatements compiles every hot-path SQL statement up front so
// later writes don't pay a parse cost on each call. Errors here are
// propagated to OpenStore — silently swallowing them (as earlier
// revisions did) leaves the Store holding nil statements, and the
// first use nil-panics deep inside the indexer.
func (s *Store) prepareStatements() error {
	prep := func(name, query string) (*sql.Stmt, error) {
		stmt, err := s.db.Prepare(query)
		if err != nil {
			return nil, fmt.Errorf("prepare %s: %w", name, err)
		}
		return stmt, nil
	}
	var err error
	if s.stmtUpsertFile, err = prep("upsertFile", `INSERT INTO files (path, content_hash, language, size_bytes, last_modified, symbol_count)
		VALUES (?, ?, ?, ?, ?, ?) ON CONFLICT(path) DO UPDATE SET
		content_hash=excluded.content_hash, language=excluded.language, size_bytes=excluded.size_bytes,
		last_modified=excluded.last_modified, symbol_count=excluded.symbol_count`); err != nil {
		return err
	}
	if s.stmtDeleteFTS, err = prep("deleteFTS", `DELETE FROM symbols_fts WHERE symbol_id=?`); err != nil {
		return err
	}
	if s.stmtInsertSymbol, err = prep("insertSymbol", `INSERT OR REPLACE INTO symbols (id, name, kind, file_path, line, end_line, signature, doc_comment, parent_id, rank) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`); err != nil {
		return err
	}
	if s.stmtInsertFTS, err = prep("insertFTS", `INSERT INTO symbols_fts (symbol_id, name, signature, doc_comment) VALUES (?, ?, ?, ?)`); err != nil {
		return err
	}
	if s.stmtInsertEdge, err = prep("insertEdge", `INSERT OR IGNORE INTO edges (from_id, to_id, kind) VALUES (?, ?, ?)`); err != nil {
		return err
	}
	if s.stmtUpdateRank, err = prep("updateRank", `UPDATE symbols SET rank=? WHERE id=?`); err != nil {
		return err
	}
	return nil
}

func (s *Store) Close() error {
	for _, stmt := range []*sql.Stmt{s.stmtUpsertFile, s.stmtInsertSymbol, s.stmtDeleteFTS, s.stmtInsertFTS, s.stmtInsertEdge, s.stmtUpdateRank} {
		if stmt != nil {
			stmt.Close()
		}
	}
	return s.db.Close()
}

func (s *Store) DB() *sql.DB { return s.db }

// GetMeta reads a metadata value.
func (s *Store) GetMeta(key string) string {
	var val string
	s.db.QueryRow("SELECT value FROM meta WHERE key=?", key).Scan(&val)
	return val
}

// SetMeta writes a metadata value.
func (s *Store) SetMeta(key, value string) {
	s.db.Exec("INSERT OR REPLACE INTO meta (key, value) VALUES (?, ?)", key, value)
}

func (s *Store) migrate() error {
	schema := `
CREATE TABLE IF NOT EXISTS files (
    path TEXT PRIMARY KEY,
    content_hash BLOB,
    language TEXT,
    size_bytes INTEGER,
    last_modified INTEGER,
    symbol_count INTEGER DEFAULT 0
);

CREATE TABLE IF NOT EXISTS symbols (
    id TEXT PRIMARY KEY,
    name TEXT,
    kind TEXT,
    file_path TEXT REFERENCES files(path) ON DELETE CASCADE,
    line INTEGER,
    end_line INTEGER,
    signature TEXT,
    doc_comment TEXT,
    parent_id TEXT,
    rank REAL DEFAULT 0.0
);

CREATE TABLE IF NOT EXISTS edges (
    from_id TEXT REFERENCES symbols(id) ON DELETE CASCADE,
    to_id TEXT REFERENCES symbols(id) ON DELETE CASCADE,
    kind TEXT,
    PRIMARY KEY (from_id, to_id, kind)
);

CREATE TABLE IF NOT EXISTS tasks (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    name TEXT,
    description TEXT NOT NULL,
    status TEXT DEFAULT 'created',
    session_id TEXT,
    classification TEXT,
    plan TEXT,
    source_repo TEXT,
    source_task_id INTEGER,
    created_by TEXT,
    acceptance TEXT,
    result TEXT,
    branch TEXT,
    tokens_used INTEGER DEFAULT 0,
    tools_used_json TEXT,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_symbols_file ON symbols(file_path);
CREATE INDEX IF NOT EXISTS idx_symbols_name ON symbols(name);
CREATE INDEX IF NOT EXISTS idx_symbols_kind ON symbols(kind);
CREATE INDEX IF NOT EXISTS idx_edges_from ON edges(from_id);
CREATE INDEX IF NOT EXISTS idx_edges_to ON edges(to_id);
CREATE INDEX IF NOT EXISTS idx_tasks_status ON tasks(status);

CREATE TABLE IF NOT EXISTS meta (
    key TEXT PRIMARY KEY,
    value TEXT
);

-- task_steps: per-step execution state for step-level task execution (#7).
-- Materialized from tasks.plan_json when a task is approved. Each row is
-- one discrete execution unit — the executor shells out once per step,
-- per-step retry, per-step commit. See internal/tasks/steps.go.
CREATE TABLE IF NOT EXISTS task_steps (
    task_id         INTEGER NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    seq             INTEGER NOT NULL,
    verb            TEXT NOT NULL,
    target_file     TEXT,
    symbols_json    TEXT,
    acceptance      TEXT,
    depends_on_json TEXT,
    description     TEXT,
    status          TEXT NOT NULL DEFAULT 'pending',
    result          TEXT,
    session_id      TEXT,
    model_override  TEXT,
    tokens_used     INTEGER DEFAULT 0,
    duration_ms     INTEGER DEFAULT 0,
    retry_count     INTEGER DEFAULT 0,
    commit_sha      TEXT,
    created_at      DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at      DATETIME DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (task_id, seq)
);
CREATE INDEX IF NOT EXISTS idx_task_steps_status ON task_steps(task_id, status);
`
	if _, err := s.db.Exec(schema); err != nil {
		return fmt.Errorf("exec schema: %w", err)
	}

	// FTS5 tables — CREATE VIRTUAL TABLE doesn't support IF NOT EXISTS in all builds,
	// so we check first.
	fts := []struct {
		table string
		ddl   string
	}{
		{"symbols_fts", `CREATE VIRTUAL TABLE symbols_fts USING fts5(symbol_id, name, signature, doc_comment)`},
		{"tasks_fts", `CREATE VIRTUAL TABLE tasks_fts USING fts5(task_id, description, acceptance)`},
	}
	for _, f := range fts {
		var n int
		err := s.db.QueryRow("SELECT count(*) FROM sqlite_master WHERE type='table' AND name=?", f.table).Scan(&n)
		if err != nil {
			return fmt.Errorf("check fts table %s: %w", f.table, err)
		}
		if n == 0 {
			if _, err := s.db.Exec(f.ddl); err != nil {
				return fmt.Errorf("create fts table %s: %w", f.table, err)
			}
		}
	}

	// Lightweight migrations for existing DBs. Each column add is idempotent
	// via a has-column check so re-opening an already-migrated DB is a no-op.
	if err := s.ensureColumn("tasks", "tokens_used", "INTEGER DEFAULT 0"); err != nil {
		return err
	}
	if err := s.ensureColumn("tasks", "tools_used_json", "TEXT"); err != nil {
		return err
	}
	// Structured plan steps. Stored as a JSON array of PlanStep objects.
	// Keeps the pipeline's structured output so the executor prompt can
	// render steps with verbs, target files, and acceptance criteria
	// instead of reparsing the prose `plan` column.
	if err := s.ensureColumn("tasks", "plan_json", "TEXT"); err != nil {
		return err
	}
	// Approval watchdog flag. Set to 1 when the daemon's capture-pane
	// loop detects the executing LLM paused on a confirmation prompt.
	// Cleared when the prompt disappears or the task leaves executing.
	if err := s.ensureColumn("tasks", "needs_input", "INTEGER DEFAULT 0"); err != nil {
		return err
	}

	// FTS5 sync triggers. Before this, tasks_fts was populated manually
	// from Go code inside Create/Delete — which meant any new write path
	// (cross-repo import, migrations, direct SQL) could silently drift
	// FTS out of sync with tasks. Triggers close that class of bug.
	//
	// Idempotent via CREATE TRIGGER IF NOT EXISTS. The triggers replace
	// the manual INSERT/DELETE calls in internal/tasks/manager.go.
	triggers := []string{
		`CREATE TRIGGER IF NOT EXISTS tasks_ai AFTER INSERT ON tasks BEGIN
			INSERT INTO tasks_fts(task_id, description, acceptance)
			VALUES (new.id, new.description, COALESCE(new.acceptance, ''));
		END`,
		`CREATE TRIGGER IF NOT EXISTS tasks_ad AFTER DELETE ON tasks BEGIN
			DELETE FROM tasks_fts WHERE task_id = old.id;
		END`,
		`CREATE TRIGGER IF NOT EXISTS tasks_au AFTER UPDATE OF description, acceptance ON tasks BEGIN
			DELETE FROM tasks_fts WHERE task_id = old.id;
			INSERT INTO tasks_fts(task_id, description, acceptance)
			VALUES (new.id, new.description, COALESCE(new.acceptance, ''));
		END`,
	}
	for _, ddl := range triggers {
		if _, err := s.db.Exec(ddl); err != nil {
			return fmt.Errorf("create fts trigger: %w", err)
		}
	}

	return nil
}

// ensureColumn is a tiny idempotent migration helper: if the column does
// not exist on the given table, ALTER TABLE ... ADD COLUMN. SQLite does
// not natively support "ADD COLUMN IF NOT EXISTS".
func (s *Store) ensureColumn(table, column, typeDecl string) error {
	rows, err := s.db.Query("PRAGMA table_info(" + table + ")")
	if err != nil {
		return fmt.Errorf("pragma table_info %s: %w", table, err)
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var name, colType string
		var notnull, pk int
		var dflt sql.NullString
		if err := rows.Scan(&cid, &name, &colType, &notnull, &dflt, &pk); err != nil {
			return err
		}
		if name == column {
			return nil // already present
		}
	}
	_, err = s.db.Exec(fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s %s", table, column, typeDecl))
	if err != nil {
		return fmt.Errorf("add column %s.%s: %w", table, column, err)
	}
	return nil
}

// File represents a row in the files table.
type File struct {
	Path         string
	ContentHash  []byte
	Language     string
	SizeBytes    int64
	LastModified int64
	SymbolCount  int
}

// Symbol represents a row in the symbols table.
type Symbol struct {
	ID         string
	Name       string
	Kind       string
	FilePath   string
	Line       int
	EndLine    int
	Signature  string
	DocComment string
	ParentID   string
	Rank       float64
}

// Edge represents a row in the edges table.
type Edge struct {
	FromID string
	ToID   string
	Kind   string
}

// UpsertFile inserts or updates a file record.
func (s *Store) UpsertFile(f *File) error {
	stmt := s.stmtUpsertFile
	if stmt == nil {
		return s.upsertFileFallback(f)
	}
	_, err := stmt.Exec(f.Path, f.ContentHash, f.Language, f.SizeBytes, f.LastModified, f.SymbolCount)
	if err != nil {
		return fmt.Errorf("upsert file %s: %w", f.Path, err)
	}
	return nil
}

func (s *Store) upsertFileFallback(f *File) error {
	_, err := s.db.Exec(`INSERT INTO files (path, content_hash, language, size_bytes, last_modified, symbol_count)
		VALUES (?, ?, ?, ?, ?, ?) ON CONFLICT(path) DO UPDATE SET
		content_hash=excluded.content_hash, language=excluded.language, size_bytes=excluded.size_bytes,
		last_modified=excluded.last_modified, symbol_count=excluded.symbol_count`,
		f.Path, f.ContentHash, f.Language, f.SizeBytes, f.LastModified, f.SymbolCount)
	return err
}

// GetFile returns a file record by path, or nil if not found.
func (s *Store) GetFile(path string) (*File, error) {
	f := &File{}
	err := s.db.QueryRow("SELECT path, content_hash, language, size_bytes, last_modified, symbol_count FROM files WHERE path=?", path).
		Scan(&f.Path, &f.ContentHash, &f.Language, &f.SizeBytes, &f.LastModified, &f.SymbolCount)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get file %s: %w", path, err)
	}
	return f, nil
}

// DeleteFile removes a file and its symbols/edges (via CASCADE).
func (s *Store) DeleteFile(path string) error {
	_, err := s.db.Exec("DELETE FROM files WHERE path=?", path)
	if err != nil {
		return fmt.Errorf("delete file %s: %w", path, err)
	}
	return nil
}

// DeleteSymbolsByFile removes all symbols for a file.
func (s *Store) DeleteSymbolsByFile(filePath string) error {
	// Delete edges referencing these symbols first
	_, err := s.db.Exec(`DELETE FROM edges WHERE from_id IN (SELECT id FROM symbols WHERE file_path=?) OR to_id IN (SELECT id FROM symbols WHERE file_path=?)`, filePath, filePath)
	if err != nil {
		return fmt.Errorf("delete edges for file %s: %w", filePath, err)
	}
	// Delete from FTS
	_, err = s.db.Exec(`DELETE FROM symbols_fts WHERE symbol_id IN (SELECT id FROM symbols WHERE file_path=?)`, filePath)
	if err != nil {
		return fmt.Errorf("delete fts for file %s: %w", filePath, err)
	}
	_, err = s.db.Exec("DELETE FROM symbols WHERE file_path=?", filePath)
	if err != nil {
		return fmt.Errorf("delete symbols for file %s: %w", filePath, err)
	}
	return nil
}

// InsertSymbol adds a symbol and its FTS entry. Uses prepared statements when available.
func (s *Store) InsertSymbol(sym *Symbol) error {
	// Delete old FTS row first
	if s.stmtDeleteFTS != nil {
		s.stmtDeleteFTS.Exec(sym.ID)
	} else {
		s.db.Exec(`DELETE FROM symbols_fts WHERE symbol_id=?`, sym.ID)
	}

	if s.stmtInsertSymbol != nil {
		_, err := s.stmtInsertSymbol.Exec(sym.ID, sym.Name, sym.Kind, sym.FilePath, sym.Line, sym.EndLine, sym.Signature, sym.DocComment, sym.ParentID, sym.Rank)
		if err != nil {
			return fmt.Errorf("insert symbol %s: %w", sym.ID, err)
		}
	} else {
		_, err := s.db.Exec(`INSERT OR REPLACE INTO symbols (id, name, kind, file_path, line, end_line, signature, doc_comment, parent_id, rank) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			sym.ID, sym.Name, sym.Kind, sym.FilePath, sym.Line, sym.EndLine, sym.Signature, sym.DocComment, sym.ParentID, sym.Rank)
		if err != nil {
			return fmt.Errorf("insert symbol %s: %w", sym.ID, err)
		}
	}

	if s.stmtInsertFTS != nil {
		s.stmtInsertFTS.Exec(sym.ID, sym.Name, sym.Signature, sym.DocComment)
	} else {
		s.db.Exec(`INSERT INTO symbols_fts (symbol_id, name, signature, doc_comment) VALUES (?, ?, ?, ?)`,
			sym.ID, sym.Name, sym.Signature, sym.DocComment)
	}
	return nil
}

// InsertEdge adds a call/reference edge.
func (s *Store) InsertEdge(e *Edge) error {
	if s.stmtInsertEdge != nil {
		_, err := s.stmtInsertEdge.Exec(e.FromID, e.ToID, e.Kind)
		return err
	}
	_, err := s.db.Exec("INSERT OR IGNORE INTO edges (from_id, to_id, kind) VALUES (?, ?, ?)", e.FromID, e.ToID, e.Kind)
	return err
}

// SearchSymbols performs an FTS5 search over symbols.
// Sanitizes the query for FTS5 syntax — wraps in quotes if it contains special chars.
func (s *Store) SearchSymbols(query string, limit int) ([]*Symbol, error) {
	query = sanitizeFTS(query)
	rows, err := s.db.Query(`
		SELECT s.id, s.name, s.kind, s.file_path, s.line, s.end_line, s.signature, s.doc_comment, s.parent_id, s.rank
		FROM symbols s
		JOIN symbols_fts f ON s.id = f.symbol_id
		WHERE symbols_fts MATCH ?
		ORDER BY f.rank
		LIMIT ?`, query, limit)
	if err != nil {
		return nil, fmt.Errorf("search symbols: %w", err)
	}
	defer rows.Close()

	var syms []*Symbol
	for rows.Next() {
		sym := &Symbol{}
		if err := rows.Scan(&sym.ID, &sym.Name, &sym.Kind, &sym.FilePath, &sym.Line, &sym.EndLine,
			&sym.Signature, &sym.DocComment, &sym.ParentID, &sym.Rank); err != nil {
			return nil, fmt.Errorf("scan symbol: %w", err)
		}
		syms = append(syms, sym)
	}
	return syms, rows.Err()
}

// AllSymbols returns all symbols (for graph building).
func (s *Store) AllSymbols() ([]*Symbol, error) {
	rows, err := s.db.Query("SELECT id, name, kind, file_path, line, end_line, signature, doc_comment, parent_id, rank FROM symbols")
	if err != nil {
		return nil, fmt.Errorf("all symbols: %w", err)
	}
	defer rows.Close()

	var syms []*Symbol
	for rows.Next() {
		sym := &Symbol{}
		if err := rows.Scan(&sym.ID, &sym.Name, &sym.Kind, &sym.FilePath, &sym.Line, &sym.EndLine,
			&sym.Signature, &sym.DocComment, &sym.ParentID, &sym.Rank); err != nil {
			return nil, fmt.Errorf("scan symbol: %w", err)
		}
		syms = append(syms, sym)
	}
	return syms, rows.Err()
}

// TopSymbols returns the top N symbols by rank (for context building).
func (s *Store) TopSymbols(limit int) ([]*Symbol, error) {
	rows, err := s.db.Query(`
		SELECT id, name, kind, file_path, line, end_line, signature, doc_comment, parent_id, rank
		FROM symbols
		WHERE kind IN ('function','method','struct','interface','class','type')
		ORDER BY rank DESC
		LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("top symbols: %w", err)
	}
	defer rows.Close()

	var syms []*Symbol
	for rows.Next() {
		sym := &Symbol{}
		if err := rows.Scan(&sym.ID, &sym.Name, &sym.Kind, &sym.FilePath, &sym.Line, &sym.EndLine,
			&sym.Signature, &sym.DocComment, &sym.ParentID, &sym.Rank); err != nil {
			return nil, fmt.Errorf("scan symbol: %w", err)
		}
		syms = append(syms, sym)
	}
	return syms, rows.Err()
}

// AllEdges returns all edges (for graph building).
func (s *Store) AllEdges() ([]*Edge, error) {
	rows, err := s.db.Query("SELECT from_id, to_id, kind FROM edges")
	if err != nil {
		return nil, fmt.Errorf("all edges: %w", err)
	}
	defer rows.Close()

	var edges []*Edge
	for rows.Next() {
		e := &Edge{}
		if err := rows.Scan(&e.FromID, &e.ToID, &e.Kind); err != nil {
			return nil, fmt.Errorf("scan edge: %w", err)
		}
		edges = append(edges, e)
	}
	return edges, rows.Err()
}

// UpdateSymbolRank sets the rank for a symbol.
func (s *Store) UpdateSymbolRank(id string, rank float64) error {
	if s.stmtUpdateRank != nil {
		_, err := s.stmtUpdateRank.Exec(rank, id)
		return err
	}
	_, err := s.db.Exec("UPDATE symbols SET rank=? WHERE id=?", rank, id)
	return err
}

// FileCount returns number of indexed files.
func (s *Store) FileCount() (int, error) {
	var n int
	err := s.db.QueryRow("SELECT count(*) FROM files").Scan(&n)
	return n, err
}

// SymbolCount returns number of indexed symbols.
func (s *Store) SymbolCount() (int, error) {
	var n int
	err := s.db.QueryRow("SELECT count(*) FROM symbols").Scan(&n)
	return n, err
}

// AllFiles returns all file records.
func (s *Store) AllFiles() ([]*File, error) {
	rows, err := s.db.Query("SELECT path, content_hash, language, size_bytes, last_modified, symbol_count FROM files")
	if err != nil {
		return nil, fmt.Errorf("all files: %w", err)
	}
	defer rows.Close()

	var files []*File
	for rows.Next() {
		f := &File{}
		if err := rows.Scan(&f.Path, &f.ContentHash, &f.Language, &f.SizeBytes, &f.LastModified, &f.SymbolCount); err != nil {
			return nil, fmt.Errorf("scan file: %w", err)
		}
		files = append(files, f)
	}
	return files, rows.Err()
}

// SymbolsByFile returns all symbols in a given file.
func (s *Store) SymbolsByFile(filePath string) ([]*Symbol, error) {
	rows, err := s.db.Query(`
		SELECT id, name, kind, file_path, line, end_line, signature, doc_comment, parent_id, rank
		FROM symbols WHERE file_path=? ORDER BY line`, filePath)
	if err != nil {
		return nil, fmt.Errorf("symbols by file: %w", err)
	}
	defer rows.Close()

	var syms []*Symbol
	for rows.Next() {
		sym := &Symbol{}
		if err := rows.Scan(&sym.ID, &sym.Name, &sym.Kind, &sym.FilePath, &sym.Line, &sym.EndLine,
			&sym.Signature, &sym.DocComment, &sym.ParentID, &sym.Rank); err != nil {
			return nil, fmt.Errorf("scan symbol: %w", err)
		}
		syms = append(syms, sym)
	}
	return syms, rows.Err()
}

// Callers returns symbols that call the given symbol.
func (s *Store) Callers(symbolID string) ([]*Symbol, error) {
	rows, err := s.db.Query(`
		SELECT s.id, s.name, s.kind, s.file_path, s.line, s.end_line, s.signature, s.doc_comment, s.parent_id, s.rank
		FROM symbols s JOIN edges e ON s.id = e.from_id
		WHERE e.to_id=? AND e.kind='call'`, symbolID)
	if err != nil {
		return nil, fmt.Errorf("callers: %w", err)
	}
	defer rows.Close()

	var syms []*Symbol
	for rows.Next() {
		sym := &Symbol{}
		if err := rows.Scan(&sym.ID, &sym.Name, &sym.Kind, &sym.FilePath, &sym.Line, &sym.EndLine,
			&sym.Signature, &sym.DocComment, &sym.ParentID, &sym.Rank); err != nil {
			return nil, fmt.Errorf("scan caller: %w", err)
		}
		syms = append(syms, sym)
	}
	return syms, rows.Err()
}

// Callees returns symbols called by the given symbol.
func (s *Store) Callees(symbolID string) ([]*Symbol, error) {
	rows, err := s.db.Query(`
		SELECT s.id, s.name, s.kind, s.file_path, s.line, s.end_line, s.signature, s.doc_comment, s.parent_id, s.rank
		FROM symbols s JOIN edges e ON s.id = e.to_id
		WHERE e.from_id=? AND e.kind='call'`, symbolID)
	if err != nil {
		return nil, fmt.Errorf("callees: %w", err)
	}
	defer rows.Close()

	var syms []*Symbol
	for rows.Next() {
		sym := &Symbol{}
		if err := rows.Scan(&sym.ID, &sym.Name, &sym.Kind, &sym.FilePath, &sym.Line, &sym.EndLine,
			&sym.Signature, &sym.DocComment, &sym.ParentID, &sym.Rank); err != nil {
			return nil, fmt.Errorf("scan callee: %w", err)
		}
		syms = append(syms, sym)
	}
	return syms, rows.Err()
}
