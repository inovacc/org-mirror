package main

import "testing"

const expectedDefaultMirrorRoot = `C:\Users\dyamm\Downloads\mirror\orgs`

func TestNewSyncCommandConfiguresRequiredFlags(t *testing.T) {
	command := newSyncCommand()
	if command.Use != "sync <organization>" {
		t.Fatalf("unexpected command use: %q", command.Use)
	}
	if command.Flags().Lookup("root") == nil || command.Flags().Lookup("dry-run") == nil {
		t.Fatal("sync command must expose root and dry-run flags")
	}
	root, err := command.Flags().GetString("root")
	if err != nil {
		t.Fatalf("read root flag: %v", err)
	}
	if root != expectedDefaultMirrorRoot {
		t.Fatalf("default root = %q, want %q", root, expectedDefaultMirrorRoot)
	}
}
