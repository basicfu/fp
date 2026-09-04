package registry

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/basicfu/fp/internal/im/model"
	"github.com/basicfu/fp/internal/im/redisx"
	"github.com/redis/go-redis/v9"
)

// ConnRef 指向某条连接及其所在节点。
type ConnRef struct {
	ConnID string
	Node   string
}

type HandshakeResult struct {
	Rejected bool
	Kicked   []ConnRef
}

// Conns 读写每个 subject 的连接表 fp:im:{app:subject}:conn。
type Conns struct {
	c        redis.UniversalClient
	run      *redisx.Runner
	fieldTTL time.Duration
}

func NewConns(c redis.UniversalClient, run *redisx.Runner, fieldTTL time.Duration) *Conns {
	return &Conns{c: c, run: run, fieldTTL: fieldTTL}
}

// Handshake 跑握手脚本。脚本直接执行，不经写管道：它的返回值决定要不要接受连接，
// 攒批只会给握手加延迟而省不了什么。
func (c *Conns) Handshake(ctx context.Context, app, subject, connID string, meta model.ConnMeta, policy model.Policy, limit int, live []string) (HandshakeResult, error) {
	args := []interface{}{connID, meta.Encode(), string(policy), limit, int64(c.fieldTTL.Seconds())}
	// live 为空表示调用方没有存活节点信息（仅测试场景会这样调）：整段 ARGV[6..] 都不传，
	// 让脚本进入"跳过死节点清理"的分支。传一个只含自己的单节点列表看似更安全，
	// 实际效果是脚本会把 live 之外的所有节点都当成死的，误删同 subject 下别的节点的合法连接。
	if len(live) > 0 {
		for _, n := range live {
			args = append(args, n)
		}
		// 自己所在的节点必须在存活列表里，否则脚本会把刚登记的自己当残留删掉
		if !contains(live, meta.Node) {
			args = append(args, meta.Node)
		}
	}
	raw, err := handshakeScript.Run(ctx, c.c, []string{model.ConnKey(app, subject)}, args...).Slice()
	if err != nil {
		return HandshakeResult{}, fmt.Errorf("registry: 握手脚本: %w", err)
	}
	if len(raw) == 0 {
		return HandshakeResult{}, errors.New("registry: 握手脚本返回空")
	}
	if raw[0] == "REJECT" {
		return HandshakeResult{Rejected: true}, nil
	}
	var res HandshakeResult
	if len(raw) > 1 {
		flat, _ := raw[1].([]interface{})
		for i := 0; i+1 < len(flat); i += 2 {
			res.Kicked = append(res.Kicked, ConnRef{ConnID: str(flat[i]), Node: str(flat[i+1])})
		}
	}
	return res, nil
}

// Remove 在断开时删自己的 field。走写管道：结果没人等。
func (c *Conns) Remove(ctx context.Context, app, subject, connID string) error {
	return c.run.Run(ctx, func(p redis.Pipeliner) { p.HDel(ctx, model.ConnKey(app, subject), connID) })
}

// Lookup 返回 subject 的连接，已过滤掉不在 live 里的节点。
func (c *Conns) Lookup(ctx context.Context, app, subject string, live []string) (map[string]model.ConnMeta, error) {
	many, err := c.LookupMany(ctx, app, []string{subject}, live)
	if err != nil {
		return nil, err
	}
	return many[0], nil
}

// LookupMany 把多个 HGETALL 放进一次 Run，这是 PushMany 只花一次往返的来源。
func (c *Conns) LookupMany(ctx context.Context, app string, subjects []string, live []string) ([]map[string]model.ConnMeta, error) {
	cmds := make([]*redis.MapStringStringCmd, len(subjects))
	err := c.run.Run(ctx, func(p redis.Pipeliner) {
		for i, s := range subjects {
			cmds[i] = p.HGetAll(ctx, model.ConnKey(app, s))
		}
	})
	if err != nil {
		return nil, fmt.Errorf("registry: HGETALL: %w", err)
	}
	liveSet := map[string]bool{}
	for _, n := range live {
		liveSet[n] = true
	}
	out := make([]map[string]model.ConnMeta, len(subjects))
	for i, cmd := range cmds {
		m := map[string]model.ConnMeta{}
		for cid, raw := range cmd.Val() {
			meta, err := model.DecodeMeta(raw)
			if err != nil || !liveSet[meta.Node] {
				continue
			}
			m[cid] = meta
		}
		out[i] = m
	}
	return out, nil
}

func (c *Conns) KickAll(ctx context.Context, app, subject string) ([]ConnRef, error) {
	raw, err := kickAllScript.Run(ctx, c.c, []string{model.ConnKey(app, subject)}).Slice()
	if err != nil {
		return nil, fmt.Errorf("registry: kickAll: %w", err)
	}
	var refs []ConnRef
	for i := 0; i+1 < len(raw); i += 2 {
		meta, err := model.DecodeMeta(str(raw[i+1]))
		if err != nil {
			continue
		}
		refs = append(refs, ConnRef{ConnID: str(raw[i]), Node: meta.Node})
	}
	return refs, nil
}

func (c *Conns) KickOne(ctx context.Context, app, subject, connID string) (*ConnRef, error) {
	raw, err := kickOneScript.Run(ctx, c.c, []string{model.ConnKey(app, subject)}, connID).Result()
	if errors.Is(err, redis.Nil) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("registry: kickOne: %w", err)
	}
	meta, err := model.DecodeMeta(str(raw))
	if err != nil {
		return nil, nil
	}
	return &ConnRef{ConnID: connID, Node: meta.Node}, nil
}

// Renew 给本节点持有的 connIDs 续 field TTL。一个 subject 一条命令，走写管道。
func (c *Conns) Renew(ctx context.Context, app, subject string, connIDs []string) error {
	if len(connIDs) == 0 {
		return nil
	}
	return c.run.Run(ctx, func(p redis.Pipeliner) {
		p.HExpire(ctx, model.ConnKey(app, subject), c.fieldTTL, connIDs...)
	})
}

func str(v interface{}) string {
	s, _ := v.(string)
	return s
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}
