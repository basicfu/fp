package fpsdk

import (
	"fmt"
	"reflect"
	"sort"
	"strings"
	"time"
	"unicode"
)

// fp 的配置类型。**不带任何一门语言的特性**——没有 duration，因为其他
// 语言没这个概念；Go 的 time.Duration 是本 SDK 按毫秒当 int 处理的私事。
//
// 这几个字符串是**线上契约**，必须与服务端 internal/domain 的
// ConfigValue* 逐字相同。两处分处 sdk/ 与 internal/（sdk 不得 import
// internal），任何一边单独看都只是几个孤立的字符串常量，改错了
// go build / vet / 全量测试照样全绿。配对由 internal/integration 里的
// TestConfigValueTypesMatch 守护——那是唯一能同时看到两个包的地方。
const (
	cfgTypeBool   = "bool"
	cfgTypeInt    = "int"
	cfgTypeFloat  = "float"
	cfgTypeString = "string"
	cfgTypeArray  = "array"
	cfgTypeObject = "object"
)

// durationType 缓存 time.Duration 的反射类型，供 fpTypeOf 做**具体类型**
// 判断——它必须发生在 Kind 判断之前，见 fpTypeOf 的注释。
var durationType = reflect.TypeOf(time.Duration(0))

// fieldSpec 是 struct 里一个可绑定字段的规格。
type fieldSpec struct {
	// Key 是配置项在 fp 上的键，如 "upstream.timeout"。
	Key string
	// Type 是 fp 类型，取值见上面的 cfgType* 常量。
	Type string
	// Index 是 reflect.Value.FieldByIndex 用的字段索引路径。
	Index []int
	// Duration 为 true 时，拉到的整数按**毫秒**解释，填进字段前乘
	// time.Millisecond。
	Duration bool
}

// specsOf 反射 t 推导出全部可绑定字段，按 key 升序返回。
//
// 排序不是审美：MissingConfigError 的清单直接来自它，不排序的话 Go 的
// map 遍历顺序会让每次启动打出的清单顺序都不同，人对着控制台一项项建的
// 时候极易漏项。
func specsOf(t reflect.Type) ([]fieldSpec, error) {
	if t.Kind() != reflect.Struct {
		return nil, fmt.Errorf("fpsdk: 只能绑定 struct，得到 %s", t)
	}
	var out []fieldSpec
	if err := collectSpecs(t, "", nil, &out); err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out, nil
}

func collectSpecs(t reflect.Type, prefix string, index []int, out *[]fieldSpec) error {
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		// 未导出字段跳过：反射既读不到也写不进，它不可能是配置项。
		if !f.IsExported() {
			continue
		}
		key := prefix + toSnake(f.Name)
		// 拷一份再 append：append 可能复用底层数组，
		// 兄弟字段的路径会互相踩。
		path := append(append([]int{}, index...), i)

		// 标了 fp:"json" 的 struct 整体当一个 object，不展开成分组。
		// 供应商列表那种拆不动的结构走这条。
		if f.Tag.Get("fp") == "json" {
			*out = append(*out, fieldSpec{Key: key, Type: cfgTypeObject, Index: path})
			continue
		}

		// 未标 tag 的嵌套 struct 是**分组**，递归展开成 父.子 的 key。
		// time.Duration 不是 struct，落不到这里；time.Time 会——但它不在
		// 支持的类型里，展开后它的字段全是未导出的，会得到一个空分组。
		// 这属于"用了不该用的类型"，交给下面的 fpTypeOf 报错更清楚，
		// 所以这里先排除掉标准库里那个唯一常见的误用。
		if f.Type.Kind() == reflect.Struct && f.Type != reflect.TypeOf(time.Time{}) {
			if err := collectSpecs(f.Type, key+".", path, out); err != nil {
				return err
			}
			continue
		}

		typ, isDur, err := fpTypeOf(f.Type)
		if err != nil {
			return fmt.Errorf("fpsdk: 字段 %s: %w", key, err)
		}
		*out = append(*out, fieldSpec{Key: key, Type: typ, Index: path, Duration: isDur})
	}
	return nil
}

// fpTypeOf 把 Go 类型映射成 fp 类型。
func fpTypeOf(t reflect.Type) (string, bool, error) {
	// **必须先判具体类型再判 Kind。** time.Duration 底层是 int64，
	// 顺序反了它会被当成普通整数，毫秒换算整个丢掉——配的 3000 会变成
	// 3 微秒，而且不报任何错。
	if t == durationType {
		return cfgTypeInt, true, nil
	}
	switch t.Kind() {
	case reflect.Bool:
		return cfgTypeBool, false, nil
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return cfgTypeInt, false, nil
	case reflect.Float32, reflect.Float64:
		return cfgTypeFloat, false, nil
	case reflect.String:
		return cfgTypeString, false, nil
	case reflect.Slice, reflect.Array:
		return cfgTypeArray, false, nil
	case reflect.Map:
		return cfgTypeObject, false, nil
	default:
		// 指针、interface、chan、func 一律报错，不静默跳过——静默跳过会让
		// 一个本该被配置的字段永远停在零值，且没有任何迹象。
		return "", false, fmt.Errorf("不支持的类型 %s", t)
	}
}

// toSnake 把 Go 字段名转成 snake_case 的 key。
//
// 连续大写要当成一个缩写整体处理：APIKey → api_key 而不是 a_p_i_key，
// UserID → user_id，HTTPSProxy → https_proxy。
func toSnake(s string) string {
	r := []rune(s)
	var b strings.Builder
	b.Grow(len(r) + 4)
	for i, c := range r {
		if unicode.IsUpper(c) {
			// 在两种位置插下划线：① 前一个字符不是大写（词边界）；
			// ② 前一个是大写但后一个是小写（缩写结束，如 HTTPSProxy 的 P）。
			if i > 0 && (!unicode.IsUpper(r[i-1]) ||
				(i+1 < len(r) && unicode.IsLower(r[i+1]))) {
				b.WriteByte('_')
			}
			b.WriteRune(unicode.ToLower(c))
			continue
		}
		b.WriteRune(c)
	}
	return b.String()
}

// ExportedConfigTypes 返回 SDK 认识的全部 fp 类型。
//
// 导出它只为一个目的：让 internal/integration 能核对它与服务端
// domain.IsConfigValueType 的取值集合一致（见 TestConfigValueTypesMatch）。
// 业务方用不到这个函数。
func ExportedConfigTypes() []string {
	return []string{cfgTypeBool, cfgTypeInt, cfgTypeFloat,
		cfgTypeString, cfgTypeArray, cfgTypeObject}
}
