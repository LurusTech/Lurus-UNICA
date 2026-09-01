package main

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestWeComSend_BusinessErrorInBody verifies that a WeCom robot response
// with HTTP 200 but a non-zero errcode in the body is treated as failure.
// WeCom returns 200 for rate limiting, removed robots, expired tokens, etc.,
// with the real outcome encoded in {"errcode":...,"errmsg":...}.
func TestWeComSend_BusinessErrorInBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"errcode":45009,"errmsg":"reach max send notifications count of the day"}`))
	}))
	defer server.Close()

	n := &WeComNotifier{}
	err := n.Send(server.URL, "", samplePayload())

	require.Error(t, err)
	assert.Contains(t, err.Error(), "45009")
	assert.Contains(t, err.Error(), "reach max send notifications count of the day")
}

// TestWeComSend_Success verifies that a WeCom robot response with errcode 0
// is treated as success (positive control).
func TestWeComSend_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"errcode":0,"errmsg":"ok"}`))
	}))
	defer server.Close()

	n := &WeComNotifier{}
	err := n.Send(server.URL, "", samplePayload())

	assert.NoError(t, err)
}

// TestWeComSend_EmptyBody verifies that an empty 200 body cannot be
// confirmed as delivered, so Send must fail closed.
func TestWeComSend_EmptyBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	n := &WeComNotifier{}
	err := n.Send(server.URL, "", samplePayload())

	require.Error(t, err)
}

// TestWeComSend_NonJSONBody verifies that a non-JSON 200 body cannot be
// confirmed as delivered, so Send must fail closed.
func TestWeComSend_NonJSONBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("<html>not json</html>"))
	}))
	defer server.Close()

	n := &WeComNotifier{}
	err := n.Send(server.URL, "", samplePayload())

	require.Error(t, err)
}

// TestDingTalkSend_BusinessErrorInBody verifies that a DingTalk robot
// response with HTTP 200 but a non-zero errcode in the body is treated as
// failure. DingTalk returns 200 with the real outcome encoded in
// {"errcode":...,"errmsg":...}.
func TestDingTalkSend_BusinessErrorInBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"errcode":130101,"errmsg":"send too fast"}`))
	}))
	defer server.Close()

	n := &DingTalkNotifier{}
	err := n.Send(server.URL, "", samplePayload())

	require.Error(t, err)
	assert.Contains(t, err.Error(), "130101")
	assert.Contains(t, err.Error(), "send too fast")
}

// TestDingTalkSend_Success verifies that a DingTalk robot response with
// errcode 0 is treated as success (positive control).
func TestDingTalkSend_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"errcode":0,"errmsg":"ok"}`))
	}))
	defer server.Close()

	n := &DingTalkNotifier{}
	err := n.Send(server.URL, "", samplePayload())

	assert.NoError(t, err)
}

// TestDingTalkSend_EmptyBody verifies that an empty 200 body cannot be
// confirmed as delivered, so Send must fail closed.
func TestDingTalkSend_EmptyBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	n := &DingTalkNotifier{}
	err := n.Send(server.URL, "", samplePayload())

	require.Error(t, err)
}

// TestDingTalkSend_NonJSONBody verifies that a non-JSON 200 body cannot be
// confirmed as delivered, so Send must fail closed.
func TestDingTalkSend_NonJSONBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("not json at all"))
	}))
	defer server.Close()

	n := &DingTalkNotifier{}
	err := n.Send(server.URL, "", samplePayload())

	require.Error(t, err)
}

// TestFeishuSend_BusinessErrorInBody verifies that a Feishu custom bot
// response with HTTP 200 but a non-zero code in the body is treated as
// failure. Feishu returns 200 with the real outcome encoded in
// {"code":...,"msg":...}.
func TestFeishuSend_BusinessErrorInBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"code":19001,"msg":"param invalid: incoming webhook access token invalid"}`))
	}))
	defer server.Close()

	n := &FeishuNotifier{}
	err := n.Send(server.URL, "", samplePayload())

	require.Error(t, err)
	assert.Contains(t, err.Error(), "19001")
	assert.Contains(t, err.Error(), "param invalid: incoming webhook access token invalid")
}

// TestFeishuSend_StatusCodeErrorInBody verifies that a Feishu response with
// code=0 but a non-zero legacy StatusCode is still treated as failure:
// Feishu's custom bot API must have both code and StatusCode be zero to
// count as a real success.
func TestFeishuSend_StatusCodeErrorInBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"code":0,"msg":"success","StatusCode":9499,"StatusMessage":"Bad Request"}`))
	}))
	defer server.Close()

	n := &FeishuNotifier{}
	err := n.Send(server.URL, "", samplePayload())

	require.Error(t, err)
	assert.Contains(t, err.Error(), "9499")
}

// TestFeishuSend_Success verifies that a Feishu response with both code and
// StatusCode 0 is treated as success (positive control).
func TestFeishuSend_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"StatusCode":0,"StatusMessage":"success","code":0,"msg":"success"}`))
	}))
	defer server.Close()

	n := &FeishuNotifier{}
	err := n.Send(server.URL, "", samplePayload())

	assert.NoError(t, err)
}

// TestFeishuSend_EmptyBody verifies that an empty 200 body cannot be
// confirmed as delivered, so Send must fail closed.
func TestFeishuSend_EmptyBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	n := &FeishuNotifier{}
	err := n.Send(server.URL, "", samplePayload())

	require.Error(t, err)
}

// TestFeishuSend_NonJSONBody verifies that a non-JSON 200 body cannot be
// confirmed as delivered, so Send must fail closed.
func TestFeishuSend_NonJSONBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("<html>not json</html>"))
	}))
	defer server.Close()

	n := &FeishuNotifier{}
	err := n.Send(server.URL, "", samplePayload())

	require.Error(t, err)
}
