package webproxy

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/mc373906408/codex-mobile-bridge-server/internal/testutil"
)

func TestProxyLiveStreamsCancelRevocationAndUnknownWrites(t *testing.T) {
	s := testutil.Database(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	c := testutil.Config()
	c.BytesPerSecond = 64 * 1024 * 1024
	g, _, _ := testutil.Devices(t, s)
	account, err := s.AccountByLogin(ctx, "devices")
	if err != nil {
		t.Fatal(err)
	}
	tokens, err := s.NewWebClientSession(ctx, account, c)
	if err != nil {
		t.Fatal(err)
	}
	principal, err := s.AuthenticateWebClient(ctx, tokens.Web)
	if err != nil {
		t.Fatal(err)
	}
	h := New(s, c)
	defer h.Shutdown()
	mux := http.NewServeMux()
	mux.HandleFunc("/socket", func(w http.ResponseWriter, r *http.Request) { h.Serve(w, r, g, g.Token) })
	mux.HandleFunc("/proxy/", func(w http.ResponseWriter, r *http.Request) {
		expires := time.Now().Add(time.Hour)
		if r.URL.Path == "/proxy/expires" {
			expires = time.Now().Add(200 * time.Millisecond)
		}
		err := h.Proxy(w, r, principal, g.ID, strings.TrimPrefix(r.URL.Path, "/proxy"), "https://web.example.test", expires)
		if err != nil {
			http.Error(w, err.Error(), 502)
		}
	})
	server := httptest.NewServer(mux)
	defer server.Close()
	ws, _, err := websocket.Dial(ctx, strings.Replace(server.URL, "http:", "ws:", 1)+"/socket", &websocket.DialOptions{Subprotocols: []string{Protocol}})
	if err != nil {
		t.Fatal(err)
	}
	defer ws.CloseNow()
	ws.SetReadLimit(Chunk + 9)
	inbox := make(chan Message, 64)
	go func() {
		defer close(inbox)
		for {
			kind, raw, e := ws.Read(ctx)
			if e != nil {
				return
			}
			var m Message
			if kind == websocket.MessageBinary {
				m.Kind = "data"
				m.ID = binary.BigEndian.Uint64(raw[1:9])
				m.Data = raw[9:]
			} else {
				_ = json.Unmarshal(raw, &m)
			}
			select {
			case inbox <- m:
			case <-ctx.Done():
				return
			}
		}
	}()
	send := func(m Message) {
		t.Helper()
		kind := websocket.MessageText
		raw, _ := json.Marshal(m)
		if m.Data != nil {
			kind = websocket.MessageBinary
			raw = make([]byte, len(m.Data)+9)
			raw[0] = 1
			binary.BigEndian.PutUint64(raw[1:9], m.ID)
			copy(raw[9:], m.Data)
		}
		if err := ws.Write(ctx, kind, raw); err != nil {
			t.Fatal(err)
		}
	}
	next := func(kind string) Message {
		t.Helper()
		for {
			select {
			case m, ok := <-inbox:
				if !ok {
					t.Fatal("gateway socket closed")
				}
				if m.Kind == kind {
					return m
				}
			case <-ctx.Done():
				t.Fatal("timed out waiting", kind)
			}
		}
	}
	for range 100 {
		if h.Online(g.ID) {
			break
		}
		time.Sleep(time.Millisecond)
	}
	// Two requests use fresh bytes from the desktop without a cloud asset package.
	for _, content := range []string{"desktop-v1", "desktop-v2"} {
		finished := make(chan string, 1)
		go func() {
			r, e := http.Get(server.URL + "/proxy/")
			if e != nil {
				finished <- e.Error()
				return
			}
			defer r.Body.Close()
			raw, e := io.ReadAll(r.Body)
			if e != nil {
				finished <- e.Error()
			} else {
				finished <- string(raw)
			}
		}()
		request := next("request")
		if request.Account != g.AccountID || request.Session != principal.SessionID || request.Base != "/d/"+g.ID {
			t.Fatal("request scope")
		}
		next("request_end")
		send(Message{Kind: "response_start", ID: request.ID, Status: 200, Headers: map[string]string{"Content-Type": "text/html", "Content-Length": "10", "Cache-Control": "no-store"}})
		next("credit")
		send(Message{ID: request.ID, Data: []byte(content)})
		send(Message{Kind: "response_end", ID: request.ID})
		if got := <-finished; got != content {
			t.Fatal("live resource", got)
		}
		next("cancel")
	}
	// A maximum-size upload crosses the actual HTTP and WebSocket readers once.
	uploadResult := make(chan error, 1)
	go func() {
		r, e := http.Post(server.URL+"/proxy/upload", "application/octet-stream", bytes.NewReader(bytes.Repeat([]byte{0x5a}, MaxBody)))
		if e == nil {
			defer r.Body.Close()
			_, e = io.Copy(io.Discard, r.Body)
			if e == nil && r.StatusCode != 204 {
				e = io.ErrUnexpectedEOF
			}
		}
		uploadResult <- e
	}()
	upload := next("request")
	if upload.Length != MaxBody {
		t.Fatal("upload length", upload.Length)
	}
	send(Message{Kind: "credit", ID: upload.ID, Count: 4})
	for received := 0; received < MaxBody; {
		chunk := next("data")
		if chunk.ID != upload.ID || len(chunk.Data) > Chunk || bytes.Count(chunk.Data, []byte{0x5a}) != len(chunk.Data) {
			t.Fatal("large upload corrupted")
		}
		received += len(chunk.Data)
		if received < MaxBody {
			send(Message{Kind: "credit", ID: upload.ID, Count: 1})
		}
	}
	next("request_end")
	send(Message{Kind: "response_start", ID: upload.ID, Status: 204})
	next("credit")
	send(Message{Kind: "response_end", ID: upload.ID})
	if err := <-uploadResult; err != nil {
		t.Fatal("large upload", err)
	}
	next("cancel")

	type result struct {
		body   []byte
		err    error
		status int
	}
	read := func(path string) <-chan result {
		out := make(chan result, 1)
		go func() {
			r, err := http.Get(server.URL + "/proxy/" + path)
			if err != nil {
				out <- result{err: err}
				return
			}
			defer r.Body.Close()
			data, err := io.ReadAll(r.Body)
			out <- result{body: data, err: err, status: r.StatusCode}
		}()
		return out
	}
	downloadResult := read("large")
	download := next("request")
	next("request_end")
	send(Message{Kind: "response_start", ID: download.ID, Status: 200, Headers: map[string]string{"Content-Type": "application/octet-stream", "Content-Length": strconv.Itoa(MaxBody)}})
	next("credit")
	// A second large response from the same browser is rejected without harming the first.
	limited := read("second-large")
	second := next("request")
	next("request_end")
	send(Message{Kind: "response_start", ID: second.ID, Status: 200, Headers: map[string]string{"Content-Length": strconv.Itoa(MaxBody)}})
	if r := <-limited; r.status != 502 || !strings.Contains(string(r.body), "web_capacity") {
		t.Fatal("large response capacity", r.status, r.err)
	}
	next("cancel")
	block := bytes.Repeat([]byte{0x6b}, Chunk)
	for sent := 0; sent < MaxBody; sent += len(block) {
		send(Message{ID: download.ID, Data: block})
		next("credit")
	}
	send(Message{Kind: "response_end", ID: download.ID})
	if r := <-downloadResult; r.err != nil || len(r.body) != MaxBody || bytes.Count(r.body, []byte{0x6b}) != MaxBody {
		t.Fatal("large download", len(r.body), r.err)
	}
	next("cancel")
	// An ended response shorter than Content-Length is a failed read, never success JSON.
	truncated := read("truncated")
	short := next("request")
	next("request_end")
	send(Message{Kind: "response_start", ID: short.ID, Status: 200, Headers: map[string]string{"Content-Length": "10"}})
	next("credit")
	send(Message{ID: short.ID, Data: []byte("short")})
	send(Message{Kind: "response_end", ID: short.ID})
	if r := <-truncated; r.err == nil || string(r.body) != "short" {
		t.Fatal("truncation was acknowledged as success", r)
	}
	next("cancel")
	expired := read("expires")
	next("request")
	next("request_end")
	if r := <-expired; r.status != 502 {
		t.Fatal("expired stream retained", r.status, r.err)
	}
	next("cancel")
	if !h.Online(g.ID) {
		t.Fatal("expiry closed desktop")
	}
	// Metadata is bounded before dispatch, so one browser cannot kill the tunnel.
	tooLarge, _ := http.NewRequest("GET", server.URL+"/proxy/metadata", nil)
	tooLarge.Header.Set("Range", strings.Repeat("x", 17000))
	badResponse, err := http.DefaultClient.Do(tooLarge)
	if err != nil {
		t.Fatal(err)
	}
	badResponse.Body.Close()
	if badResponse.StatusCode != 502 || !h.Online(g.ID) {
		t.Fatal("oversized metadata escaped request boundary")
	}
	// Streaming flush reaches the caller before end; cancelling closes only that request.
	requestCtx, stop := context.WithCancel(ctx)
	req, _ := http.NewRequestWithContext(requestCtx, "GET", server.URL+"/proxy/events", nil)
	response := make(chan *http.Response, 1)
	go func() { r, _ := http.DefaultClient.Do(req); response <- r }()
	live := next("request")
	next("request_end")
	send(Message{Kind: "response_start", ID: live.ID, Status: 200, Headers: map[string]string{"Content-Type": "text/event-stream"}})
	next("credit")
	send(Message{ID: live.ID, Data: []byte("data: first\n\n")})
	r := <-response
	if r == nil {
		t.Fatal("no streaming headers")
	}
	buf := make([]byte, 13)
	if _, err = io.ReadFull(r.Body, buf); err != nil || string(buf) != "data: first\n\n" {
		t.Fatal("stream not flushed", err)
	}
	stop()
	r.Body.Close()
	next("cancel")
	if !h.Online(g.ID) {
		t.Fatal("request cancellation closed desktop")
	}
	// A write with a lost response is delivered once, never retried by the proxy.
	done := make(chan struct{})
	go func() {
		r, _ := http.Post(server.URL+"/proxy/api/sessions", "application/json", bytes.NewBufferString(`{}`))
		if r != nil {
			io.Copy(io.Discard, r.Body)
			r.Body.Close()
		}
		close(done)
	}()
	write := next("request")
	send(Message{Kind: "credit", ID: write.ID, Count: 4})
	data := next("data")
	if string(data.Data) != "{}" {
		t.Fatal("upload corrupted")
	}
	next("request_end")
	send(Message{Kind: "stream_error", ID: write.ID})
	<-done
	next("cancel")
	select {
	case m := <-inbox:
		if m.Kind == "request" {
			t.Fatal("unknown write replayed")
		}
	case <-time.After(30 * time.Millisecond):
	}
	// Revocation cancels outstanding response and keeps the gateway connected.
	done = make(chan struct{})
	go func() {
		r, _ := http.Get(server.URL + "/proxy/pending")
		if r != nil {
			r.Body.Close()
		}
		close(done)
	}()
	next("request")
	next("request_end")
	if err = s.RevokeResource(ctx, principal, principal.ID, "session", principal.SessionID); err != nil {
		t.Fatal(err)
	}
	h.CloseSession(principal.SessionID)
	<-done
	if !h.Online(g.ID) {
		t.Fatal("browser logout closed desktop")
	}
}
