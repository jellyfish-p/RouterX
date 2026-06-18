package router

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// routerFakeRedisServer implements the small Redis command subset used by
// router integration tests: settings cache reads/writes, auth lookups and rate-limit counters.
type routerFakeRedisServer struct {
	listener net.Listener
	mu       sync.Mutex
	hashes   map[string]map[string]string
	strings  map[string]string
	counters map[string]int64
}

func newRouterFakeRedisServer(t *testing.T) *routerFakeRedisServer {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := &routerFakeRedisServer{
		listener: listener,
		hashes:   make(map[string]map[string]string),
		strings:  make(map[string]string),
		counters: make(map[string]int64),
	}
	go server.accept()
	t.Cleanup(func() {
		_ = listener.Close()
	})
	return server
}

func (s *routerFakeRedisServer) Addr() string {
	return s.listener.Addr().String()
}

func (s *routerFakeRedisServer) accept() {
	for {
		conn, err := s.listener.Accept()
		if err != nil {
			return
		}
		go s.handle(conn)
	}
}

func (s *routerFakeRedisServer) handle(conn net.Conn) {
	defer conn.Close()
	reader := bufio.NewReader(conn)
	writer := bufio.NewWriter(conn)
	for {
		args, err := readRouterRESPArray(reader)
		if err != nil {
			return
		}
		s.writeResponse(writer, args)
		if err := writer.Flush(); err != nil {
			return
		}
	}
}

func (s *routerFakeRedisServer) writeResponse(writer *bufio.Writer, args []string) {
	if len(args) == 0 {
		writeRouterRESPError(writer, "empty command")
		return
	}
	switch strings.ToLower(args[0]) {
	case "get":
		if len(args) != 2 {
			writeRouterRESPError(writer, "get requires key")
			return
		}
		s.mu.Lock()
		value, ok := s.strings[args[1]]
		s.mu.Unlock()
		if !ok {
			_, _ = writer.WriteString("$-1\r\n")
			return
		}
		writeRouterRESPBulkString(writer, value)
	case "set":
		if len(args) < 3 {
			writeRouterRESPError(writer, "set requires key and value")
			return
		}
		s.mu.Lock()
		s.strings[args[1]] = args[2]
		s.mu.Unlock()
		_, _ = writer.WriteString("+OK\r\n")
	case "del":
		if len(args) < 2 {
			writeRouterRESPError(writer, "del requires at least one key")
			return
		}
		deleted := 0
		s.mu.Lock()
		for _, key := range args[1:] {
			if _, ok := s.strings[key]; ok {
				delete(s.strings, key)
				deleted++
			}
		}
		s.mu.Unlock()
		_, _ = writer.WriteString(":" + strconv.Itoa(deleted) + "\r\n")
	case "hget":
		if len(args) != 3 {
			writeRouterRESPError(writer, "hget requires hash and field")
			return
		}
		s.mu.Lock()
		value, ok := s.hashes[args[1]][args[2]]
		s.mu.Unlock()
		if !ok {
			_, _ = writer.WriteString("$-1\r\n")
			return
		}
		writeRouterRESPBulkString(writer, value)
	case "hset":
		if len(args) < 4 || len(args[2:])%2 != 0 {
			writeRouterRESPError(writer, "hset requires field value pairs")
			return
		}
		added := 0
		s.mu.Lock()
		if s.hashes[args[1]] == nil {
			s.hashes[args[1]] = make(map[string]string)
		}
		for i := 2; i < len(args); i += 2 {
			if _, exists := s.hashes[args[1]][args[i]]; !exists {
				added++
			}
			s.hashes[args[1]][args[i]] = args[i+1]
		}
		s.mu.Unlock()
		_, _ = writer.WriteString(":" + strconv.Itoa(added) + "\r\n")
	case "incr":
		if len(args) != 2 {
			writeRouterRESPError(writer, "incr requires key")
			return
		}
		s.mu.Lock()
		s.counters[args[1]]++
		count := s.counters[args[1]]
		s.mu.Unlock()
		_, _ = writer.WriteString(":" + strconv.FormatInt(count, 10) + "\r\n")
	case "expire":
		_, _ = writer.WriteString(":1\r\n")
	case "ping":
		_, _ = writer.WriteString("+PONG\r\n")
	case "hello":
		writeRouterRESPError(writer, "unknown command 'HELLO'")
	case "client", "select":
		_, _ = writer.WriteString("+OK\r\n")
	default:
		_, _ = writer.WriteString("+OK\r\n")
	}
}

func readRouterRESPArray(reader *bufio.Reader) ([]string, error) {
	line, err := reader.ReadString('\n')
	if err != nil {
		return nil, err
	}
	line = strings.TrimSuffix(line, "\r\n")
	if !strings.HasPrefix(line, "*") {
		return nil, fmt.Errorf("expected RESP array, got %q", line)
	}
	count, err := strconv.Atoi(strings.TrimPrefix(line, "*"))
	if err != nil {
		return nil, err
	}
	args := make([]string, 0, count)
	for i := 0; i < count; i++ {
		lengthLine, err := reader.ReadString('\n')
		if err != nil {
			return nil, err
		}
		lengthLine = strings.TrimSuffix(lengthLine, "\r\n")
		if !strings.HasPrefix(lengthLine, "$") {
			return nil, fmt.Errorf("expected RESP bulk string, got %q", lengthLine)
		}
		length, err := strconv.Atoi(strings.TrimPrefix(lengthLine, "$"))
		if err != nil {
			return nil, err
		}
		raw := make([]byte, length+2)
		if _, err := io.ReadFull(reader, raw); err != nil {
			return nil, err
		}
		args = append(args, string(raw[:length]))
	}
	return args, nil
}

func writeRouterRESPBulkString(writer *bufio.Writer, value string) {
	_, _ = writer.WriteString("$" + strconv.Itoa(len(value)) + "\r\n" + value + "\r\n")
}

func writeRouterRESPError(writer *bufio.Writer, message string) {
	_, _ = writer.WriteString("-ERR " + message + "\r\n")
}
