package ipc

import (
	"context"
	"encoding/json"
	"net"
	"path/filepath"
	"testing"
	"time"

	"live-recorder/internal/app"
	"live-recorder/internal/domain"
)

func TestListenDomainEvents_NoClients_DoesNotBlock(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	server := NewUnixSocketServer(ctx, nil, filepath.Join(t.TempDir(), "ipc.sock"))
	eventCh := make(chan app.Event)

	go server.ListenDomainEvents(eventCh)

	// Send an event when no client is connected
	done := make(chan struct{})
	go func() {
		eventCh <- app.UserStatusChangedEvent{
			UserID: "user_1",
			Alias:  "Test",
			Current: domain.UserStatus{
				ID:             "user_1",
				LiveState:      domain.LiveStateLive,
				RecordingState: domain.RecordingStateIdle,
			},
			Previous: domain.UserStatus{
				ID:        "user_1",
				LiveState: domain.LiveStateOffline,
			},
		}
		close(done)
	}()

	select {
	case <-done:
		// Succeeded: data was discarded without blocking
	case <-time.After(500 * time.Millisecond):
		t.Fatal("ListenDomainEvents blocked when no clients were connected")
	}
}

func TestBroadcast_DeliversToConnectedClients(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	server := NewUnixSocketServer(ctx, nil, filepath.Join(t.TempDir(), "ipc.sock"))

	clientConn1, serverConn1 := net.Pipe()
	defer clientConn1.Close()
	defer serverConn1.Close()

	clientConn2, serverConn2 := net.Pipe()
	defer clientConn2.Close()
	defer serverConn2.Close()

	ch1 := server.registerClient(serverConn1)
	ch2 := server.registerClient(serverConn2)

	go server.sendAppEventsToClientSocket(serverConn1, ch1)
	go server.sendAppEventsToClientSocket(serverConn2, ch2)

	eventCh := make(chan app.Event)
	go server.ListenDomainEvents(eventCh)

	eventCh <- app.RecordingStartedEvent{
		Session: domain.RecordingSessionInfo{
			UserID: "user_2",
			Alias:  "TestUser",
		},
	}

	buf1 := make([]byte, 1024)
	buf2 := make([]byte, 1024)

	n1, err := clientConn1.Read(buf1)
	if err != nil {
		t.Fatalf("client 1 read error: %v", err)
	}

	n2, err := clientConn2.Read(buf2)
	if err != nil {
		t.Fatalf("client 2 read error: %v", err)
	}

	var status1, status2 domain.GlobalStatus
	if err := json.Unmarshal(buf1[:n1], &status1); err != nil {
		t.Fatalf("client 1 unmarshal error: %v", err)
	}
	if err := json.Unmarshal(buf2[:n2], &status2); err != nil {
		t.Fatalf("client 2 unmarshal error: %v", err)
	}

	if len(status1.ActiveRecordings) != 1 || status1.ActiveRecordings[0].UserID != "user_2" {
		t.Errorf("unexpected status in client 1: %+v", status1)
	}
	if len(status2.ActiveRecordings) != 1 || status2.ActiveRecordings[0].UserID != "user_2" {
		t.Errorf("unexpected status in client 2: %+v", status2)
	}
}

func TestSendAppEventsToClientSocket_ExitsOnClientClose(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	server := NewUnixSocketServer(ctx, nil, filepath.Join(t.TempDir(), "ipc.sock"))

	clientConn, serverConn := net.Pipe()
	defer serverConn.Close()

	ch := server.registerClient(serverConn)

	goroutineDone := make(chan struct{})
	go func() {
		server.sendAppEventsToClientSocket(serverConn, ch)
		close(goroutineDone)
	}()

	// Close client end
	clientConn.Close()

	// Trigger a broadcast so write fails immediately
	server.broadcast(domain.GlobalStatus{})

	select {
	case <-goroutineDone:
		// Succeeded: goroutine exited on write error
	case <-time.After(1 * time.Second):
		t.Fatal("sendAppEventsToClientSocket did not terminate after client connection closed")
	}

	// Verify client is unregistered
	server.mu.RLock()
	clientCount := len(server.clients)
	server.mu.RUnlock()
	if clientCount != 0 {
		t.Errorf("expected 0 clients after disconnect, got %d", clientCount)
	}
}
