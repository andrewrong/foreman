package ears

import "testing"

func TestMatch(t *testing.T) {
	cases := []struct {
		in   string
		want Pattern
		ok   bool
	}{
		{"系统应当记录每次转移", Ubiquitous, true},
		{"  系统应当记录每次转移 ", Ubiquitous, true},
		{"当 runtime.toml 的 forge.poll_interval 改变时，系统应当在下一轮轮询使用新值", Event, true},
		{"当 收到请求 时，系统应当返回 200", Event, true},
		{"在 额度紧张 期间，系统应当降低轮询频率", State, true},
		{"在额度紧张期间，系统应当降低轮询频率", State, true},
		{"如果 token 过期，那么系统应当拒绝请求", Unwanted, true},
		{"在具备 GPU 的情况下，系统应当启用加速", Optional, true},
		{"在具备GPU的情况下，系统应当启用加速", Optional, true},

		{"", "", false},
		{"系统应当", "", false},
		{"系统会记录每次转移", "", false},
		{"当 收到请求 时, 系统应当返回 200", "", false},
		{"当 收到请求 时，系统返回 200", "", false},
		{"当 时，系统应当返回", "", false},
		{"在 期间，系统应当返回", "", false},
		{"如果 出错，系统应当报告", "", false},
		{"如果 出错，那么系统报告", "", false},
		{"在具备 的情况下，系统应当启用", "", false},
		{"x", "", false},
		{"记录日志。系统应当记录", "", false},
		{"系统应当记录\n并且返回", "", false},
	}
	for _, c := range cases {
		got, ok := Match(c.in)
		if ok != c.ok || got != c.want {
			t.Errorf("Match(%q) = (%q, %v), want (%q, %v)", c.in, got, ok, c.want, c.ok)
		}
	}
}
