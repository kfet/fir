// Package ptyrpc provides a Unix-socket JSON-RPC server/client for the PTY
// manager. This lets shell scripts drive PTY sessions via "fir pty" commands
// without needing tmux.
package ptydriver

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Request is a JSON-RPC-like request for the PTY server.
type Request struct {
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
}

// Response is a JSON-RPC-like response from the PTY server.
type Response struct {
	Result json.RawMessage `json:"result,omitempty"`
	Error  string          `json:"error,omitempty"`
}

// Server runs a Manager behind a Unix socket.
type Server struct {
	mgr      *Manager
	listener net.Listener
	wg       sync.WaitGroup
}

// DefaultSocketPath returns the default socket path for the PTY server.
func DefaultSocketPath() string {
	dir := os.Getenv("FIR_PTY_SOCKET_DIR")
	if dir == "" {
		dir = filepath.Join(os.TempDir(), "fir-pty")
	}
	os.MkdirAll(dir, 0o700)
	return filepath.Join(dir, "pty.sock")
}

// NewServer creates a PTY server listening on the given Unix socket path.
func NewServer(sockPath string) (*Server, error) {
	// Remove stale socket.
	os.Remove(sockPath)

	listener, err := net.Listen("unix", sockPath)
	if err != nil {
		return nil, fmt.Errorf("listen %s: %w", sockPath, err)
	}
	return &Server{
		mgr:      NewManager(),
		listener: listener,
	}, nil
}

// Serve accepts connections until the listener is closed.
func (s *Server) Serve() error {
	for {
		conn, err := s.listener.Accept()
		if err != nil {
			return err // listener closed
		}
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			s.handleConn(conn)
		}()
	}
}

// Close shuts down the server.
func (s *Server) Close() {
	s.listener.Close()
	s.wg.Wait()
}

func (s *Server) handleConn(conn net.Conn) {
	defer conn.Close()
	dec := json.NewDecoder(conn)
	enc := json.NewEncoder(conn)

	var req Request
	if err := dec.Decode(&req); err != nil {
		return
	}

	resp := s.dispatch(req)
	enc.Encode(resp)
}

func (s *Server) dispatch(req Request) Response {
	switch req.Method {
	case "new":
		var p struct {
			Session string `json:"session"`
			Window  string `json:"window"`
		}
		json.Unmarshal(req.Params, &p)
		sess, err := s.mgr.New(p.Session, p.Window)
		if err != nil {
			return errResp(err)
		}
		return okResp(map[string]string{"name": sess.Name})

	case "new_window":
		var p struct {
			Session string `json:"session"`
			Window  string `json:"window"`
			Command string `json:"command"`
		}
		json.Unmarshal(req.Params, &p)
		sess, err := s.mgr.NewWindow(p.Session, p.Window, p.Command)
		if err != nil {
			return errResp(err)
		}
		return okResp(map[string]string{"name": sess.Name})

	case "send":
		var p struct {
			Target string `json:"target"`
			Text   string `json:"text"`
		}
		json.Unmarshal(req.Params, &p)
		if err := s.mgr.Send(p.Target, p.Text); err != nil {
			return errResp(err)
		}
		return okResp("ok")

	case "send_raw":
		var p struct {
			Target string `json:"target"`
			Data   string `json:"data"` // base64 or literal
		}
		json.Unmarshal(req.Params, &p)
		if err := s.mgr.SendRaw(p.Target, []byte(p.Data)); err != nil {
			return errResp(err)
		}
		return okResp("ok")

	case "capture":
		var p struct {
			Target string `json:"target"`
			Lines  int    `json:"lines"`
		}
		json.Unmarshal(req.Params, &p)
		if p.Lines == 0 {
			p.Lines = 200
		}
		out, err := s.mgr.Capture(p.Target, p.Lines)
		if err != nil {
			return errResp(err)
		}
		return okResp(map[string]string{"output": out})

	case "wait":
		var p struct {
			Target  string `json:"target"`
			Pattern string `json:"pattern"`
			Timeout int    `json:"timeout"` // seconds
		}
		json.Unmarshal(req.Params, &p)
		if p.Timeout == 0 {
			p.Timeout = 15
		}
		if err := s.mgr.Wait(p.Target, p.Pattern, time.Duration(p.Timeout)*time.Second); err != nil {
			return errResp(err)
		}
		return okResp("ok")

	case "list":
		var p struct {
			Session string `json:"session"`
		}
		json.Unmarshal(req.Params, &p)
		return okResp(s.mgr.List(p.Session))

	case "kill":
		var p struct {
			Session string `json:"session"`
		}
		json.Unmarshal(req.Params, &p)
		if err := s.mgr.Kill(p.Session); err != nil {
			return errResp(err)
		}
		return okResp("ok")

	case "kill_window":
		var p struct {
			Session string `json:"session"`
			Window  string `json:"window"`
		}
		json.Unmarshal(req.Params, &p)
		if err := s.mgr.KillWindow(p.Session, p.Window); err != nil {
			return errResp(err)
		}
		return okResp("ok")

	case "alive":
		var p struct {
			Target string `json:"target"`
		}
		json.Unmarshal(req.Params, &p)
		return okResp(map[string]bool{"alive": s.mgr.Alive(p.Target)})

	case "shutdown":
		// Kill all sessions, then the caller should exit.
		for _, name := range s.mgr.List("") {
			s.mgr.Kill(name)
		}
		go func() {
			time.Sleep(100 * time.Millisecond)
			s.Close()
		}()
		return okResp("ok")

	default:
		return Response{Error: fmt.Sprintf("unknown method: %s", req.Method)}
	}
}

func errResp(err error) Response {
	return Response{Error: err.Error()}
}

func okResp(v any) Response {
	b, _ := json.Marshal(v)
	return Response{Result: b}
}

// Client sends a single request to the PTY server.
type Client struct {
	SocketPath string
}

// Call sends a request and returns the response.
func (c *Client) Call(method string, params any) (*Response, error) {
	conn, err := net.DialTimeout("unix", c.SocketPath, 2*time.Second)
	if err != nil {
		return nil, fmt.Errorf("connect to %s: %w", c.SocketPath, err)
	}
	defer conn.Close()

	conn.SetDeadline(time.Now().Add(120 * time.Second)) // long for wait commands

	paramBytes, _ := json.Marshal(params)
	req := Request{Method: method, Params: paramBytes}
	if err := json.NewEncoder(conn).Encode(req); err != nil {
		return nil, err
	}

	var resp Response
	if err := json.NewDecoder(conn).Decode(&resp); err != nil {
		return nil, err
	}
	return &resp, nil
}
