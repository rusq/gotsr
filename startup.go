package gotsr

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"time"
)

const startupErrorAlreadyRunning = "already_running"

var errTimeout = errors.New("startup timeout")

type startupResult struct {
	OK      bool   `json:"ok"`
	Code    string `json:"code,omitempty"`
	Message string `json:"message,omitempty"`
	MAC     string `json:"mac"`
}

func newStartupChannel(vars envVar) (net.Listener, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("listen for startup result: %w", err)
	}
	secret, err := randomBytes(secretSize)
	if err != nil {
		_ = ln.Close()
		return nil, fmt.Errorf("generate startup secret: %w", err)
	}
	if err := os.Setenv(vars.addr(), ln.Addr().String()); err != nil {
		_ = ln.Close()
		return nil, fmt.Errorf("set startup address: %w", err)
	}
	if err := os.Setenv(vars.secret(), base64.RawURLEncoding.EncodeToString(secret)); err != nil {
		_ = ln.Close()
		return nil, fmt.Errorf("set startup secret: %w", err)
	}
	return ln, nil
}

func awaitStartup(ln net.Listener, vars envVar, timeout time.Duration) error {
	type acceptResult struct {
		conn net.Conn
		err  error
	}
	accepted := make(chan acceptResult, 1)
	go func() {
		conn, err := ln.Accept()
		accepted <- acceptResult{conn: conn, err: err}
	}()

	var conn net.Conn
	select {
	case result := <-accepted:
		if result.err != nil {
			return fmt.Errorf("accept startup result: %w", result.err)
		}
		conn = result.conn
	case <-time.After(timeout):
		_ = ln.Close()
		return errTimeout
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(controlTimeout))

	var result startupResult
	if err := json.NewDecoder(io.LimitReader(conn, maxControlSize)).Decode(&result); err != nil {
		return fmt.Errorf("read startup result: %w", err)
	}
	secret, err := startupSecret(vars)
	if err != nil {
		return err
	}
	wantMAC := messageMAC(secret, "startup", strconv.FormatBool(result.OK), result.Code, result.Message)
	if !validMAC(result.MAC, wantMAC) {
		return errors.New("invalid startup result authentication")
	}
	if result.OK {
		return nil
	}
	if result.Code == startupErrorAlreadyRunning {
		return fmt.Errorf("%w: %s", ErrAlreadyRunning, result.Message)
	}
	if result.Message == "" {
		return errors.New("daemon startup failed")
	}
	return errors.New(result.Message)
}

func reportStartup(vars envVar, startupErr error) error {
	secret, err := startupSecret(vars)
	if err != nil {
		return err
	}
	result := startupResult{OK: startupErr == nil}
	if startupErr != nil {
		result.Message = startupErr.Error()
		if errors.Is(startupErr, ErrAlreadyRunning) {
			result.Code = startupErrorAlreadyRunning
		}
	}
	result.MAC = messageMAC(secret, "startup", strconv.FormatBool(result.OK), result.Code, result.Message)

	address := os.Getenv(vars.addr())
	if address == "" {
		return errors.New("missing startup address")
	}
	conn, err := net.DialTimeout("tcp", address, controlTimeout)
	if err != nil {
		return fmt.Errorf("connect to startup listener: %w", err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(controlTimeout))
	if err := json.NewEncoder(conn).Encode(&result); err != nil {
		return fmt.Errorf("send startup result: %w", err)
	}
	return nil
}

func startupSecret(vars envVar) ([]byte, error) {
	encoded := os.Getenv(vars.secret())
	secret, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil || len(secret) != secretSize {
		return nil, errors.New("invalid startup secret")
	}
	return secret, nil
}
