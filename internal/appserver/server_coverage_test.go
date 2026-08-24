package appserver

import (
	"context"
	"testing"

	"primeradiant.com/evener/appwire"
)

func TestServerBroadcastAllEvictsSlowConsumer(t *testing.T) {
	var logged []string
	server := NewServer(ServerConfig{
		Logf: func(format string, args ...any) {
			logged = append(logged, format)
		},
	})
	conn := server.NewConnection("conn-1")
	server.registerConnection(conn)

	// Don't drain the send channel — fill it to capacity, then BroadcastAll
	// should evict the connection.
	for range appwire.NotificationBufferCap {
		conn.send <- appwire.Message{}
	}

	server.BroadcastAll("test", nil)

	// The connection should be evicted — the logf callback should have fired.
	if len(logged) == 0 {
		t.Fatal("evictSlowConsumer should have logged via logf")
	}
}

func TestServerBroadcastAllNormalDelivery(t *testing.T) {
	server := NewServer(ServerConfig{})
	conn := server.NewConnection("conn-1")
	server.registerConnection(conn)

	server.BroadcastAll("test", map[string]string{"key": "value"})

	// The message should be in the connection's send channel.
	select {
	case msg := <-conn.send:
		if msg.Notification == nil {
			t.Fatal("notification should not be nil")
		}
	default:
		t.Fatal("message should have been delivered")
	}
}

func TestServerBroadcastAllNoConnections(t *testing.T) {
	server := NewServer(ServerConfig{})
	// Should not panic with no connections.
	server.BroadcastAll("test", nil)
}

func TestServerLogfNoCallback(t *testing.T) {
	server := NewServer(ServerConfig{})
	// Should not panic when Logf is nil.
	server.logf("test %s", "arg")
}

func TestServerLogfWithCallback(t *testing.T) {
	var logged string
	server := NewServer(ServerConfig{
		Logf: func(format string, args ...any) {
			logged = format
		},
	})
	server.logf("test %s", "arg")
	if logged != "test %s" {
		t.Fatalf("expected 'test %%s', got %q", logged)
	}
}

func TestServerEvictSlowConsumerUnregisters(t *testing.T) {
	server := NewServer(ServerConfig{})
	conn := server.NewConnection("conn-1")
	server.registerConnection(conn)

	server.evictSlowConsumer(conn, "test")

	// Connection should be unregistered.
	server.mu.RLock()
	_, exists := server.conns["conn-1"]
	server.mu.RUnlock()
	if exists {
		t.Fatal("connection should be unregistered after eviction")
	}
}

func TestServerUnregisterConnectionNil(t *testing.T) {
	server := NewServer(ServerConfig{})
	// Should not panic on nil connection.
	server.unregisterConnection(nil)
}

func TestServerUnregisterConnectionAlreadyReplaced(t *testing.T) {
	server := NewServer(ServerConfig{})
	stale := server.NewConnection("conn-1")
	replacement := server.NewConnection("conn-1")
	server.registerConnection(stale)
	server.registerConnection(replacement)

	// Unregister the stale one — it should be a no-op since it's been replaced.
	server.unregisterConnection(stale)

	// The replacement should still be there.
	server.mu.RLock()
	conn := server.conns["conn-1"]
	server.mu.RUnlock()
	if conn != replacement {
		t.Fatal("replacement connection should still be registered")
	}
}

func TestServerInitializeWithWrongProtocol(t *testing.T) {
	server := NewServer(ServerConfig{ServerName: "test"})
	_, err := server.initialize(context.TODO(), appwire.InitializeParams{ProtocolVersion: "wrong"})
	if err == nil {
		t.Fatal("wrong protocol version should return error")
	}
}

func TestServerInitializeWithCorrectProtocol(t *testing.T) {
	server := NewServer(ServerConfig{ServerName: "test", Version: "1.0", SourceID: "src1"})
	resp, err := server.initialize(context.TODO(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion})
	if err != nil {
		t.Fatalf("correct protocol should not error: %v", err)
	}
	if resp.ServerInfo.Name != "test" {
		t.Fatalf("expected server name 'test', got %q", resp.ServerInfo.Name)
	}
}

func TestServerInitializeAdapterNative(t *testing.T) {
	server := NewServer(ServerConfig{AdapterNativeInitialize: true})
	resp, err := server.initialize(context.TODO(), appwire.InitializeParams{ProtocolVersion: "anything"})
	if err != nil {
		t.Fatalf("adapter native initialize should not error: %v", err)
	}
	if resp.ProtocolVersion != "" {
		t.Fatalf("adapter native should return empty protocol version, got %q", resp.ProtocolVersion)
	}
}

func TestServerSubscriberCount(t *testing.T) {
	server := NewServer(ServerConfig{})
	conn := server.NewConnection("conn-1")
	server.registerConnection(conn)
	conn.Subscribe("th_1")
	if count := server.SubscriberCount("th_1"); count != 1 {
		t.Fatalf("expected count 1, got %d", count)
	}
}
