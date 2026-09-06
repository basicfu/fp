package fpsdk

import (
	"encoding/json"
	"fmt"
	"reflect"
	"sync"
	"sync/atomic"
)

// TypeBinding 是某个分区的全量配置快照，不绑定任何 struct，也没有
// MissingConfigError 那种"缺值"概念——它拉的是"这个分区当前有什么就是
// 什么"。并发安全。
//
// 它的用途是**转发给前端**：拿到 map 之后怎么送出去是业务方的事——挂个
// handler 吐 Load()、或挂 OnChange 用 WebSocket 推，都行。SDK 刻意不提供
// HTTP handler，也不让前端直连 fp（fp 是全局单点，前端流量的量级与 SDK
// 完全不同）。
type TypeBinding struct {
	c   *Client
	typ string

	// snap 是当前快照。用 atomic.Pointer 换指针而不是就地改 map：Load()
	// 拿到的必须是某一时刻的完整快照，不会看到"改了一半"的中间状态。
	snap atomic.Pointer[map[string]any]

	mu       sync.Mutex
	onChange func(old, new map[string]any)
	onError  func(error)
}

// BindType 拉取指定分区的全部配置值，解析成 JSON 原生类型后交给调用方。
//
// 分区**必填**，没有"不传就是全部"的重载：分区之间同名 key 是不同的
// 配置项，合并成一个 map 就得回答"撞了算谁的"，而这个问题不该存在——
// Bind 固定绑 ConfigTypeDefault，业务方转发给前端的场景用这个函数指定
// ConfigTypeWeb（或其他自定义分区）。
//
// 分区校验先于对 c 的任何解引用：调用方传 nil c 时（比如构造期校验、
// 或是编排代码里参数顺序写反了）也应该拿到一个清楚的 error 而不是
// 直接 panic。
//
// 与 Bind 不同，这里不存在"缺值"，因此没有 *MissingConfigError：某个
// key 在 fp 上没配就是没配，Load() 返回的 map 里不会有这个 key，没有
// 需要人另外去控制台补建的语义。
//
// 成功注册后，本 binding 会被 Client 记录下来，收到该分区的配置变更
// 推送（或推送流重连）时自动热更新，见 OnChange/OnError 与
// sdk/client.go 的 runConfigReload。
func BindType(c *Client, typ string) (*TypeBinding, error) {
	if typ == "" {
		return nil, fmt.Errorf("fpsdk: BindType 必须指定分区，如 fpsdk.ConfigTypeWeb")
	}
	values, err := c.fetchConfig(typ)
	if err != nil {
		return nil, err
	}
	b := &TypeBinding{c: c, typ: typ}
	// 首次加载复用 applySnapshot 而不是像 Binding[T].reload 那样单独一条
	// 路径：TypeBinding 没有"缺值"概念，首次加载与热重载要处理的事完全
	// 一样（解析、必要时报错），没有理由为它们分别维护一套逻辑。
	b.applySnapshot(values)
	c.registerBinding(b)
	return b, nil
}

// Load 返回当前快照的引用。返回的 map **不得修改**——它被所有 goroutine
// 共享，也可能在下一次热重载时被整个替换掉（但绝不会被就地改写）。
// 调用方需要独立副本时应自行深拷贝。
//
// 还没有成功应用过任何快照时（BindType 首次拉取即解析失败）返回非 nil
// 的空 map，调用方可以直接 range，不必判空。
func (b *TypeBinding) Load() map[string]any {
	m := b.snap.Load()
	if m == nil {
		return map[string]any{}
	}
	return *m
}

// OnChange 注册变更回调。只在快照**真的变了**时触发，old 与 new 都非 nil。
// 重复调用会替换掉上一个回调。
//
// 回调在 Client 唯一的重载 goroutine 里同步执行（见 runConfigReload），
// 会阻塞同一进程里其余绑定的重载，回调必须快。
func (b *TypeBinding) OnChange(fn func(old, new map[string]any)) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.onChange = fn
}

// OnError 注册错误回调。触发条件是某个 key 的值解析失败（fp 上存的不是
// 合法 JSON——正常链路里不会发生，属于防御性兜底）。这种情况下快照整份
// 保持原样，所以这件事**不**走 OnChange。
//
// 没注册时 SDK 打 ERROR 日志，不静默，见 raise。
func (b *TypeBinding) OnError(fn func(error)) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.onError = fn
}

// applySnapshot 是拿到 values 之后做的全部事情：解析、比较、必要时换
// 指针并回调，出错统一走 raise。首次加载（BindType）与热重载
// （reloadFromPush）走的是同一条路径，见 BindType 的注释。
//
// 测试直接调它（bindtype_test.go 的 applyForTest），绕开网络。
func (b *TypeBinding) applySnapshot(values map[string]json.RawMessage) {
	// 解析成 JSON 原生类型再交出去，而不是原样转发字符串——否则业务方
	// json.Encode 出来的是 {"feature.new": "true"}，前端还要再转一次。
	next := make(map[string]any, len(values))
	for k, raw := range values {
		var v any
		if err := json.Unmarshal(raw, &v); err != nil {
			// 与 Binding[T] 一致：宁可整份保持旧的，也不给半成品——哪怕
			// 只有一个 key 解析失败，也不能让 next 里另外几个已经解析
			// 成功的 key 半路替换掉旧快照。
			b.raise(fmt.Errorf("fpsdk: 配置项 %s 解析失败: %w", k, err))
			return
		}
		next[k] = v
	}

	// 内容没变就不换指针、不回调——同分区里别人改了你不关心的 key
	// 也会推给你，不比较的话别人配置一次你就重建一次。
	old := b.snap.Load()
	if old != nil && reflect.DeepEqual(*old, next) {
		return
	}
	b.snap.Store(&next)

	b.mu.Lock()
	fn := b.onChange
	b.mu.Unlock()
	if fn != nil && old != nil {
		fn(*old, next)
	}
}

// raise 把错误交给 OnError；没注册就打 ERROR 日志，绝不静默。
func (b *TypeBinding) raise(err error) {
	b.mu.Lock()
	fn := b.onError
	b.mu.Unlock()
	if fn != nil {
		fn(err)
		return
	}
	b.c.opts.Logger.Error("fpsdk: 配置重载出错且未注册 OnError", "type", b.typ, "err", err)
}

// reloadFromPush 实现 reloadable，让 Client 唯一的重载 goroutine 统一
// 驱动各份绑定（见 sdk/client.go 的 runConfigReload）。
func (b *TypeBinding) reloadFromPush() {
	values, err := b.c.fetchConfig(b.typ)
	if err != nil {
		b.raise(err)
		return
	}
	b.applySnapshot(values)
}
