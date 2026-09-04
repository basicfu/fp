// Package bus 是 im 节点之间唯一的通信通道：Redis sharded pub/sub 上的节点私有频道。
package bus

import (
	"encoding/binary"
	"errors"
)

// 信封类型。
const (
	TypeMsg  byte = 1 // server→client 的推送，投给本地 Subject 的所有连接
	TypeUp   byte = 2 // client→server 的消息，交给本地 server 流
	TypeEvt  byte = 3 // 连接事件，同 TypeUp 路由，Payload 是 model.EventBody 的 JSON
	TypeKick byte = 4 // 踢掉本地某条连接，Extra 是 reason
)

// Envelope 是节点频道上的一条消息。payload 本身是 JSON，所以信封不再用 JSON：
// 再包一层就得 base64，体积多三分之一。
type Envelope struct {
	Type byte
	// Hop 是游标，指向 Route 里"当前正在尝试"的位置：Hub.Deliver 每转发一次
	// 就把它设成"目标在 Route 里的下标 + 1"再发出去，这样收到信封的下游节点
	// 知道该从哪一位继续试，而不是从头重新试一遍已经确认没有订阅者的节点。
	// 只增不减、且 Route 长度固定，转发不可能无限循环。
	Hop     uint8
	App     string
	Subject string
	ConnID  string
	Extra   string
	// Route 是候选节点列表，按 rendezvous 权重降序排列，已排除首发节点自己。
	// 只在首发节点计算一次并写进信封，下游节点直接沿用、不重算：如果每跳都
	// 重算，各节点看到的存活快照可能不一致，消息会在节点间来回打转。
	// 只在 TypeUp 与 TypeEvt 上有值；TypeMsg 与 TypeKick 是定向投递，
	// Route 为空、Hop 为 0。
	Route   []string
	Payload []byte
}

// Encode 布局：type(1) hop(1)，然后 app/subject/connID/extra 四个长度前缀
// （uvarint）的字节串，接着 Route（先一个 uvarint 元素个数，再逐个长度前缀
// 的字节串），最后 payload。Route 放在 payload 之前而不是最后：payload 长度
// 通常远大于 Route，把变长的路由段夹在两个定长可预期的字段之间，Decode 读取
// 顺序与这里的写入顺序严格一一对应，不需要额外的长度字段区分"这是路由还是
// payload"。
func (e Envelope) Encode() []byte {
	size := 16 + len(e.App) + len(e.Subject) + len(e.ConnID) + len(e.Extra) + len(e.Payload)
	for _, n := range e.Route {
		size += len(n) + 10
	}
	b := make([]byte, 0, size)
	b = append(b, e.Type, e.Hop)
	for _, s := range [][]byte{[]byte(e.App), []byte(e.Subject), []byte(e.ConnID), []byte(e.Extra)} {
		b = binary.AppendUvarint(b, uint64(len(s)))
		b = append(b, s...)
	}
	b = binary.AppendUvarint(b, uint64(len(e.Route)))
	for _, n := range e.Route {
		b = binary.AppendUvarint(b, uint64(len(n)))
		b = append(b, n...)
	}
	b = binary.AppendUvarint(b, uint64(len(e.Payload)))
	b = append(b, e.Payload...)
	return b
}

var ErrBadEnvelope = errors.New("bus: 信封损坏")

func Decode(b []byte) (Envelope, error) {
	if len(b) < 2 {
		return Envelope{}, ErrBadEnvelope
	}
	e := Envelope{Type: b[0], Hop: b[1]}
	rest := b[2:]

	readBytes := func() ([]byte, bool) {
		n, k := binary.Uvarint(rest)
		if k <= 0 || uint64(len(rest)-k) < n {
			return nil, false
		}
		out := rest[k : k+int(n)]
		rest = rest[k+int(n):]
		return out, true
	}

	fields := make([][]byte, 4)
	for i := range fields {
		f, ok := readBytes()
		if !ok {
			return Envelope{}, ErrBadEnvelope
		}
		fields[i] = f
	}
	e.App, e.Subject, e.ConnID, e.Extra = string(fields[0]), string(fields[1]), string(fields[2]), string(fields[3])

	routeLen, k := binary.Uvarint(rest)
	if k <= 0 {
		return Envelope{}, ErrBadEnvelope
	}
	rest = rest[k:]
	// 每个 Route 元素编码后至少占 1 字节（哪怕字符串为空，长度前缀本身也要
	// 占一个 uvarint 字节），所以 routeLen 不可能超过剩余字节数。这里先拒绝
	// 显然不可能成立的元素个数，避免下面 make() 用一个被篡改的巨大 routeLen
	// 当容量、在真正读到不够字节报错之前就先把内存分配爆掉。
	if routeLen > uint64(len(rest)) {
		return Envelope{}, ErrBadEnvelope
	}
	if routeLen > 0 {
		e.Route = make([]string, 0, routeLen)
		for i := uint64(0); i < routeLen; i++ {
			f, ok := readBytes()
			if !ok {
				return Envelope{}, ErrBadEnvelope
			}
			e.Route = append(e.Route, string(f))
		}
	}

	payload, ok := readBytes()
	if !ok {
		return Envelope{}, ErrBadEnvelope
	}
	e.Payload = append([]byte(nil), payload...)

	if len(rest) != 0 {
		return Envelope{}, ErrBadEnvelope
	}
	return e, nil
}
