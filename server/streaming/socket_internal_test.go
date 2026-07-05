package streaming

import (
	"errors"
	"fmt"
	"net"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	logrus "github.com/teslamotors/fleet-telemetry/logger"
	"github.com/teslamotors/fleet-telemetry/telemetry"
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

func newChunkTestSocketManager() *SocketManager {
	logger, _ := logrus.NoOpLogger()
	return &SocketManager{
		UUID:            "test-uuid",
		requestIdentity: &telemetry.RequestIdentity{DeviceID: "vin-42"},
		logger:          logger,
	}
}

func TestChunkRotationOnEventCount(t *testing.T) {
	sm := newChunkTestSocketManager()
	sm.startChunk()

	if sm.chunkSpan == nil {
		t.Fatal("startChunk did not create a chunk span")
	}
	if sm.chunkIndex != 0 {
		t.Fatalf("chunkIndex = %d, want 0", sm.chunkIndex)
	}
	// The logger must be re-pointed at the chunk span immediately.
	if sm.log() == sm.logger {
		t.Error("log() still returns the base logger after startChunk; expected chunk-scoped logger")
	}

	// Filling the event budget must trigger a rotation on the next check.
	for i := 0; i < chunkMaxEvents; i++ {
		sm.addChunkEvent("message_received")
	}
	if sm.chunkEvents != chunkMaxEvents {
		t.Fatalf("chunkEvents = %d, want %d", sm.chunkEvents, chunkMaxEvents)
	}

	sm.rotateChunkIfNeeded()

	if sm.chunkIndex != 1 {
		t.Errorf("chunkIndex = %d after rotation, want 1", sm.chunkIndex)
	}
	if sm.chunkEvents != 0 {
		t.Errorf("chunkEvents = %d after rotation, want 0 (fresh budget)", sm.chunkEvents)
	}
	if sm.chunkSpan == nil {
		t.Error("rotateChunkIfNeeded did not open a new chunk span")
	}
	sm.endChunk()
}

func TestChunkRotationOnDuration(t *testing.T) {
	sm := newChunkTestSocketManager()
	sm.startChunk()

	// Not yet over budget: no rotation.
	sm.rotateChunkIfNeeded()
	if sm.chunkIndex != 0 {
		t.Fatalf("chunkIndex = %d, want 0 (no rotation before budget exceeded)", sm.chunkIndex)
	}

	// Backdate the chunk start past the duration budget.
	sm.chunkStart = time.Now().Add(-chunkMaxDuration - time.Second)
	sm.rotateChunkIfNeeded()
	if sm.chunkIndex != 1 {
		t.Errorf("chunkIndex = %d, want 1 after duration budget exceeded", sm.chunkIndex)
	}
	sm.endChunk()
}

func TestEndChunkIdempotent(t *testing.T) {
	sm := newChunkTestSocketManager()
	sm.startChunk()
	sm.endChunk()
	if sm.chunkSpan != nil {
		t.Fatal("chunkSpan not cleared after endChunk")
	}
	// A second endChunk must be a no-op, not a nil-span panic.
	sm.endChunk()
	// addChunkEvent after the chunk is ended must also be safe.
	sm.addChunkEvent("message_received")
}
