package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// These tests cover extractBearerToken directly. It is a pure function of the
// request, so the middleware's surrounding HTTP handling needs no stub issuer to
// be exercised here.

func TestExtractBearerTokenPrefersAuthorizationHeader(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/v1/ws/live", nil)
	req.Header.Set("Authorization", "Bearer header-token")
	// A credential also present in the subprotocol header must not win.
	req.Header.Set(WebSocketProtocolTokenHeader, "bearer.subprotocol-token")

	got, err := extractBearerToken(req)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if got != "header-token" {
		t.Fatalf("expected the header token, got %q", got)
	}
}

func TestExtractBearerTokenFromWebSocketSubprotocol(t *testing.T) {
	// This is the browser path: no Authorization header is possible on a
	// WebSocket handshake, so the token rides in the subprotocol header.
	req := httptest.NewRequest(http.MethodGet, "/api/v1/ws/live", nil)
	req.Header.Set(WebSocketProtocolTokenHeader, "drivethrough.v1, bearer.ws-token")

	got, err := extractBearerToken(req)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if got != "ws-token" {
		t.Fatalf("expected ws-token, got %q", got)
	}
}

func TestExtractBearerTokenAcceptsMultipleHeaderLines(t *testing.T) {
	// A client may send the header more than once rather than comma-joining.
	req := httptest.NewRequest(http.MethodGet, "/api/v1/ws/live", nil)
	req.Header.Add(WebSocketProtocolTokenHeader, "drivethrough.v1")
	req.Header.Add(WebSocketProtocolTokenHeader, "bearer.second-line-token")

	got, err := extractBearerToken(req)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if got != "second-line-token" {
		t.Fatalf("expected second-line-token, got %q", got)
	}
}

func TestExtractBearerTokenRejectsEmptyCredential(t *testing.T) {
	cases := map[string]http.Header{
		"nothing set": {},
		"bearer with no value": {
			"Authorization": []string{"Bearer "},
		},
		"wrong scheme": {
			"Authorization": []string{"Basic dXNlcjpwYXNz"},
		},
		"no scheme separator": {
			"Authorization": []string{"header-token"},
		},
		// A malformed Authorization header must fail rather than fall back to the
		// subprotocol channel, otherwise a broken header silently changes which
		// credential is in play.
		"malformed header does not fall back": {
			"Authorization":              []string{"Basic nope"},
			WebSocketProtocolTokenHeader: []string{"bearer.fallback-token"},
		},
		"subprotocol without token": {
			WebSocketProtocolTokenHeader: []string{"drivethrough.v1"},
		},
		"bearer prefix with empty token": {
			WebSocketProtocolTokenHeader: []string{"bearer."},
		},
	}

	for name, headers := range cases {
		t.Run(name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/api/v1/ws/live", nil)
			for k, vs := range headers {
				for _, v := range vs {
					req.Header.Add(k, v)
				}
			}
			if _, err := extractBearerToken(req); err == nil {
				t.Fatal("expected an error, got none")
			}
		})
	}
}

func TestWebSocketTokenPrefixIsDistinct(t *testing.T) {
	// Guards the prefix the frontend depends on: if it changes, browser auth
	// breaks silently at runtime rather than at compile time.
	if WebSocketTokenPrefix != "bearer." {
		t.Fatalf("token prefix is %q; the web client sends %q", WebSocketTokenPrefix, "bearer.")
	}
	if WebSocketProtocolTokenHeader != "Sec-WebSocket-Protocol" {
		t.Fatalf("protocol header is %q", WebSocketProtocolTokenHeader)
	}
}
