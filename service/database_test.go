package service

import (
	"net"
	"os"
	"strings"
	"testing"
)

func TestLoadLiveManagedDatabaseStateRemovesStateWhenPIDIsNotLive(t *testing.T) {
	root := t.TempDir()
	statePath := DatabaseStatePath(root, "demo")
	if err := WriteManagedDatabaseState(statePath, ManagedDatabaseRuntimeState{
		EnvironmentName: "demo",
		Engine:          toolMySQL,
		Version:         "8.4",
		Port:            3306,
	}); err != nil {
		t.Fatalf("WriteManagedDatabaseState() error = %v", err)
	}

	state, err := LoadLiveManagedDatabaseState(root, "demo", func(string) bool {
		return true
	})
	if err != nil {
		t.Fatalf("LoadLiveManagedDatabaseState() error = %v", err)
	}
	if state != nil {
		t.Fatalf("LoadLiveManagedDatabaseState() state = %#v, want nil", state)
	}
	if _, err := os.Stat(statePath); !os.IsNotExist(err) {
		t.Fatalf("Stat(statePath) error = %v, want removed stale state", err)
	}
}

func TestStartDatabaseServerRejectsOccupiedPort(t *testing.T) {
	listener, err := net.Listen("tcp", DatabaseAddress(0))
	if err != nil {
		t.Fatalf("Listen() error = %v", err)
	}
	defer listener.Close()

	tcpAddr, ok := listener.Addr().(*net.TCPAddr)
	if !ok {
		t.Fatalf("listener address = %T, want *net.TCPAddr", listener.Addr())
	}
	_, err = StartDatabaseServer(ManagedDatabaseServerSpec{
		Engine:  toolMySQL,
		Port:    tcpAddr.Port,
		LogPath: DatabaseLogPath(t.TempDir(), "demo"),
	})
	if err == nil {
		t.Fatal("StartDatabaseServer() error = nil, want occupied port error")
	}
	if !strings.Contains(err.Error(), "already in use") {
		t.Fatalf("StartDatabaseServer() error = %q, want occupied port guidance", err.Error())
	}
}
