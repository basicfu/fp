package fpsdk

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// MissingConfigError 表示有配置项在 fp 上还没有值。
//
// **它是人在控制台建配置项的唯一依据**（本模块不做 SDK 上报），所以必须
// 一次列全、且带上每一项该建成什么类型——漏一项就要多跑一轮"起→失败"。
type MissingConfigError struct {
	// Keys 是全部缺失的配置项，升序。
	Keys []string
	// Types 是每个 key 对应的 fp 类型，人照着它在控制台选类型。
	Types map[string]string
}

func (e *MissingConfigError) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "fpsdk: %d 个配置项尚未在 fp 上配置，请到控制台创建并填值：", len(e.Keys))
	for _, k := range e.Keys {
		fmt.Fprintf(&b, "\n  %-24s (%s)", k, e.Types[k])
	}
	return b.String()
}

// asMissing 是 errors.As 的一层包装，只是让 Bind 里的判定读起来像一句话。
func asMissing(err error, target **MissingConfigError) bool {
	return errors.As(err, target)
}

// Binding 是一份绑定到 T 的配置快照。并发安全。
type Binding[T any] struct {
	c     *Client
	typ   string
	specs []fieldSpec

	// snap 是当前快照。用 atomic.Pointer 换指针而不是就地改字段：
	// Load() 拿到的必须是**跨字段一致**的一份，一次请求内不会出现
	// A 字段是新值、B 字段是旧值。
	snap atomic.Pointer[T]

	mu       sync.Mutex
	onChange func(old, new *T)
	onError  func(error)
}

// Load 返回当前快照。返回的指针指向的内容**不得修改**——它被所有
// goroutine 共享。
func (b *Binding[T]) Load() *T { return b.snap.Load() }

// Bind 拉取 DEFAULT 分区的配置并填进 T。
//
// 任意字段在 fp 上没有值时返回 *MissingConfigError，但 **binding 照样返回**，
// 缺失的字段留 Go 零值——SDK 不替业务方决定能不能带伤启动。
//
// 成功注册后，本 binding 会被 Client 记录下来，收到该进程关心的配置变更
// 推送（或推送流重连）时自动热更新，见 OnChange/OnError 与
// sdk/client.go 的 runConfigReload。
func Bind[T any](c *Client) (*Binding[T], error) {
	var zero T
	specs, err := specsOf(reflect.TypeOf(zero))
	if err != nil {
		return nil, err
	}
	b := &Binding[T]{c: c, typ: ConfigTypeDefault, specs: specs}
	if err := b.reload(); err != nil {
		var miss *MissingConfigError
		if !asMissing(err, &miss) {
			return nil, err
		}
		c.registerBinding(b)
		return b, err
	}
	c.registerBinding(b)
	return b, nil
}

// reload 是 Bind 专用的首次加载路径：拉一次当前配置、从零值填充并存下
// 第一份快照，返回值供 Bind 判断这份 binding 是否可用。
//
// 刻意不复用 applySnapshot（热重载用，见下文）：首次加载没有旧快照可
// 比较，"key 消失"这个概念本身就不成立——消失是相对于此前存在过的值
// 而言的。缺项在这里唯一的报告渠道就是把 *MissingConfigError 作为返回值
// 交给 Bind；这时 OnError 还来不及注册（业务方要先拿到 Bind 返回的
// *Binding[T] 才能调 OnError），如果改走 applySnapshot/raise，缺配置的
// 首次加载会在"返回 MissingConfigError"之外，被迫再强制打一条 ERROR
// 日志——同一件事汇报两遍。
func (b *Binding[T]) reload() error {
	values, err := b.c.fetchConfig(b.typ)
	if err != nil {
		return err
	}
	snap, missing, err := fillStruct[T](b.specs, values)
	if err != nil {
		return err
	}
	b.snap.Store(snap)
	if len(missing) > 0 {
		return &MissingConfigError{Keys: missing, Types: b.typeMap()}
	}
	return nil
}

// OnChange 注册变更回调。只在快照**真的变了**时触发，old 与 new 都非 nil。
// 重复调用会替换掉上一个回调。
//
// 回调在 Client 唯一的重载 goroutine 里同步执行（见 runConfigReload），
// 会阻塞同一进程里其余绑定的重载，回调必须快。
func (b *Binding[T]) OnChange(fn func(old, new *T)) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.onChange = fn
}

// OnError 注册错误回调。触发条件是热重载路径上的两种情况：某个 key 消失
// （被删或回滚导致），或解析失败（有人把字段类型改错）。两种情况下快照
// 都保持原样——所以这些事**不**走 OnChange。
//
// 没注册时 SDK 打 ERROR 日志，不静默，见 raise。
//
// 首次 Bind 时遇到的缺配置**不**走这里——那属于"从未配置过"，由 Bind
// 的返回值（*MissingConfigError）单独表达，见 reload 的注释。
func (b *Binding[T]) OnError(fn func(error)) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.onError = fn
}

// applySnapshot 是拿到 values 之后做的全部事情：应用、比较、必要时换
// 指针并回调，出错统一走 raise。这是热重载专用路径——调用它之前一定
// 已经有一份旧快照（Bind 把 binding 注册进 Client 之前必然先成功
// reload 过一次），"key 消失""解析失败"在这里都是相对旧快照的真实变化，
// 值得通过 OnError 报出来；Bind 的首次加载没有这个前提，走的是 reload
// 那条独立路径（见其注释）。
//
// 测试直接调它（config_test.go 的 applyForTest），绕开网络：把 snap
// 预置好之后，这是"重载"这整个行为唯一的入口，不需要起 gRPC 桩就能钉住
// 硬约束。
func (b *Binding[T]) applySnapshot(values map[string]json.RawMessage) {
	old := b.snap.Load()
	next, missing, err := applyValues(old, b.specs, values)
	if err != nil {
		// 解析失败：整份旧快照原样留着，一个字段都不动。
		b.raise(err)
		return
	}
	if len(missing) > 0 {
		// key 消失：上面的 applyValues 已经用 old 兜底、让该字段保持了
		// 旧值，这里只负责报错，不影响下面是否换指针的判断。
		b.raise(&MissingConfigError{Keys: missing, Types: b.typeMap()})
	}
	// 内容没变就不换指针、不回调——同分区里别人改了你不关心的 key
	// 也会推给你，不比较的话别人配置一次你的连接池就重建一次。
	if old != nil && reflect.DeepEqual(*old, *next) {
		return
	}
	b.snap.Store(next)

	b.mu.Lock()
	fn := b.onChange
	b.mu.Unlock()
	if fn != nil && old != nil {
		fn(old, next)
	}
}

// raise 把错误交给 OnError；没注册就打 ERROR 日志，绝不静默。
func (b *Binding[T]) raise(err error) {
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
// 驱动各份绑定（见 sdk/client.go 的 runConfigReload）。刻意不给
// partition() 之类按分区筛选的方法——重载是无差别的，一个只会被写进
// 接口却没人调的方法只会误导下一个人。
func (b *Binding[T]) reloadFromPush() {
	values, err := b.c.fetchConfig(b.typ)
	if err != nil {
		b.raise(err)
		return
	}
	b.applySnapshot(values)
}

// typeMap 返回 specs 到 fp 类型的映射，供 MissingConfigError.Types 用。
// reload（首次加载）与 applySnapshot（热重载）在各自的"缺配置/key 消失"
// 分支里都要构造 MissingConfigError，靠它避免重复"specs 循环拼 map"
// 这段样板。
func (b *Binding[T]) typeMap() map[string]string {
	types := make(map[string]string, len(b.specs))
	for _, s := range b.specs {
		types[s.Key] = s.Type
	}
	return types
}

// applyValues 在 base 的副本上按 specs 应用 values，返回新快照与缺失的 key。
//
// 从 base 出发而不是从零值出发，是"key 消失时保持旧值"这条规则的执行点：
// 重载时 base 是当前快照，values 里没有的 key 就原样保留旧值；首次绑定时
// base 是 nil，效果与"填不上就留零值"一致。
//
// 任何一个字段解析失败就整体返回 error，**绝不返回半成品**——调用方拿到
// error 后保持整份旧快照，跨字段一致性因此不会被破坏。
func applyValues[T any](base *T, specs []fieldSpec, values map[string]json.RawMessage) (*T, []string, error) {
	out := new(T)
	if base != nil {
		*out = *base // 浅拷贝：先假定全部字段都不变，下面按 values 逐个覆盖。
	}
	v := reflect.ValueOf(out).Elem()

	var missing []string
	for _, s := range specs {
		raw, ok := values[s.Key]
		if !ok {
			missing = append(missing, s.Key)
			continue
		}
		field := v.FieldByIndex(s.Index)

		if s.Duration {
			// 拉到的整数按**毫秒**解释。fp 侧不知道 duration 这回事，
			// 单位约定只活在这一行和文档里。
			var ms int64
			if err := json.Unmarshal(raw, &ms); err != nil {
				return nil, nil, fmt.Errorf("fpsdk: 配置项 %s 解析失败: %w", s.Key, err)
			}
			field.SetInt(int64(time.Duration(ms) * time.Millisecond))
			continue
		}

		// slice/map 是引用类型：上面 `*out = *base` 的浅拷贝让 field 此刻
		// 与 base 的同名字段共享同一份底层数组/map。encoding/json 解码
		// slice 时若容量够用会原地复用旧数组，解码 map 时会直接在已有
		// 对象上增删键——两者都会连带改写 base（也就是旧快照，此刻可能
		// 正被别的 goroutine 通过 Load() 持有并读取，被约定为不可变）。
		// 解码前先置零切断共享，让 Unmarshal 总是分配全新的底层存储。
		if k := field.Kind(); k == reflect.Slice || k == reflect.Map {
			field.Set(reflect.Zero(field.Type()))
		}
		if err := json.Unmarshal(raw, field.Addr().Interface()); err != nil {
			return nil, nil, fmt.Errorf("fpsdk: 配置项 %s 解析失败: %w", s.Key, err)
		}
	}
	// specs 已经是升序的（specsOf 保证），missing 因此天然升序。
	return out, missing, nil
}

// fillStruct 是 Bind 首次绑定用的入口：从零值出发。
func fillStruct[T any](specs []fieldSpec, values map[string]json.RawMessage) (*T, []string, error) {
	return applyValues[T](nil, specs, values)
}
