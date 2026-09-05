package domain_test

import (
	"encoding/json"
	"testing"

	"github.com/basicfu/fp/internal/domain"
)

func TestCoerceConfigValue(t *testing.T) {
	cases := []struct {
		name      string
		valueType string
		in        string
		want      string // 期望的规范 JSON；wantErr 为 true 时忽略
		wantErr   bool
	}{
		// 原生类型直接过
		{"bool 原生", domain.ConfigValueBool, `true`, `true`, false},
		{"int 原生", domain.ConfigValueInt, `3`, `3`, false},
		{"float 原生", domain.ConfigValueFloat, `0.02`, `0.02`, false},
		{"string 原生", domain.ConfigValueString, `"商城"`, `"商城"`, false},
		{"array 原生", domain.ConfigValueArray, `[1,2,3]`, `[1,2,3]`, false},
		{"object 原生", domain.ConfigValueObject, `{"a":1}`, `{"a":1}`, false},

		// 弱转换：字符串形式的值也要能进
		{"字符串进 int", domain.ConfigValueInt, `"3"`, `3`, false},
		{"字符串进 bool", domain.ConfigValueBool, `"true"`, `true`, false},
		{"字符串进 float", domain.ConfigValueFloat, `"0.02"`, `0.02`, false},
		{"字符串进 array", domain.ConfigValueArray, `"[1,2]"`, `[1,2]`, false},
		{"字符串进 object", domain.ConfigValueObject, `"{\"a\":1}"`, `{"a":1}`, false},
		{"数字进 string", domain.ConfigValueString, `3`, `"3"`, false},

		// 转不过去才报错
		{"abc 进 int", domain.ConfigValueInt, `"abc"`, "", true},
		{"小数进 int", domain.ConfigValueInt, `3.7`, "", true},
		{"对象进 array", domain.ConfigValueArray, `{"a":1}`, "", true},
		{"数组进 object", domain.ConfigValueObject, `[1,2]`, "", true},

		// null 一律是"未配置"，任何类型都接受
		{"null 进 int", domain.ConfigValueInt, `null`, `null`, false},
		{"null 进 object", domain.ConfigValueObject, `null`, `null`, false},

		// 大整数必须原样保留：转成 any 再转回来会静默舍入到 float64 精度。
		{"object 里的大整数", domain.ConfigValueObject, `{"id":9007199254740993}`, `{"id":9007199254740993}`, false},
		{"array 里的大整数", domain.ConfigValueArray, `[9007199254740993]`, `[9007199254740993]`, false},
		// object 的 key 顺序必须原样保留，不能被重排成字母序。
		{"object 的 key 顺序", domain.ConfigValueObject, `{"b":1,"a":2}`, `{"b":1,"a":2}`, false},
		// compact：多余空白要去掉，但内容与顺序不变。
		{"object 去空白", domain.ConfigValueObject, `{ "b" : 1 }`, `{"b":1}`, false},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := domain.CoerceConfigValue(c.valueType, json.RawMessage(c.in))
			if c.wantErr {
				if err == nil {
					t.Fatalf("期望报错，实际得到 %s", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("不该报错: %v", err)
			}
			if string(got) != c.want {
				t.Fatalf("得到 %s，期望 %s", got, c.want)
			}
		})
	}
}

func TestConfigFieldIsSet(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want bool
	}{
		{"已配置", `3`, true},
		{"配成了 false", `false`, true}, // false 是有效值，不是未配置
		{"配成了 0", `0`, true},         // 0 同理
		{"配成了空串", `""`, true},
		{"JSON null 是未配置", `null`, false},
		{"nil 是未配置", ``, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := domain.ConfigField{Type: domain.ConfigValueInt, Value: json.RawMessage(c.raw)}
			if got := f.IsSet(); got != c.want {
				t.Fatalf("IsSet() = %v，期望 %v", got, c.want)
			}
		})
	}
}

func TestIsConfigTypeAndValueType(t *testing.T) {
	for _, s := range []string{domain.ConfigTypeDefault, domain.ConfigTypeWeb} {
		if !domain.IsConfigType(s) {
			t.Fatalf("%q 应当是合法分区", s)
		}
	}
	for _, s := range []string{"", "default", "web", "MOBILE"} {
		if domain.IsConfigType(s) {
			t.Fatalf("%q 不应当是合法分区", s)
		}
	}
	for _, s := range []string{"bool", "int", "float", "string", "array", "object"} {
		if !domain.IsConfigValueType(s) {
			t.Fatalf("%q 应当是合法值类型", s)
		}
	}
	for _, s := range []string{"", "duration", "secret", "Int"} {
		if domain.IsConfigValueType(s) {
			t.Fatalf("%q 不应当是合法值类型", s)
		}
	}
}
