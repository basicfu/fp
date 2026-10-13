package domain

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
)

// AvatarMaxBytes 是头像的兜底上限。业务方有自己更严的限制，这条只防接入方漏了校验把大文件
// 灌进数据库，同时让请求留在 gRPC 默认 4 MB 消息上限之内。
const AvatarMaxBytes = 1 << 20

// avatarHashLen 是 AvatarHash 的十六进制长度（SHA-256 的前 16 字节）。
const avatarHashLen = 32

// AvatarHash 是图片原始字节的内容哈希。换图即换哈希，所以按哈希取到的内容永不变化，
// 业务方可以无限期缓存。
func AvatarHash(image []byte) string {
	sum := sha256.Sum256(image)
	return hex.EncodeToString(sum[:avatarHashLen/2])
}

// IsAvatarHash 报告 s 是否具有 AvatarHash 的形状。
func IsAvatarHash(s string) bool {
	if len(s) != avatarHashLen {
		return false
	}
	for _, c := range []byte(s) {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// CheckAvatar 校验头像并给出按文件头识别出的 Content-Type。
//
// 只看文件头、不解码图片：fp 对图片只存不处理。不信调用方声明的类型，是因为下载地址由业务方
// 公开，存了类型才能回正确的 Content-Type，也挡掉把任意文件当头像存的用法。
func CheckAvatar(image []byte) (contentType string, err error) {
	if len(image) > AvatarMaxBytes {
		return "", Failf(ErrInvalidArgument, CodeAvatarInvalid, "头像不能超过 %d MB", AvatarMaxBytes>>20)
	}
	switch {
	case bytes.HasPrefix(image, []byte{0xFF, 0xD8, 0xFF}):
		return "image/jpeg", nil
	case bytes.HasPrefix(image, []byte("\x89PNG\r\n\x1a\n")):
		return "image/png", nil
	case len(image) >= 12 && bytes.Equal(image[:4], []byte("RIFF")) && bytes.Equal(image[8:12], []byte("WEBP")):
		return "image/webp", nil
	}
	return "", Fail(ErrInvalidArgument, CodeAvatarInvalid, "头像只支持 JPEG、PNG、WebP 图片")
}
