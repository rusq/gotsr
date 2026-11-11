package gotsr

import (
	"bufio"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	controlStateVersion = 2
	controlTimeout      = 2 * time.Second
	maxStateSize        = 4096
	maxControlSize      = 4096
	secretSize          = 32
	nonceSize           = 32

	commandStatus    = "status"
	commandTerminate = "terminate"
)

var errControlUnavailable = errors.New("control endpoint unavailable")

var controlRuntimes = struct {
	sync.Mutex
	entries map[*controlRuntime]pathIdentity
}{entries: make(map[*controlRuntime]pathIdentity)}

type controlMetadata struct {
	Version int    `json:"version"`
	Network string `json:"network"`
	Address string `json:"address"`
	Secret  string `json:"secret"`
}

type controlState struct {
	PID      int
	Network  string
	Address  string
	secret   []byte
	secretID string
}

type controlRequest struct {
	Command string `json:"command"`
	Nonce   string `json:"nonce"`
	MAC     string `json:"mac"`
}

type controlResponse struct {
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
	MAC   string `json:"mac"`
}

type controlRuntime struct {
	listener net.Listener
	pidFile  string
	secretID string
	lock     *stateLock
	identity pathIdentity
	close    sync.Once
}

func startControl(pidFile string, pid int, onTerminate func()) (*controlRuntime, error) {
	if currentControlRuntime(pidFile) != nil {
		return nil, ErrAlreadyRunning
	}
	lock, err := acquireStateLock(pidFile)
	if err != nil {
		return nil, err
	}
	failed := true
	defer func() {
		if failed {
			lock.Close()
		}
	}()
	if err := recoverStateForStart(pidFile); err != nil {
		return nil, err
	}

	secret, err := randomBytes(secretSize)
	if err != nil {
		return nil, fmt.Errorf("generate control secret: %w", err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("listen for control requests: %w", err)
	}

	rt := &controlRuntime{
		listener: ln,
		pidFile:  pidFile,
		secretID: base64.RawURLEncoding.EncodeToString(secret),
		lock:     lock,
	}
	go serveControl(ln, secret, onTerminate)

	state := controlState{
		PID:      pid,
		Network:  "tcp",
		Address:  ln.Addr().String(),
		secret:   secret,
		secretID: rt.secretID,
	}
	if err := writeControlState(pidFile, state); err != nil {
		_ = ln.Close()
		return nil, err
	}
	identity, err := identifyPath(pidFile)
	if err != nil {
		_ = ln.Close()
		lock.removeOwnedState(pidFile, rt.secretID)
		return nil, fmt.Errorf("identify published state: %w", err)
	}
	rt.identity = identity
	failed = false
	return rt, nil
}

func (rt *controlRuntime) Close() {
	rt.close.Do(func() {
		_ = rt.listener.Close()
		rt.lock.removeOwnedState(rt.pidFile, rt.secretID)
		unregisterControlRuntime(rt)
		rt.lock.Close()
	})
}

func registerControlRuntime(rt *controlRuntime) {
	controlRuntimes.Lock()
	controlRuntimes.entries[rt] = rt.identity
	controlRuntimes.Unlock()
}

func currentControlRuntime(pidFile string) *controlRuntime {
	identity, err := identifyPath(pidFile)
	if err != nil {
		return nil
	}
	controlRuntimes.Lock()
	defer controlRuntimes.Unlock()
	for runtime, owner := range controlRuntimes.entries {
		if samePathIdentity(identity, owner) {
			return runtime
		}
	}
	return nil
}

func unregisterControlRuntime(rt *controlRuntime) {
	controlRuntimes.Lock()
	delete(controlRuntimes.entries, rt)
	controlRuntimes.Unlock()
}

func recoverStateForStart(pidFile string) error {
	state, err := readControlState(pidFile)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("existing state cannot be verified: %w", err)
	}
	if err := sendControl(state, commandStatus); err == nil {
		return ErrAlreadyRunning
	} else if !errors.Is(err, errControlUnavailable) {
		return fmt.Errorf("existing state identity cannot be verified: %w", err)
	}
	// The lifetime lock proves that no current v2 instance owns this state.
	// An unavailable authenticated endpoint is therefore crash residue and can
	// be removed before publishing the successor state.
	if err := os.Remove(pidFile); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove stale state: %w", err)
	}
	return nil
}

func serveControl(ln net.Listener, secret []byte, onTerminate func()) {
	for {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		go handleControlConnection(conn, secret, onTerminate)
	}
}

func handleControlConnection(conn net.Conn, secret []byte, onTerminate func()) {
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(controlTimeout))

	var req controlRequest
	if err := json.NewDecoder(io.LimitReader(conn, maxControlSize)).Decode(&req); err != nil {
		return
	}
	if req.Command != commandStatus && req.Command != commandTerminate {
		return
	}
	wantMAC := messageMAC(secret, "request", req.Command, req.Nonce)
	if !validMAC(req.MAC, wantMAC) {
		return
	}

	resp := controlResponse{OK: true}
	resp.MAC = messageMAC(secret, "response", req.Command, req.Nonce, strconv.FormatBool(resp.OK), resp.Error)
	if err := json.NewEncoder(conn).Encode(&resp); err != nil {
		return
	}
	if req.Command == commandTerminate {
		onTerminate()
	}
}

func sendControl(state controlState, command string) error {
	nonce, err := randomBytes(nonceSize)
	if err != nil {
		return fmt.Errorf("generate control nonce: %w", err)
	}
	nonceText := base64.RawURLEncoding.EncodeToString(nonce)
	req := controlRequest{
		Command: command,
		Nonce:   nonceText,
		MAC:     messageMAC(state.secret, "request", command, nonceText),
	}

	conn, err := net.DialTimeout(state.Network, state.Address, controlTimeout)
	if err != nil {
		return fmt.Errorf("%w: %v", errControlUnavailable, err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(controlTimeout))
	if err := json.NewEncoder(conn).Encode(&req); err != nil {
		return fmt.Errorf("send control request: %w", err)
	}

	var resp controlResponse
	if err := json.NewDecoder(io.LimitReader(conn, maxControlSize)).Decode(&resp); err != nil {
		return fmt.Errorf("read control response: %w", err)
	}
	wantMAC := messageMAC(state.secret, "response", command, nonceText, strconv.FormatBool(resp.OK), resp.Error)
	if !validMAC(resp.MAC, wantMAC) {
		return errors.New("invalid control response authentication")
	}
	if !resp.OK {
		if resp.Error == "" {
			return errors.New("control request rejected")
		}
		return fmt.Errorf("control request rejected: %s", resp.Error)
	}
	return nil
}

func messageMAC(secret []byte, fields ...string) string {
	h := hmac.New(sha256.New, secret)
	for _, field := range fields {
		_, _ = io.WriteString(h, strconv.Itoa(len(field)))
		_, _ = io.WriteString(h, ":")
		_, _ = io.WriteString(h, field)
	}
	return hex.EncodeToString(h.Sum(nil))
}

func validMAC(got string, want string) bool {
	gotBytes, err := hex.DecodeString(got)
	if err != nil {
		return false
	}
	wantBytes, _ := hex.DecodeString(want)
	return hmac.Equal(gotBytes, wantBytes)
}

func randomBytes(size int) ([]byte, error) {
	b := make([]byte, size)
	if _, err := io.ReadFull(rand.Reader, b); err != nil {
		return nil, err
	}
	return b, nil
}

func writeControlState(filename string, state controlState) error {
	if state.PID <= 0 {
		return ErrNoPID
	}
	metadata := controlMetadata{
		Version: controlStateVersion,
		Network: state.Network,
		Address: state.Address,
		Secret:  base64.RawURLEncoding.EncodeToString(state.secret),
	}
	encoded, err := json.Marshal(metadata)
	if err != nil {
		return fmt.Errorf("encode control state: %w", err)
	}

	dir := filepath.Dir(filename)
	tmp, err := os.CreateTemp(dir, ".gotsr-state-*")
	if err != nil {
		return fmt.Errorf("create temporary state file: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)

	failed := true
	defer func() {
		if failed {
			_ = tmp.Close()
		}
	}()
	if err := tmp.Chmod(0o600); err != nil {
		return fmt.Errorf("secure temporary state file: %w", err)
	}
	if _, err := fmt.Fprintf(tmp, "%d\n%s\n", state.PID, encoded); err != nil {
		return fmt.Errorf("write temporary state file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		return fmt.Errorf("sync temporary state file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temporary state file: %w", err)
	}
	if err := replaceStateFile(tmpName, filename); err != nil {
		return fmt.Errorf("publish state file: %w", err)
	}
	failed = false
	return nil
}

func readControlState(filename string) (controlState, error) {
	var zero controlState
	f, err := openRegularState(filename)
	if err != nil {
		return zero, err
	}
	defer f.Close()

	scanner := bufio.NewScanner(io.LimitReader(f, maxStateSize+1))
	scanner.Buffer(make([]byte, 1024), maxStateSize)
	if !scanner.Scan() {
		if err := scanner.Err(); err != nil {
			return zero, fmt.Errorf("read state PID: %w", err)
		}
		return zero, errors.New("invalid state file: missing PID")
	}
	pid, err := strconv.Atoi(scanner.Text())
	if err != nil || pid <= 0 {
		return zero, ErrNoPID
	}
	if !scanner.Scan() {
		if err := scanner.Err(); err != nil {
			return zero, fmt.Errorf("read control metadata: %w", err)
		}
		return zero, errors.New("unverified legacy PID file")
	}
	var metadata controlMetadata
	if err := json.Unmarshal(scanner.Bytes(), &metadata); err != nil {
		return zero, fmt.Errorf("invalid control metadata: %w", err)
	}
	if scanner.Scan() {
		return zero, errors.New("invalid state file: unexpected data")
	}
	if err := scanner.Err(); err != nil {
		return zero, fmt.Errorf("read state file: %w", err)
	}
	if metadata.Version != controlStateVersion {
		return zero, fmt.Errorf("unsupported state version %d", metadata.Version)
	}
	if metadata.Network != "tcp" {
		return zero, fmt.Errorf("unsupported control network %q", metadata.Network)
	}
	if err := validateLoopbackAddress(metadata.Address); err != nil {
		return zero, err
	}
	secret, err := base64.RawURLEncoding.DecodeString(metadata.Secret)
	if err != nil || len(secret) != secretSize {
		return zero, errors.New("invalid control secret")
	}
	return controlState{
		PID:      pid,
		Network:  metadata.Network,
		Address:  metadata.Address,
		secret:   secret,
		secretID: metadata.Secret,
	}, nil
}

func openRegularState(filename string) (*os.File, error) {
	before, err := os.Lstat(filename)
	if err != nil {
		return nil, err
	}
	if !before.Mode().IsRegular() {
		return nil, errors.New("state file is not a regular file")
	}
	if err := validateStateFilePermissions(before); err != nil {
		return nil, err
	}
	if before.Size() > maxStateSize {
		return nil, errors.New("state file is too large")
	}
	f, err := os.Open(filename)
	if err != nil {
		return nil, err
	}
	opened, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	after, err := os.Lstat(filename)
	if err != nil || !after.Mode().IsRegular() || !os.SameFile(before, opened) || !os.SameFile(opened, after) {
		_ = f.Close()
		return nil, errors.New("state file changed while opening")
	}
	return f, nil
}

func validateLoopbackAddress(address string) error {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("invalid control address: %w", err)
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	if ip == nil || !ip.IsLoopback() {
		return errors.New("control address is not loopback")
	}
	if portNumber, err := strconv.Atoi(port); err != nil || portNumber < 1 || portNumber > 65535 {
		return errors.New("invalid control port")
	}
	return nil
}

func isRunning(pidFile string) (bool, error) {
	state, err := readControlState(pidFile)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	if err := sendControl(state, commandStatus); err != nil {
		if errors.Is(err, errControlUnavailable) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

func terminate(pidFile string) error {
	state, err := readControlState(pidFile)
	if err != nil {
		if os.IsNotExist(err) {
			return ErrNotRunning
		}
		return err
	}
	if err := sendControl(state, commandTerminate); err != nil {
		if errors.Is(err, errControlUnavailable) {
			return ErrNotRunning
		}
		return err
	}
	lg.Printf("process %d terminated", state.PID)
	return nil
}
