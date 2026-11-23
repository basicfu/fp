// 滑块验证码的线格式(GET /api/account/captcha 的响应体)。从业务方原实现逐字节搬来:
// 已发布客户端按这个格式解码,改一个字节老客户端就出不了题。
package captcha

import (
	"encoding/binary"
	"encoding/json"
	"errors"
)

// Captcha 是一道滑块题。线格式(大端 uint32 长度前缀):
//
//	[metaLen][meta JSON][bgLen][背景 JPEG][拼块 PNG(剩余全部)]
//
// 不用 base64 是为了省掉三分之一的体积。
type Captcha struct {
	ID    string `json:"id"`
	TileY int    `json:"tile_y"` // 拼块左上角 y(逻辑像素,600×320 坐标系)
	W     int    `json:"w"`
	H     int    `json:"h"`
	Tile  int    `json:"tile"` // 缺口外框边长
	Pad   int    `json:"pad"`  // 拼块 PNG 四周的投影外扩,贴图时左上角要减去它

	Bg      []byte `json:"-"`
	TilePNG []byte `json:"-"`
}

var ErrBadCaptcha = errors.New("captcha: 验证码数据损坏")

func EncodeCaptcha(c *Captcha) []byte {
	meta, _ := json.Marshal(c)
	out := make([]byte, 0, 8+len(meta)+len(c.Bg)+len(c.TilePNG))
	out = binary.BigEndian.AppendUint32(out, uint32(len(meta)))
	out = append(out, meta...)
	out = binary.BigEndian.AppendUint32(out, uint32(len(c.Bg)))
	out = append(out, c.Bg...)
	return append(out, c.TilePNG...)
}

func DecodeCaptcha(b []byte) (*Captcha, error) {
	seg := func() ([]byte, bool) {
		if len(b) < 4 {
			return nil, false
		}
		n := binary.BigEndian.Uint32(b)
		if uint64(n) > uint64(len(b)-4) {
			return nil, false
		}
		s := b[4 : 4+n]
		b = b[4+n:]
		return s, true
	}
	meta, ok := seg()
	if !ok {
		return nil, ErrBadCaptcha
	}
	var c Captcha
	if json.Unmarshal(meta, &c) != nil {
		return nil, ErrBadCaptcha
	}
	bg, ok := seg()
	if !ok || len(bg) == 0 || len(b) == 0 {
		return nil, ErrBadCaptcha
	}
	c.Bg, c.TilePNG = bg, b
	return &c, nil
}
