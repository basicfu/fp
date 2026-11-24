package fpsdk

import (
	"context"
	"errors"
	"io"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// 推送流的几种断法要翻成人话: 对端重启(EOF)、被代理重置(RST_STREAM)、连不上、本端心跳判死主动重建。
func TestWatchEndReason(t *testing.T) {
	cases := []struct {
		err  error
		want string
	}{
		{io.EOF, "对端关闭了连接(多半是对端重启或发布)"},
		{errors.New("EOF"), "对端关闭了连接(多半是对端重启或发布)"},
		{status.Error(codes.Internal, "stream terminated by RST_STREAM with error code: PROTOCOL_ERROR"), "流被代理或对端重置(RST_STREAM,常见于代理空闲超时)"},
		{status.Error(codes.Unavailable, "connection error: desc = \"transport: Error while dialing: dial tcp 127.0.0.1:1: connect: connection refused\""), "连不上对端"},
		{context.Canceled, "本端主动重建流(心跳无应答)"},
		{status.Error(codes.Canceled, "context canceled"), "本端主动重建流(心跳无应答)"},
		{errors.New("boom"), "其他"},
	}
	for _, c := range cases {
		if got := watchEndReason(c.err); got != c.want {
			t.Errorf("%v: %q, 想要 %q", c.err, got, c.want)
		}
	}
}
