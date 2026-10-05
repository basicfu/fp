// Package notify 是 fp 通知的分发层：模板渲染与校验、各渠道的供应商实现。
// 不碰数据库——供应商实例与模板的持久化、发送编排在 internal/service/notify*.go。
package notify

import (
	"bytes"
	"encoding/json"
	"html"
	"net/url"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/basicfu/fp/internal/domain"
)

var (
	// 占位符是 {name}，name 限标识符。JSON 模板里的 {"k": ...} 花括号后面是引号，
	// 不匹配这个形状，不会被误认成占位符。
	placeholderRE = regexp.MustCompile(`\{([A-Za-z_][A-Za-z0-9_]*)\}`)
	identRE       = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
)

// ExtractPlaceholders 返回 s 里 {name} 占位符的名字，去重，按首次出现的顺序。
func ExtractPlaceholders(s string) []string {
	var out []string
	seen := map[string]bool{}
	for _, m := range placeholderRE.FindAllStringSubmatch(s, -1) {
		if !seen[m[1]] {
			seen[m[1]] = true
			out = append(out, m[1])
		}
	}
	return out
}

// 各目标格式的转义。替换值进了什么上下文就按什么转义，否则一个变量值就能弄坏
// JSON 报文、注入 HTML、或者往 URL 里多塞一个参数。
func escapeNone(s string) string { return s }

func escapeHTML(s string) string { return html.EscapeString(s) }

// escapeQuery 用 %20 而不是 +：并非所有接收端都把 + 当空格。
func escapeQuery(s string) string {
	return strings.ReplaceAll(url.QueryEscape(s), "+", "%20")
}

// escapeJSON 返回可以直接放进 JSON 字符串字面量里的文本（不含两侧引号）。
func escapeJSON(s string) string {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(s)
	out := strings.TrimSuffix(buf.String(), "\n")
	return out[1 : len(out)-1]
}

// Substitute 把 s 里的 {name} 换成 params[name]（经 esc 转义）。params 里没有的名字
// 原样保留——调用前已经用 ValidateParams 保证键集合与变量一致。
//
// 单趟替换：替换进去的值里即使带着 {other}，也不会被再次当成占位符。
func Substitute(s string, params map[string]string, esc func(string) string) string {
	return placeholderRE.ReplaceAllStringFunc(s, func(m string) string {
		v, ok := params[m[1:len(m)-1]]
		if !ok {
			return m
		}
		return esc(v)
	})
}

// ValidateParams 要求 params 的键集合与模板声明的 variables 完全一致：缺了、多了都拒绝，
// 拼错变量名当场报错，而不是发出一条缺内容的短信。
func ValidateParams(variables []string, params map[string]string) error {
	want := make(map[string]bool, len(variables))
	for _, v := range variables {
		want[v] = true
	}
	// 非 nil：只缺或只多时，另一边在 detail 里要是 [] 而不是 null，接入方按数组读它。
	missing, unexpected := []string{}, []string{}
	for _, v := range variables {
		if _, ok := params[v]; !ok {
			missing = append(missing, v)
		}
	}
	for k := range params {
		if !want[k] {
			unexpected = append(unexpected, k)
		}
	}
	if len(missing) == 0 && len(unexpected) == 0 {
		return nil
	}
	sort.Strings(missing)
	sort.Strings(unexpected)
	return domain.Fail(domain.ErrInvalidArgument, domain.CodeNotifyParamsInvalid, "模板变量不匹配").
		WithField("missing", missing).WithField("unexpected", unexpected)
}

func invalid(format string, a ...any) error {
	return domain.Failf(domain.ErrInvalidArgument, domain.CodeNotifyTemplateInvalid, format, a...)
}

// 渠道 / 模式之外的可选字段名，用于"这个渠道不支持该字段"的报错。固定顺序，报错稳定。
var optionalFields = []string{"subject", "contentType", "method", "mentionedList", "mentionedMobileList", "atMobiles", "isAtAll"}

func presentFields(c domain.NotifyContent) map[string]bool {
	return map[string]bool{
		"subject":             c.Subject != "",
		"contentType":         c.ContentType != "",
		"method":              c.Method != "",
		"mentionedList":       len(c.MentionedList) > 0,
		"mentionedMobileList": len(c.MentionedMobileList) > 0,
		"atMobiles":           len(c.AtMobiles) > 0,
		"isAtAll":             c.IsAtAll,
	}
}

func rejectUnsupported(c domain.NotifyContent, allowed ...string) error {
	present := presentFields(c)
	for _, name := range optionalFields {
		if present[name] && !slices.Contains(allowed, name) {
			return invalid("该渠道的模板不支持 %s", name)
		}
	}
	return nil
}

// ValidateContent 校验模板内容与渠道、模式是否相容，并返回补齐了默认值的内容。
//
// custom 模式要求 content（以及邮件的 subject）里的占位符集合恰好等于 variables；
// vendor 模式的 content 只是供应商侧模板的原文，写法由供应商决定，不解析。
func ValidateContent(ch domain.NotifyChannel, mode domain.NotifyMode, c domain.NotifyContent) (domain.NotifyContent, error) {
	if !ch.Valid() {
		return c, invalid("未知的渠道 %q", ch)
	}
	if !ch.AllowsMode(mode) {
		return c, invalid("渠道 %s 不支持 %s 模板", ch, mode)
	}
	if strings.TrimSpace(c.Content) == "" {
		return c, invalid("模板内容不能为空")
	}
	seen := map[string]bool{}
	for _, v := range c.Variables {
		if !identRE.MatchString(v) {
			return c, invalid("变量名 %q 不合法：只能是字母、数字、下划线，且不能以数字开头", v)
		}
		if seen[v] {
			return c, invalid("变量 %q 重复", v)
		}
		seen[v] = true
	}
	if c.Variables == nil {
		c.Variables = []string{}
	}

	var err error
	switch {
	case mode == domain.NotifyModeVendor:
		err = rejectUnsupported(c)
	case ch == domain.NotifyChannelEmail:
		if err = rejectUnsupported(c, "subject", "contentType"); err != nil {
			break
		}
		if strings.TrimSpace(c.Subject) == "" {
			return c, invalid("自定义邮件需要填写主题")
		}
		if c.ContentType == "" {
			c.ContentType = "text/plain"
		}
		if c.ContentType != "text/plain" && c.ContentType != "text/html" {
			return c, invalid("邮件 contentType 只能是 text/plain 或 text/html")
		}
	case ch == domain.NotifyChannelTelegram:
		err = rejectUnsupported(c)
	case ch == domain.NotifyChannelWecomBot:
		err = rejectUnsupported(c, "mentionedList", "mentionedMobileList")
	case ch == domain.NotifyChannelDingtalkBot:
		err = rejectUnsupported(c, "atMobiles", "isAtAll")
	case ch == domain.NotifyChannelWebhook:
		if err = rejectUnsupported(c, "method", "contentType"); err != nil {
			break
		}
		if c.Method == "" {
			c.Method = "POST"
		}
		switch c.Method {
		case "GET":
			if c.ContentType != "" {
				return c, invalid("GET 请求没有 body，不能设置 contentType")
			}
		case "POST":
			if c.ContentType == "" {
				c.ContentType = "application/json"
			}
			if c.ContentType != "application/json" && c.ContentType != "text/plain" {
				return c, invalid("webhook contentType 只能是 application/json 或 text/plain")
			}
		default:
			return c, invalid("webhook method 只能是 GET 或 POST")
		}
	}
	if err != nil {
		return c, err
	}

	if mode == domain.NotifyModeCustom {
		if err := checkPlaceholders(c); err != nil {
			return c, err
		}
		if ch == domain.NotifyChannelWebhook && c.Method == "POST" && c.ContentType == "application/json" {
			sample := make(map[string]string, len(c.Variables))
			for _, v := range c.Variables {
				sample[v] = "0"
			}
			if !json.Valid([]byte(Substitute(c.Content, sample, escapeJSON))) {
				return c, invalid("contentType 是 application/json，但模板内容不是合法的 JSON")
			}
		}
	}
	return c, nil
}

// checkPlaceholders 要求模板里用到的占位符与声明的 variables 恰好一致。
func checkPlaceholders(c domain.NotifyContent) error {
	used := ExtractPlaceholders(c.Subject + "\n" + c.Content)
	declared := make(map[string]bool, len(c.Variables))
	for _, v := range c.Variables {
		declared[v] = true
	}
	usedSet := make(map[string]bool, len(used))
	var undeclared []string
	for _, u := range used {
		usedSet[u] = true
		if !declared[u] {
			undeclared = append(undeclared, u)
		}
	}
	if len(undeclared) > 0 {
		return invalid("模板里用到了没有声明的变量：%s", strings.Join(undeclared, "、"))
	}
	var unused []string
	for _, v := range c.Variables {
		if !usedSet[v] {
			unused = append(unused, v)
		}
	}
	if len(unused) > 0 {
		return invalid("声明了但模板里没有用到的变量：%s", strings.Join(unused, "、"))
	}
	return nil
}

// Rendered 是 custom 模板渲染后的结果。
type Rendered struct {
	Subject string // 仅邮件
	Body    string // 邮件正文 / IM 文本 / webhook 的 body（POST）或 query（GET）
}

// Render 渲染 custom 模板。调用前应已通过 ValidateParams。
func Render(t domain.NotifyTemplate, params map[string]string) Rendered {
	c := t.Content
	esc := escapeNone
	switch t.Channel {
	case domain.NotifyChannelEmail:
		if c.ContentType == "text/html" {
			esc = escapeHTML
		}
	case domain.NotifyChannelWebhook:
		switch {
		case c.Method == "GET":
			esc = escapeQuery
		case c.ContentType == "" || c.ContentType == "application/json":
			esc = escapeJSON
		}
	}
	r := Rendered{Body: Substitute(c.Content, params, esc)}
	if c.Subject != "" {
		// 主题进邮件头：变量值里的换行会注入新的邮件头，统一压成空格。
		r.Subject = strings.NewReplacer("\r", " ", "\n", " ").Replace(Substitute(c.Subject, params, escapeNone))
	}
	return r
}
