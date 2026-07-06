package streaming

import (
	"errors"
	"fmt"
	"net"
	"os"
	"syscall"
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
		{"ws close 1000 normal", &websocket.CloseError{Code: websocket.CloseNormalClosure}, true},
		{"ws close 1001 going away", &websocket.CloseError{Code: websocket.CloseGoingAway}, true},
		{"ws close 1005 no status", &websocket.CloseError{Code: websocket.CloseNoStatusReceived}, true},
		{"ws close 1006 abnormal closure", &websocket.CloseError{Code: websocket.CloseAbnormalClosure}, true},
		{"wrapped ws close 1006", fmt.Errorf("read: %w", &websocket.CloseError{Code: websocket.CloseAbnormalClosure}), true},
		{"ws close 1011 internal error stays unexpected", &websocket.CloseError{Code: websocket.CloseInternalServerErr}, false},
		{
			"read tcp connection reset by peer",
			&net.OpError{Op: "read", Net: "tcp", Err: os.NewSyscallError("read", syscall.ECONNRESET)},
			true,
		},
		{"wrapped connection reset by peer", fmt.Errorf("wrap: %w", syscall.ECONNRESET), true},
		{"unrelated syscall error stays unexpected", &net.OpError{Op: "read", Net: "tcp", Err: os.NewSyscallError("read", syscall.ECONNREFUSED)}, false},
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
