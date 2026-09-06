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

// reloadFromPush 在收到该分区的配置变更推送时被 Client 调用。
//
// 这里先给一个正确但没有回调的版本：重新拉取并替换快照，出错时吞掉——
// Task 11 才会补上 onChange/onError 的接线，在那之前压根没有地方能把
// 这个 error 交出去，让推送处理的 goroutine 因此崩掉或阻塞更糟。
// reloadable 接口要求这个方法存在，Task 10 里 Bind 已经会把 *Binding[T]
// 注册进 Client，不给出最小实现的话根本编译不过。
func (b *Binding[T]) reloadFromPush() {
	_ = b.reload()
}

// Bind 拉取 DEFAULT 分区的配置并填进 T。
//
// 任意字段在 fp 上没有值时返回 *MissingConfigError，但 **binding 照样返回**，
// 缺失的字段留 Go 零值——SDK 不替业务方决定能不能带伤启动。
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

// reload 拉一次当前配置并替换快照。Task 11 会给它加上变更比较与回调。
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
		types := make(map[string]string, len(missing))
		for _, s := range b.specs {
			types[s.Key] = s.Type
		}
		return &MissingConfigError{Keys: missing, Types: types}
	}
	return nil
}

// fillStruct 按 specs 把 values 填进一个新的 T。
//
// 返回的 missing 是**升序**的全部缺失 key，一次给全。缺值不是 error——
// 是不是致命由调用方判断。类型对不上才是 error。
func fillStruct[T any](specs []fieldSpec, values map[string]json.RawMessage) (*T, []string, error) {
	out := new(T)
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
		if err := json.Unmarshal(raw, field.Addr().Interface()); err != nil {
			return nil, nil, fmt.Errorf("fpsdk: 配置项 %s 解析失败: %w", s.Key, err)
		}
	}
	// specs 已经是升序的（specsOf 保证），missing 因此天然升序。
	return out, missing, nil
}
