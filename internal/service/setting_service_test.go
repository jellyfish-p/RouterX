package service

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"routerx/internal"
	"routerx/internal/common"
	"routerx/internal/model"
)

func TestSettingCacheRefreshesStaleRedisValues(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:setting_service_test_"+time.Now().Format("150405.000000000")+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.Setting{}); err != nil {
		t.Fatal(err)
	}
	internal.DB = db

	redisServer := newFakeRedisServer(t)
	rdb := redis.NewClient(&redis.Options{Addr: redisServer.Addr(), Protocol: 2, DisableIdentity: true})
	internal.RDB = rdb
	t.Cleanup(func() {
		_ = rdb.Close()
		internal.RDB = nil
	})

	if err := db.Create(&model.Setting{Key: "relay.timeout", Value: "120", Category: "relay"}).Error; err != nil {
		t.Fatal(err)
	}

	svc := NewSettingService()
	got, err := svc.Get("relay.timeout")
	if err != nil || got != "120" {
		t.Fatalf("initial setting should load from DB and warm cache, got value=%q err=%v", got, err)
	}
	if cached, ok := redisServer.HashValue("settings", "relay.timeout"); !ok || cached != "120" {
		t.Fatalf("initial Get should warm Redis cache, got value=%q ok=%v", cached, ok)
	}
	if err := db.Model(&model.Setting{}).Where("key = ?", "relay.timeout").Update("value", "30").Error; err != nil {
		t.Fatal(err)
	}
	got, err = svc.Get("relay.timeout")
	if err != nil || got != "120" {
		t.Fatalf("stale cache should be observable before refresh, got value=%q err=%v", got, err)
	}

	if err := svc.Set("relay.timeout", "45"); err != nil {
		t.Fatal(err)
	}
	got, err = svc.Get("relay.timeout")
	if err != nil || got != "45" {
		t.Fatalf("Set should refresh stale cache, got value=%q err=%v", got, err)
	}
	if cached, ok := redisServer.HashValue("settings", "relay.timeout"); !ok || cached != "45" {
		t.Fatalf("Set should overwrite Redis cache, got value=%q ok=%v", cached, ok)
	}

	if err := svc.BatchSet(map[string]string{
		"relay.timeout":            "60",
		"log.request_body_enabled": "true",
	}); err != nil {
		t.Fatal(err)
	}
	got, err = svc.Get("relay.timeout")
	if err != nil || got != "60" {
		t.Fatalf("BatchSet should refresh existing cached setting, got value=%q err=%v", got, err)
	}
	bodyLogging, err := svc.GetBool("log.request_body_enabled")
	if err != nil || !bodyLogging {
		t.Fatalf("BatchSet should cache newly created setting, got value=%v err=%v", bodyLogging, err)
	}
}

func TestSettingLoadCacheAppliesRequestIDHeaderRuntimeConfig(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:setting_runtime_test_"+time.Now().Format("150405.000000000")+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.Setting{}); err != nil {
		t.Fatal(err)
	}
	internal.DB = db
	internal.RDB = nil
	t.Cleanup(func() {
		internal.RDB = nil
	})

	if err := db.Create(&model.Setting{
		Key:      "observability.request_id_header",
		Value:    "X-Correlation-Id",
		Category: "observability",
	}).Error; err != nil {
		t.Fatal(err)
	}

	common.SetRequestIDHeaderName(common.DefaultRequestIDHeader)
	if err := NewSettingService().LoadCache(); err != nil {
		t.Fatal(err)
	}
	if got := common.RequestIDHeaderName(); got != "X-Correlation-Id" {
		t.Fatalf("LoadCache should apply request id header runtime config, got %q", got)
	}
}

// fakeRedisServer implements the tiny Redis subset SettingService needs in tests.
type fakeRedisServer struct {
	listener  net.Listener
	mu        sync.Mutex
	hashes    map[string]map[string]string
	strings   map[string]string
	published []fakeRedisPublish
}

type fakeRedisPublish struct {
	Channel string
	Message string
}

func newFakeRedisServer(t *testing.T) *fakeRedisServer {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := &fakeRedisServer{
		listener: listener,
		hashes:   make(map[string]map[string]string),
		strings:  make(map[string]string),
	}
	go server.accept()
	t.Cleanup(func() {
		_ = listener.Close()
	})
	return server
}

func (s *fakeRedisServer) Addr() string {
	return s.listener.Addr().String()
}

func (s *fakeRedisServer) HashValue(hash, field string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	values := s.hashes[hash]
	if values == nil {
		return "", false
	}
	value, ok := values[field]
	return value, ok
}

func (s *fakeRedisServer) StringValue(key string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	value, ok := s.strings[key]
	return value, ok
}

func (s *fakeRedisServer) SetString(key, value string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.strings[key] = value
}

func (s *fakeRedisServer) Published(channel string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	messages := []string{}
	for _, item := range s.published {
		if item.Channel == channel {
			messages = append(messages, item.Message)
		}
	}
	return messages
}

func (s *fakeRedisServer) accept() {
	for {
		conn, err := s.listener.Accept()
		if err != nil {
			return
		}
		go s.handle(conn)
	}
}

func (s *fakeRedisServer) handle(conn net.Conn) {
	defer conn.Close()
	reader := bufio.NewReader(conn)
	writer := bufio.NewWriter(conn)
	for {
		args, err := readRESPArray(reader)
		if err != nil {
			return
		}
		s.writeResponse(writer, args)
		if err := writer.Flush(); err != nil {
			return
		}
	}
}

func (s *fakeRedisServer) writeResponse(writer *bufio.Writer, args []string) {
	if len(args) == 0 {
		writeRESPError(writer, "empty command")
		return
	}
	switch strings.ToLower(args[0]) {
	case "get":
		if len(args) != 2 {
			writeRESPError(writer, "get requires key")
			return
		}
		s.mu.Lock()
		value, ok := s.strings[args[1]]
		s.mu.Unlock()
		if !ok {
			_, _ = writer.WriteString("$-1\r\n")
			return
		}
		writeRESPBulkString(writer, value)
	case "set":
		if len(args) < 3 {
			writeRESPError(writer, "set requires key and value")
			return
		}
		s.mu.Lock()
		s.strings[args[1]] = args[2]
		s.mu.Unlock()
		_, _ = writer.WriteString("+OK\r\n")
	case "del":
		if len(args) < 2 {
			writeRESPError(writer, "del requires at least one key")
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
			writeRESPError(writer, "hget requires hash and field")
			return
		}
		s.mu.Lock()
		value, ok := s.hashes[args[1]][args[2]]
		s.mu.Unlock()
		if !ok {
			_, _ = writer.WriteString("$-1\r\n")
			return
		}
		writeRESPBulkString(writer, value)
	case "hset":
		if len(args) < 4 || len(args[2:])%2 != 0 {
			writeRESPError(writer, "hset requires field value pairs")
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
	case "ping":
		_, _ = writer.WriteString("+PONG\r\n")
	case "publish":
		if len(args) != 3 {
			writeRESPError(writer, "publish requires channel and message")
			return
		}
		s.mu.Lock()
		s.published = append(s.published, fakeRedisPublish{Channel: args[1], Message: args[2]})
		s.mu.Unlock()
		_, _ = writer.WriteString(":1\r\n")
	case "hello":
		writeRESPError(writer, "unknown command 'HELLO'")
	case "client", "select":
		_, _ = writer.WriteString("+OK\r\n")
	default:
		_, _ = writer.WriteString("+OK\r\n")
	}
}

func readRESPArray(reader *bufio.Reader) ([]string, error) {
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

func writeRESPBulkString(writer *bufio.Writer, value string) {
	_, _ = writer.WriteString("$" + strconv.Itoa(len(value)) + "\r\n" + value + "\r\n")
}

func writeRESPError(writer *bufio.Writer, message string) {
	_, _ = writer.WriteString("-ERR " + message + "\r\n")
}
