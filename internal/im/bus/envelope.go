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
	Type    byte
	Hops    uint8
	App     string
	Subject string
	ConnID  string
	Extra   string
	Payload []byte
}

// Encode 布局：type(1) hops(1) 然后五个长度前缀（uvarint）的字节串。
func (e Envelope) Encode() []byte {
	b := make([]byte, 0, 16+len(e.App)+len(e.Subject)+len(e.ConnID)+len(e.Extra)+len(e.Payload))
	b = append(b, e.Type, e.Hops)
	for _, s := range [][]byte{[]byte(e.App), []byte(e.Subject), []byte(e.ConnID), []byte(e.Extra), e.Payload} {
		b = binary.AppendUvarint(b, uint64(len(s)))
		b = append(b, s...)
	}
	return b
}

var ErrBadEnvelope = errors.New("bus: 信封损坏")

func Decode(b []byte) (Envelope, error) {
	if len(b) < 2 {
		return Envelope{}, ErrBadEnvelope
	}
	e := Envelope{Type: b[0], Hops: b[1]}
	rest := b[2:]
	fields := make([][]byte, 5)
	for i := range fields {
		n, k := binary.Uvarint(rest)
		if k <= 0 || uint64(len(rest)-k) < n {
			return Envelope{}, ErrBadEnvelope
		}
		fields[i] = rest[k : k+int(n)]
		rest = rest[k+int(n):]
	}
	if len(rest) != 0 {
		return Envelope{}, ErrBadEnvelope
	}
	e.App, e.Subject, e.ConnID, e.Extra = string(fields[0]), string(fields[1]), string(fields[2]), string(fields[3])
	e.Payload = append([]byte(nil), fields[4]...)
	return e, nil
}
