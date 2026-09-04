# im-demo 手工验收

一个接入 fp-im 的最小业务服务：收到 client 发来的消息原样推回去，连接
上线/下线都打日志。跟 `examples/demo` 一样，它同时是接入文档（照抄
`main.go` 就是接入 fp-im 要写的全部代码）和手工验收工具。

## 前置条件

- fp 与 fp-im 都是本仓库自带的二进制，全部用 `go run` / bash 脚本起，
  不需要 Docker。
- 仓库根目录有 `.env.local`（从 `.env.example` 复制、填好 PG/Redis）。
- 本机没有 `wscat`/`websocat` 这类 ws 命令行工具，第 5、7、8 步改用本
  README 附带的一个几十行的小 Go 程序 `wsclient` 当验收工具，用的就是
  仓库自带的 `sdk/im` client（`fpim.Dial`）。

## 第 1 步：起 fp，建应用，启用登录方式

```bash
./scripts/run.sh
```

另开终端，用管理员登录、建应用、启用 `sms_code`（与 `examples/demo`
第 1 步完全一样，这里只是换了个应用名）：

```bash
ADMIN_TOKEN=$(curl -s -X POST http://localhost:8080/admin/api/login \
  -H 'Content-Type: application/json' -d '{"username":"admin","password":"admin123456"}' \
  | python3 -c 'import json,sys;print(json.load(sys.stdin)["token"])')

curl -s -X POST http://localhost:8080/admin/api/applications \
  -H "Authorization: Bearer $ADMIN_TOKEN" -H 'Content-Type: application/json' \
  -d '{"name":"ImDemo","slug":"imdemo"}'
# 记下 application.id / application.appId / appSecret

curl -s -o /dev/null -w '%{http_code}\n' \
  -X PUT "http://localhost:8080/admin/api/applications/<application.id>/connectors/sms_code" \
  -H "Authorization: Bearer $ADMIN_TOKEN" -H 'Content-Type: application/json' -d '{"enabled":true,"config":{}}'
# 该看到 204
```

**本机手工验证实测**：`application.appId` = `IDxzbrx-KKUaIYGm8AjKqP9363T8GlZQzLrnq8rDTj8`，
`appSecret` 只在建应用那一次响应里出现。

## 第 2 步：写网关的应用配置文件

```bash
mkdir -p tmp
cat > tmp/im-apps.json <<'EOF'
{"apps":[{"app_id":"<application.appId>","app_secret":"<appSecret>","allow_guest":true}]}
EOF
```

`allow_guest:true` 是为了第 8 步能测访客；`conn_policy` 不给会取默认值
`replace`（见 `internal/im/appcfg/file.go`），正好是第 7 步顶号要测的
那个策略。

## 第 3 步：起两个网关节点

```bash
FP_IM_HTTP_ADDR=:8081 FP_IM_GRPC_ADDR=:9091 ./scripts/run-im.sh   # 终端 A
FP_IM_HTTP_ADDR=:8082 FP_IM_GRPC_ADDR=:9092 ./scripts/run-im.sh   # 终端 B
```

**该看到**每个终端各打一行 `"fp-im 启动"`，带各自的 `node`/`http`/`grpc`。

## 第 4 步：起 im-demo，连第二个节点

```bash
FP_APP_ID=<application.appId> FP_APP_SECRET=<appSecret> FP_IM_ADDR=localhost:9092 \
  go run ./examples/im-demo
```

**该看到**：`INFO fpim: 流就绪 node=<im2 的 nodeId>`。

## 第 5 步：验收工具 wsclient

本机没有 ws 命令行工具，用下面这个几十行的小程序代替，保存为
`tmp/wsclient/main.go`：

```go
// wsclient 是手工验收用的一次性小工具：用仓库自带的 fpim client SDK 当
// "ws 命令行工具"用。
//
// 环境变量：
//   WS_URL   ws://host:port/ws
//   WS_APP   app_id
//   WS_TOKEN 或 WS_GUEST 二选一
//   WS_SEND  连接成功后要发送的 payload（合法 JSON），可选
//   WS_HOLD  连接后保持存活的时长，如 "3s"，默认 "3s"
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"time"

	fpim "github.com/basicfu/fp/sdk/im"
)

func main() {
	hold := 3 * time.Second
	if h := os.Getenv("WS_HOLD"); h != "" {
		var err error
		hold, err = time.ParseDuration(h)
		if err != nil {
			log.Fatal(err)
		}
	}
	cfg := fpim.ClientConfig{URL: os.Getenv("WS_URL"), App: os.Getenv("WS_APP")}
	if t := os.Getenv("WS_TOKEN"); t != "" {
		cfg.Token = t
	} else {
		cfg.Guest = os.Getenv("WS_GUEST")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	c, err := fpim.Dial(ctx, cfg)
	if err != nil {
		log.Fatalf("Dial 失败: %v", err)
	}
	fmt.Println("HANDSHAKE_OK")
	c.OnMessage(func(p []byte) { fmt.Printf("RECV %s\n", p) })
	c.OnClose(func(code int) { fmt.Printf("CLOSED code=%d\n", code) })
	if send := os.Getenv("WS_SEND"); send != "" {
		if err := c.Send(context.Background(), []byte(send)); err != nil {
			fmt.Printf("SEND_ERR %v\n", err)
		} else {
			fmt.Println("SENT")
		}
	}
	time.Sleep(hold)
	_ = c.Close()
}
```

```bash
go build -o tmp/wsclient/wsclient.exe ./tmp/wsclient   # Linux/macOS 去掉 .exe
```

登录拿一个 token（走法与 `examples/demo` 第 2 步相同：发验证码、从 fp
自己的 WARN 日志里抄 6 位验证码、登录换 token）。

## 第 6 步：握手、发消息、看回显

```bash
WS_URL="ws://localhost:8081/ws" WS_APP=<application.appId> WS_TOKEN=<上面的 token> \
  WS_SEND='{"hi":1}' WS_HOLD=3s ./tmp/wsclient/wsclient.exe
```

**本机实测输出**：

```
HANDSHAKE_OK
SENT
RECV {"hi":1}
```

im-demo 的日志里同时该看到：

```
INFO Connected subject=u:<userId> conn=<connId> os=other mobile=false reason=""
INFO 回显 subject=u:<userId> status=1 err=<nil>
```

（`status=1` 是 `fpim.Sent`。第 4 步连的是节点二、这次握手连的是节点一，
一来一回真的跨了一个节点转发。）

**已修复的坑，写给下一个人**：`main.go` 里 `OnMessage` 回调现在可以直接
同步调用 `srv.Push` 后等它返回，不需要另开 goroutine——`OnMessage`/
`OnEvent` 由 `Server` 内部一个独立的消费协程按入队顺序调用，不再是在
读 `Result` 应答的那条 gRPC 读循环里同步执行。早期实现不是这样：那时
`OnMessage` 就是在唯一一条读循环里同步执行的，而 `Push` 要等的 `Result`
回执恰好也是从这同一条流由同一条读循环读回来的，回调里同步调用 `Push`
会把读循环自己堵死，卡满 `RequestTimeout`（默认 5 秒）后才报
`ErrUnavailable`，而消息其实已经送达——这里记录下来是因为这个坑一度
非常容易在照抄示例时踩上，见 `sdk/im/server.go` 的
`TestServerPushInsideOnMessageDoesNotDeadlock`。

## 第 7 步：顶号

```bash
# 连节点一，保持存活 8 秒
WS_URL="ws://localhost:8081/ws" WS_APP=<appId> WS_TOKEN=<同一个 token> WS_HOLD=8s ./tmp/wsclient/wsclient.exe &
sleep 2
# 用同一个 token 连节点二
WS_URL="ws://localhost:8082/ws" WS_APP=<appId> WS_TOKEN=<同一个 token> WS_HOLD=3s ./tmp/wsclient/wsclient.exe
wait
```

**本机实测**：第一条连接的输出是

```
HANDSHAKE_OK
CLOSED code=4003
```

im-demo 日志里出现 `Disconnected ... reason=replaced`。`4003` 就是
`fpim.CloseKicked`（`sdk/im/client.go`），`replaced` 是断开原因常量
（`internal/im/model/frame.go` 的 `ReasonReplaced`）。

## 第 8 步：访客

```bash
GUEST_ID=$(node -e 'console.log(crypto.randomUUID())')   # 或用 uuidgen / powershell [guid]::NewGuid()
WS_URL="ws://localhost:8082/ws" WS_APP=<appId> WS_GUEST="$GUEST_ID" WS_SEND='{"g":1}' WS_HOLD=3s ./tmp/wsclient/wsclient.exe
```

**该看到**：`RECV {"g":1}`；im-demo 日志里 `subject` 是 `g:<GUEST_ID>`
前缀（不是 `u:`）。访客 id 必须是标准写法的 uuid v4，否则握手会被拒。

## 第 9 步：杀一个节点，判死与重连

```bash
# 先起一条长连接连节点一
WS_URL="ws://localhost:8081/ws" WS_APP=<appId> WS_GUEST=<新的 uuid v4> WS_HOLD=180s ./tmp/wsclient/wsclient.exe &

# 杀掉节点一（找到监听 8081 的进程），立刻原地重启
kill <节点一的 PID>              # 或 Windows: taskkill //F //PID <pid>
FP_IM_HTTP_ADDR=:8081 FP_IM_GRPC_ADDR=:9091 ./scripts/run-im.sh &
```

**本机实测**：杀掉节点后，那条 ws 连接立刻收到 `CLOSED code=-1`（TCP 被
直接切断，读不出正常的关闭码），随后每隔一小段递增的时间打一条
`fpim: 重连失败`（指数退避，直到节点一重新监听）。

判死靠 `fp:im:node` 表里的心跳时间戳，本 demo 没有直接暴露这张表，用
`Sessions` 间接验证：节点一死后 10 秒以上（`node.dead_after` 默认值），
对着**还活着的节点二**查这个访客的 `Sessions`，残留的连接条目应该已经
被过滤掉、返回空列表——本机实测确实如此。可以在 `im-demo` 里临时加一行
`http.HandleFunc` 调 `srv.Sessions` 验证，或者照抄下面这个更小的一次性
工具（`tmp/srvtool/main.go`，同样只用 `sdk/im`）：

```go
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"time"

	fpim "github.com/basicfu/fp/sdk/im"
)

func main() {
	srv, err := fpim.NewServer(fpim.ServerConfig{
		Addr: os.Getenv("SRV_ADDR"), AppID: os.Getenv("SRV_APP"), AppSecret: os.Getenv("SRV_SECRET"), Insecure: true,
	})
	if err != nil {
		log.Fatal(err)
	}
	defer srv.Close()
	time.Sleep(500 * time.Millisecond)
	sub := fpim.Guest(os.Getenv("SRV_ID")) // 或 fpim.User(...)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	sess, err := srv.Sessions(ctx, sub)
	fmt.Printf("Sessions(%s) = %+v err=%v\n", sub, sess, err)
}
```

节点一重新起来之后（等它打出 `"fp-im 启动"`），再查一次 `Sessions`：
本机实测能看到一条新的连接记录，`Node` 字段是节点一**新的** `nodeId`
（每次启动都是新 nodeId，见 `internal/im/model/nodeid.go`）——`wsclient`
那条长连接已经自动重连成功，不需要人工介入。
