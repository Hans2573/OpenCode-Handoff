package store

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
)

// SnapshotSQLite includes committed WAL transactions without copying a live
// database's files independently. The destination is published only after a
// successful integrity check, and an existing file is never overwritten.
func SnapshotSQLite(ctx context.Context, source, destination string) error {
	abs, err := filepath.Abs(source)
	if err != nil {
		return err
	}
	path := filepath.ToSlash(abs)
	if len(path) > 1 && path[1] == ':' {
		path = "/" + path
	}
	u := url.URL{Scheme: "file", Path: path, RawQuery: "mode=ro"}
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if err := db.PingContext(ctx); err != nil {
		return fmt.Errorf("open snapshot source: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
		return err
	}
	temp, err := os.CreateTemp(filepath.Dir(destination), ".snapshot-*.db")
	if err != nil {
		return err
	}
	name := temp.Name()
	if err := temp.Close(); err != nil {
		return err
	}
	defer os.Remove(name)
	if _, err := db.ExecContext(ctx, "VACUUM INTO ?", name); err != nil {
		return fmt.Errorf("snapshot SQLite: %w", err)
	}
	check, err := sql.Open("sqlite", name)
	if err != nil {
		return err
	}
	var result string
	err = check.QueryRowContext(ctx, "PRAGMA integrity_check").Scan(&result)
	closeErr := check.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if result != "ok" {
		return fmt.Errorf("snapshot integrity check failed: %s", result)
	}
	file, err := os.OpenFile(name, os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	err = file.Sync()
	closeErr = file.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	// Same-directory hard link gives atomic, exclusive publication on Windows
	// and Unix; a concurrent startup cannot replace an already imported store.
	if err := os.Link(name, destination); err != nil {
		return fmt.Errorf("publish SQLite snapshot: %w", err)
	}
	return nil
}
