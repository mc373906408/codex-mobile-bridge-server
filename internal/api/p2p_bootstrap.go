package api

import (
	"context"
	"github.com/coder/websocket"
	"net"
	"net/http"
	"time"
)

// Carries only discovery RPC to the fixed operator-configured EasyTier node.
// The node's empty relay whitelist and disable_relay_data enforce the data boundary.
func (a *Server) p2pBootstrap(w http.ResponseWriter, r *http.Request) error {
	if a.Config.P2PProxyTarget == "" || len(a.Config.P2PBootstrap) == 0 {
		return failure{404, "p2p_disabled"}
	}
	if r.Header.Get("Origin") != "" {
		return failure{403, "native_client_required"}
	}
	select {
	case a.bootstrapSlots <- struct{}{}:
		defer func() { <-a.bootstrapSlots }()
	default:
		return failure{429, "capacity"}
	}
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	upstream, err := (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, "tcp", a.Config.P2PProxyTarget)
	if err != nil {
		return failure{503, "bootstrap_unavailable"}
	}
	defer upstream.Close()
	ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{CompressionMode: websocket.CompressionDisabled})
	if err != nil {
		return nil
	}
	defer ws.CloseNow()
	ws.SetReadLimit(65536)
	stream := websocket.NetConn(ctx, ws, websocket.MessageBinary)
	done := make(chan struct{}, 2)
	copyBounded := func(dst net.Conn, src net.Conn) {
		defer func() { done <- struct{}{} }()
		buffer := make([]byte, 16384)
		for {
			_ = src.SetReadDeadline(time.Now().Add(60 * time.Second))
			n, err := src.Read(buffer)
			if n > 0 {
				_ = dst.SetWriteDeadline(time.Now().Add(10 * time.Second))
				written, writeErr := dst.Write(buffer[:n])
				if writeErr != nil || written != n {
					return
				}
			}
			if err != nil {
				return
			}
		}
	}
	go copyBounded(upstream, stream)
	go copyBounded(stream, upstream)
	<-done
	cancel()
	_ = upstream.Close()
	_ = ws.CloseNow()
	<-done
	return nil
}
