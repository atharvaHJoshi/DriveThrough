package websocket

import (
	"net/http/httptest"
	"testing"

	"drivethrough/internal/middleware"
)

func TestNegotiateSubprotocolPrefersARealProtocolName(t *testing.T) {
	// The credential entry must not be advertised back as the protocol name; a
	// stable identifier should win when the client offers one.
	if got := negotiateSubprotocol([]string{"bearer.jwt-token", "drivethrough.v1"}); got != "drivethrough.v1" {
		t.Fatalf("expected drivethrough.v1, got %q", got)
	}
	if got := negotiateSubprotocol([]string{"drivethrough.v1", "bearer.jwt-token"}); got != "drivethrough.v1" {
		t.Fatalf("expected drivethrough.v1 regardless of order, got %q", got)
	}
}

func TestNegotiateSubprotocolEchoesCredentialWhenItIsTheOnlyOption(t *testing.T) {
	// RFC 6455 requires the server to select one of the offered protocols, and a
	// browser aborts when none is returned. A client that sent only the
	// credential entry still deserves a successful handshake.
	if got := negotiateSubprotocol([]string{"bearer.jwt-token"}); got != "bearer.jwt-token" {
		t.Fatalf("expected the credential entry to be echoed, got %q", got)
	}
}

func TestNegotiateSubprotocolWithNothingOffered(t *testing.T) {
	if got := negotiateSubprotocol(nil); got != "" {
		t.Fatalf("expected empty, got %q", got)
	}
	if got := negotiateSubprotocol([]string{}); got != "" {
		t.Fatalf("expected empty, got %q", got)
	}
}

func TestOfferedSubprotocolsParsesCommaSeparatedAndRepeatedHeaders(t *testing.T) {
	r := httptest.NewRequest("GET", "/", nil)
	r.Header.Set("Sec-WebSocket-Protocol", "drivethrough.v1, bearer.token-a")
	r.Header.Add("Sec-WebSocket-Protocol", "bearer.token-b")

	got := offeredSubprotocols(r)
	want := []string{"drivethrough.v1", "bearer.token-a", "bearer.token-b"}
	if len(got) != len(want) {
		t.Fatalf("expected %d protocols, got %d: %v", len(want), len(got), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("position %d: expected %q, got %q", i, want[i], got[i])
		}
	}
}

func TestNegotiatedProtocolIsAlwaysOneTheClientOffered(t *testing.T) {
	// The upgrader only echoes a protocol the client actually asked for, so a
	// mismatch here would fail the handshake rather than degrade gracefully.
	r := httptest.NewRequest("GET", "/", nil)
	r.Header.Set("Sec-WebSocket-Protocol", "drivethrough.v1, bearer."+middleware.WebSocketTokenPrefix)

	offered := offeredSubprotocols(r)
	selected := negotiateSubprotocol(offered)
	if selected == "" {
		t.Fatal("nothing selected, the browser would abort")
	}
	found := false
	for _, p := range offered {
		if p == selected {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("selected %q which was not offered %v", selected, offered)
	}
}
