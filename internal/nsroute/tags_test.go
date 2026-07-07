package nsroute

import "testing"

func TestCodesToTags(t *testing.T) {
	cases := []struct {
		codes []string
		want  string
	}{
		{nil, ""},
		{[]string{}, ""},
		{[]string{"default"}, ""},
		{[]string{"province:上海", "isp:电信"}, "province=上海;isp=电信"},
		{[]string{"country:中国", "province:上海", "isp:电信"}, "country=中国;province=上海;isp=电信"},
		{[]string{"", "province:北京"}, "province=北京"},
		{[]string{"malformed"}, ""},
	}
	for _, c := range cases {
		got := CodesToTags(c.codes)
		if got != c.want {
			t.Errorf("CodesToTags(%v) = %q, want %q", c.codes, got, c.want)
		}
	}
}
