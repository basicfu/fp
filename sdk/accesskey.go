package fpsdk

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/basicfu/fp/sdk/aksign"
	fpv1 "github.com/basicfu/fp/sdk/gen/fp/v1"
)

// signatureWindow 是请求时间戳与服务器时间允许的最大偏差。
const signatureWindow = 15 * time.Minute

// verifyAccessKey 校验带 X-Fp-Access-Key 的请求，顺序见 spec 第七节。
// 通过时 r.Body 已换成读过的副本，handler 能读到原字节。
func (a *Auth) verifyAccessKey(r *http.Request) (*Identity, error) {
	akID := r.Header.Get(aksign.HeaderAccessKey)
	nonce := r.Header.Get(aksign.HeaderNonce)
	ts, tsErr := strconv.ParseInt(r.Header.Get(aksign.HeaderTimestamp), 10, 64)
	sig, sigErr := hex.DecodeString(r.Header.Get(aksign.HeaderSignature))
	if tsErr != nil || sigErr != nil || len(sig) != sha256.Size || !aksign.ValidNonce(nonce) {
		return nil, akErr(ErrUnauthorized, CodeSignatureInvalid, "签名请求头缺失或格式不正确")
	}
	now := a.cache.now()
	if d := now.Sub(time.Unix(ts, 0)); d > signatureWindow || d < -signatureWindow {
		return nil, akErr(ErrUnauthorized, CodeTimestampExpired, "时间戳与服务器时间相差超过 15 分钟")
	}

	info, roles, stale, err := a.accessKey(r.Context(), akID)
	if err != nil {
		return nil, err
	}

	body, err := readSignedBody(r, a.c.opts.MaxSignedBodyBytes)
	if err != nil {
		return nil, err
	}
	requestURI := r.RequestURI
	if requestURI == "" {
		requestURI = r.URL.RequestURI()
	}
	path, query := aksign.SplitRequestURI(requestURI)
	sts := aksign.StringToSign(r.Method, path, query, ts, nonce, body)
	want, _ := hex.DecodeString(aksign.Signature(info.secret, sts))
	if !hmac.Equal(want, sig) {
		e := akErr(ErrUnauthorized, CodeSignatureMismatch, "签名不匹配")
		e.StringToSign = sts
		return nil, e
	}

	// 签名通过之后才记 nonce：伪造请求不该占内存，也不该让真实请求被误判成重放。
	switch err := a.c.nonces.add(akID+":"+nonce, ts+int64(signatureWindow/time.Second), now.Unix()); {
	case errors.Is(err, errNonceUsed):
		return nil, akErr(ErrUnauthorized, CodeNonceUsed, "nonce 已经使用过")
	case err != nil:
		return nil, errors.Join(ErrUnavailable, err)
	}

	if len(info.allowed) > 0 {
		ip, ok := clientIP(r)
		if !ok || !prefixesContain(info.allowed, ip) {
			a.c.opts.Logger.Warn("fpsdk: 访问密钥的来源 IP 不在白名单", "accessKeyId", akID, "ip", ip.String())
			return nil, akErr(ErrForbidden, CodeIPDenied, "来源 IP 不在白名单内")
		}
	}
	return &Identity{AccessKeyID: info.id, AccessKeyRemark: info.remark, Roles: roles, Stale: stale}, nil
}

// accessKey 取 key 的校验材料：本地缓存优先，未命中回源 GetAccessKey。缓存规则与 Validate 相同。
func (a *Auth) accessKey(ctx context.Context, akID string) (*accessKeyEntry, []string, bool, error) {
	var maxTTL, maxStale time.Duration
	if !a.c.StreamHealthy() {
		maxTTL = a.c.opts.DegradedCacheTTL
	}
	if a.c.opts.AllowStaleOnOutage {
		maxStale = a.c.opts.MaxStaleness
	}
	key := accessKeyCacheKey(akID)
	if e, st := a.cache.get(key, maxTTL, maxStale); st == cacheFresh && e.accessKey != nil {
		return e.accessKey, e.roles, false, nil
	}

	v, err, _ := a.akSF.Do(key, func() (any, error) {
		gen := a.cache.generation()
		callCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), a.c.opts.ValidateTimeout)
		defer cancel()
		res, err := a.c.rpc.GetAccessKey(callCtx, &fpv1.GetAccessKeyRequest{AccessKeyId: akID})
		if err != nil {
			return nil, err
		}
		allowed := make([]netip.Prefix, 0, len(res.GetAllowedIps()))
		for _, s := range res.GetAllowedIps() {
			p, perr := netip.ParsePrefix(s)
			if perr != nil {
				return nil, fmt.Errorf("fpsdk: fp 下发的 IP 白名单 %q 无法解析: %w", s, perr)
			}
			allowed = append(allowed, p)
		}
		e := entry{roles: res.GetRoles(), accessKey: &accessKeyEntry{
			id: akID, secret: res.GetSecret(), remark: res.GetRemark(), allowed: allowed,
		}}
		a.cache.putIfGen(key, e, time.Duration(res.GetCacheTtlMs())*time.Millisecond, gen)
		return e, nil
	})
	if err == nil {
		e := v.(entry)
		return e.accessKey, e.roles, false, nil
	}

	// fp 给出的确定答案按错误码分 401 / 403，不走 gRPC code（translate 会把 PermissionDenied 归成 401）。
	var fe *Error
	if errors.As(translate(err), &fe) {
		switch fe.Code {
		case CodeAccessKeyDisabled:
			return nil, nil, false, akErr(ErrForbidden, fe.Code, "AccessKey 已停用")
		case CodeAccessKeyExpired:
			return nil, nil, false, akErr(ErrForbidden, fe.Code, "AccessKey 已过期")
		}
	}
	switch status.Code(err) {
	case codes.Unauthenticated, codes.PermissionDenied, codes.NotFound:
		return nil, nil, false, akErr(ErrUnauthorized, CodeAccessKeyInvalid, "AccessKey 无效")
	}
	if maxStale > 0 {
		if e, st := a.cache.get(key, maxTTL, maxStale); st == cacheStale && e.accessKey != nil {
			return e.accessKey, e.roles, true, nil
		}
	}
	return nil, nil, false, errors.Join(ErrUnavailable, err)
}

// readSignedBody 读出 body 用于算摘要，并把副本放回 r.Body。
func readSignedBody(r *http.Request, limit int64) ([]byte, error) {
	if r.Body == nil || r.Body == http.NoBody {
		return nil, nil
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, limit+1))
	_ = r.Body.Close()
	if err != nil {
		return nil, akErr(ErrUnauthorized, CodeSignatureInvalid, "读取请求体失败")
	}
	if int64(len(body)) > limit {
		return nil, akErr(ErrBodyTooLarge, CodeBodyTooLarge, "请求体超过签名校验的大小上限")
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	return body, nil
}

// clientIP 取来源 IP：X-Forwarded-For 的第一个地址，没有这个头时取 RemoteAddr。
// 部署要求最外层代理用真实客户端地址覆盖 X-Forwarded-For，见 docs/access-key.md。
func clientIP(r *http.Request) (netip.Addr, bool) {
	raw := r.RemoteAddr
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		first, _, _ := strings.Cut(xff, ",")
		raw = strings.TrimSpace(first)
	} else if host, _, err := net.SplitHostPort(raw); err == nil {
		raw = host
	}
	addr, err := netip.ParseAddr(raw)
	if err != nil {
		return netip.Addr{}, false
	}
	return addr.Unmap(), true
}

func prefixesContain(prefixes []netip.Prefix, addr netip.Addr) bool {
	for _, p := range prefixes {
		if p.Contains(addr) {
			return true
		}
	}
	return false
}
