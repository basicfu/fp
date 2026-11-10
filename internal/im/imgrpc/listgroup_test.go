package imgrpc

import (
	"context"
	"errors"
	"fmt"
	"net"
	"slices"
	"testing"
	"time"

	"github.com/basicfu/fp/internal/im/hub/hubtest"
	"github.com/basicfu/fp/internal/im/model"
	"github.com/basicfu/fp/internal/im/registry"
	fpimv1 "github.com/basicfu/fp/sdk/gen/fpim/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
)

// listGroupStream 起一个装了 ListGroup 的 Server，返回已收过 Ready 的流。
func listGroupStream(t *testing.T, lg func(ctx context.Context, app, group string) ([]registry.SubjectConns, error)) fpimv1.ImService_ConnectClient {
	t.Helper()
	h, _, _, _, _ := hubtest.NewHub()
	srv := New(Deps{Hub: h, Creds: &fakeVerifier{ok: map[string]string{"a1": "s"}}, ListGroup: lg})
	lis := bufconn.Listen(1 << 20)
	go srv.Serve(lis)
	t.Cleanup(func() { srv.Stop(context.Background()) })
	cc, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return lis.DialContext(ctx) }),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cc.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	stream, err := fpimv1.NewImServiceClient(cc).Connect(authed(ctx, "a1", "s"))
	if err != nil {
		t.Fatal(err)
	}
	if first, err := stream.Recv(); err != nil || first.GetReady() == nil {
		t.Fatalf("第一帧应是 Ready，实际 %+v %v", first, err)
	}
	return stream
}

func askListGroup(t *testing.T, stream fpimv1.ImService_ConnectClient, id, group string) *fpimv1.Result {
	t.Helper()
	if err := stream.Send(&fpimv1.ConnectRequest{ReqId: id, Body: &fpimv1.ConnectRequest_ListGroup{ListGroup: &fpimv1.ListGroupRequest{Group: group}}}); err != nil {
		t.Fatal(err)
	}
	for {
		resp, err := stream.Recv()
		if err != nil {
			t.Fatal(err)
		}
		if r := resp.GetResult(); r != nil && r.ReqId == id {
			return r
		}
	}
}

func TestConnectListGroup(t *testing.T) {
	var gotApp, gotGroup string
	stream := listGroupStream(t, func(_ context.Context, app, group string) ([]registry.SubjectConns, error) {
		gotApp, gotGroup = app, group
		return []registry.SubjectConns{
			{UID: "a:b", Conns: map[string]model.ConnMeta{"c1": {Node: "im-a", OS: "linux", At: 7}}},
			{UID: "x", Conns: map[string]model.ConnMeta{"c2": {Node: "im-a"}, "c3": {Node: "im-b", Mobile: true}}},
		}, nil
	})

	r := askListGroup(t, stream, "g1", "r")
	if r.Error != "" || gotApp != "a1" || gotGroup != "r" {
		t.Fatalf("ListGroup 应按流的 app 查 r 组，实际 app=%q group=%q err=%q", gotApp, gotGroup, r.Error)
	}
	var subs []string
	conns := map[string][]string{}
	for _, m := range r.GroupMembers {
		subs = append(subs, m.Subject)
		for _, s := range m.Sessions {
			conns[m.Subject] = append(conns[m.Subject], s.ConnId+"@"+s.NodeId)
		}
		slices.Sort(conns[m.Subject])
	}
	slices.Sort(subs)
	// subject 必须写成完整的 b.<group>:<uid>，业务方拿去 Push/Kick 不用自己拼
	if !slices.Equal(subs, []string{"b.r:a:b", "b.r:x"}) {
		t.Fatalf("group_members 的 subject 应是 b.r:<uid>，实际 %v", subs)
	}
	if !slices.Equal(conns["b.r:x"], []string{"c2@im-a", "c3@im-b"}) || !slices.Equal(conns["b.r:a:b"], []string{"c1@im-a"}) {
		t.Fatalf("连接没有原样带回：%v", conns)
	}
	if len(r.SessionsMany) != 0 {
		t.Fatal("ListGroup 的结果只放 group_members，不能混进 sessions_many")
	}
}

func TestConnectListGroupRejectsBadGroup(t *testing.T) {
	called := false
	stream := listGroupStream(t, func(context.Context, string, string) ([]registry.SubjectConns, error) {
		called = true
		return nil, nil
	})
	for i, g := range []string{"", "R", "a-b"} {
		if r := askListGroup(t, stream, string(rune('a'+i)), g); r.Error == "" {
			t.Fatalf("分组 %q 非法，应以 Result.Error 返回", g)
		}
	}
	if called {
		t.Fatal("分组名非法时不该去查 Redis")
	}
}

func TestConnectListGroupPropagatesError(t *testing.T) {
	stream := listGroupStream(t, func(_ context.Context, _, group string) ([]registry.SubjectConns, error) {
		if group == "big" {
			return nil, fmt.Errorf("包了一层: %w", registry.ErrGroupTooLarge)
		}
		return nil, errors.New("redis 挂了")
	})
	// 分组过大回固定文案：SDK 按它映射成 fpim.ErrGroupTooLarge，不能带上 registry 的前缀或包装
	if r := askListGroup(t, stream, "g1", "big"); r.Error != errGroupTooLarge {
		t.Fatalf("分组过大应回 %q，实际 %q", errGroupTooLarge, r.Error)
	}
	if r := askListGroup(t, stream, "g2", "r"); r.Error == "" || r.Error == errGroupTooLarge {
		t.Fatalf("其它错误应原样带回，实际 %q", r.Error)
	}
}

// 没装配 ListGroup 时按"未知请求类型"回，与旧版 fp-im 同一口径，SDK 映射成 ErrUnsupported。
func TestConnectListGroupUnwired(t *testing.T) {
	stream := listGroupStream(t, nil)
	if r := askListGroup(t, stream, "g1", "r"); r.Error != errUnknownRequest {
		t.Fatalf("未装配时应回 %q，实际 %q", errUnknownRequest, r.Error)
	}
}
