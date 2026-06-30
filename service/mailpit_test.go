package service

import (
	"net"
	"os"
	"strings"
	"testing"
)

func TestLoadLiveMailpitStateRemovesStateWhenPIDIsNotLive(t *testing.T) {
	root := t.TempDir()
	statePath := MailpitStatePath(root, "demo")
	if err := WriteMailpitState(statePath, MailpitRuntimeState{
		EnvironmentName: "demo",
		Version:         "1.30",
		SMTPPort:        1025,
		UIPort:          8025,
	}); err != nil {
		t.Fatalf("WriteMailpitState() error = %v", err)
	}

	state, err := LoadLiveMailpitState(root, "demo", func(string) bool {
		return true
	})
	if err != nil {
		t.Fatalf("LoadLiveMailpitState() error = %v", err)
	}
	if state != nil {
		t.Fatalf("LoadLiveMailpitState() state = %#v, want nil", state)
	}
	if _, err := os.Stat(statePath); !os.IsNotExist(err) {
		t.Fatalf("Stat(statePath) error = %v, want removed stale state", err)
	}
}

func TestStartMailpitServerRejectsOccupiedPort(t *testing.T) {
	listener, err := net.Listen("tcp", MailpitAddress(0))
	if err != nil {
		t.Fatalf("Listen() error = %v", err)
	}
	defer listener.Close()

	tcpAddr, ok := listener.Addr().(*net.TCPAddr)
	if !ok {
		t.Fatalf("listener address = %T, want *net.TCPAddr", listener.Addr())
	}
	_, err = StartMailpitServer(MailpitServerSpec{
		SMTPPort: tcpAddr.Port,
		UIPort:   8025,
		LogPath:  MailpitLogPath(t.TempDir(), "demo"),
	})
	if err == nil {
		t.Fatal("StartMailpitServer() error = nil, want occupied port error")
	}
	if !strings.Contains(err.Error(), "already in use") {
		t.Fatalf("StartMailpitServer() error = %q, want occupied port guidance", err.Error())
	}
}
