package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDefaultSyncPathsUseCurrentUsersHome(t *testing.T) {
	home := filepath.Join("home", "another-user")
	root, database := defaultSyncPaths(home)

	if want := filepath.Join(home, "Downloads", "mirror", "orgs"); root != want {
		t.Fatalf("default root = %q, want %q", root, want)
	}
	if want := filepath.Join(home, "Downloads", "mirror", "database.db"); database != want {
		t.Fatalf("default database = %q, want %q", database, want)
	}
}

func TestPrepareSyncStorageCreatesDatabaseAndOrganizationsDirectory(t *testing.T) {
	root, databasePath := defaultSyncPaths(t.TempDir())

	database, err := prepareSyncStorage(root, databasePath)
	if err != nil {
		t.Fatalf("prepare sync storage: %v", err)
	}
	if err := database.Close(); err != nil {
		t.Fatalf("close sync database: %v", err)
	}
	if info, err := os.Stat(root); err != nil || !info.IsDir() {
		t.Fatalf("organizations directory was not created at %q: %v", root, err)
	}
	if info, err := os.Stat(databasePath); err != nil || info.IsDir() {
		t.Fatalf("database file was not created at %q: %v", databasePath, err)
	}
}

func TestNewSyncCommandConfiguresRequiredFlags(t *testing.T) {
	command := newSyncCommand()
	if command.Use != "sync <organization>" {
		t.Fatalf("unexpected command use: %q", command.Use)
	}
	if command.Flags().Lookup("root") == nil || command.Flags().Lookup("dry-run") == nil || command.Flags().Lookup("no-tui") == nil {
		t.Fatal("sync command must expose root, dry-run, and no-tui flags")
	}
	root, err := command.Flags().GetString("root")
	if err != nil {
		t.Fatalf("read root flag: %v", err)
	}
	if root == "" {
		t.Fatal("default root must not be empty")
	}
}

func TestSyncCommandRootFlagOverridesDefault(t *testing.T) {
	command := newSyncCommand()
	want := filepath.Join(t.TempDir(), "mirrors")

	if err := command.Flags().Parse([]string{"--root", want}); err != nil {
		t.Fatalf("parse root flag: %v", err)
	}
	got, err := command.Flags().GetString("root")
	if err != nil {
		t.Fatalf("read root flag: %v", err)
	}
	if got != want {
		t.Fatalf("root flag = %q, want %q", got, want)
	}
}
