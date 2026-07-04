package streaming

import (
	"errors"
	"fmt"
	"net"
	"testing"

	"github.com/gorilla/websocket"
)

func TestIsExpectedDisconnect(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"close sent", websocket.ErrCloseSent, true},
		{"wrapped close sent", fmt.Errorf("wrap: %w", websocket.ErrCloseSent), true},
		{"closed network connection", net.ErrClosed, true},
		{"wrapped closed network connection", fmt.Errorf("wrap: %w", net.ErrClosed), true},
		{
			"tls closeNotify write failure",
			errors.New("tls: failed to send closeNotify alert (but connection was closed anyway): write tcp 10.0.0.5:8443->10.0.0.2:17638: write: broken pipe"),
			true,
		},
		{"unrelated error", errors.New("unexpected EOF"), false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isExpectedDisconnect(tt.err); got != tt.want {
				t.Errorf("isExpectedDisconnect(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}
