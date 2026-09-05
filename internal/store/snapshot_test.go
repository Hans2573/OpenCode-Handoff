package store

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/Hans2573/OpenCode-Handoff/internal/domain"
)

func TestSnapshotIncludesWALAndNeverOverwritesDestination(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "live.db")
	db, err := OpenSQLite(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.db.Exec("PRAGMA wal_autocheckpoint=0"); err != nil {
		t.Fatal(err)
	}
	if err := db.SetSetting(ctx, "committed-in-wal", "preserve"); err != nil {
		t.Fatal(err)
	}
	wal, err := os.Stat(path + "-wal")
	if err != nil || wal.Size() == 0 {
		t.Fatalf("expected live WAL: %v", err)
	}
	dest := filepath.Join(t.TempDir(), "snapshot.db")
	if err := SnapshotSQLite(ctx, path, dest); err != nil {
		t.Fatal(err)
	}
	copy, err := OpenSQLite(ctx, dest)
	if err != nil {
		t.Fatal(err)
	}
	value, err := copy.GetSetting(ctx, "committed-in-wal")
	copy.Close()
	if err != nil || value != "preserve" {
		t.Fatalf("WAL record lost: %q, %v", value, err)
	}
	if err := db.SetSetting(ctx, "committed-in-wal", "changed"); err != nil {
		t.Fatal(err)
	}
	if err := SnapshotSQLite(ctx, path, dest); err == nil {
		t.Fatal("snapshot overwrote destination")
	}
	copy, err = OpenSQLite(ctx, dest)
	if err != nil {
		t.Fatal(err)
	}
	defer copy.Close()
	value, err = copy.GetSetting(ctx, "committed-in-wal")
	if err != nil || value != "preserve" {
		t.Fatalf("existing snapshot changed: %q, %v", value, err)
	}
}

func TestPersistenceAfterAbruptProcessExit(t *testing.T) {
	path := os.Getenv("HANDOFF_PERSISTENCE_TEST_DB")
	ctx := context.Background()
	if path != "" {
		db, err := OpenSQLite(ctx, path)
		if err != nil {
			t.Fatal(err)
		}
		if err := db.EnsureDesktopDefaults(ctx, "http://127.0.0.1:4096"); err != nil {
			t.Fatal(err)
		}
		if err := db.SyncProjects(ctx, []domain.AgentProject{{ID: "restart", AgentID: DefaultAgentID, Name: "Restart", Directory: "/restart"}}); err != nil {
			t.Fatal(err)
		}
		if err := db.SetProjectRoute(ctx, "restart", DefaultChannelID, true); err != nil {
			t.Fatal(err)
		}
		if err := db.SetSetting(ctx, "restart-value", "persisted"); err != nil {
			t.Fatal(err)
		}
		// Deliberately leave the connection open, like terminating wails3 dev.
		os.Exit(0)
	}
	path = filepath.Join(t.TempDir(), "restart.db")
	cmd := exec.Command(os.Args[0], "-test.run=^TestPersistenceAfterAbruptProcessExit$")
	cmd.Env = append(os.Environ(), "HANDOFF_PERSISTENCE_TEST_DB="+path)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("writer: %v\n%s", err, output)
	}
	for i := 0; i < 2; i++ {
		db, err := OpenSQLite(ctx, path)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.EnsureProjectRoutesOptIn(ctx); err != nil {
			t.Fatal(err)
		}
		routes, err := db.ListProjectRoutes(ctx)
		if err != nil || len(routes) != 1 || !routes[0].RouteEnabled {
			t.Fatalf("restart %d lost route: %+v, %v", i, routes, err)
		}
		value, err := db.GetSetting(ctx, "restart-value")
		if err != nil || value != "persisted" {
			t.Fatalf("restart %d lost setting: %q, %v", i, value, err)
		}
		db.Close()
	}
}
