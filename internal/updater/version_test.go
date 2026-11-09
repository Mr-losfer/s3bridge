package updater

import "testing"

func TestParseVersion(t *testing.T) {
	cases := []struct {
		in   string
		ok   bool
		want Version
	}{
		{"v1.2.3", true, Version{1, 2, 3, "", "v1.2.3"}},
		{"1.2.3", true, Version{1, 2, 3, "", "1.2.3"}},
		{"V2.0", true, Version{2, 0, 0, "", "V2.0"}},
		{"v1.4.0-rc.1+build.9", true, Version{1, 4, 0, "rc.1", "v1.4.0-rc.1+build.9"}},
		{"dev", false, Version{}},
		{"", false, Version{}},
		{"v1.2.3.4", false, Version{}},
		{"v1.x.3", false, Version{}},
		{"abc123def", false, Version{}},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			got, ok := ParseVersion(tc.in)
			if ok != tc.ok {
				t.Fatalf("ParseVersion(%q) ok=%v，期望 %v", tc.in, ok, tc.ok)
			}
			if ok && got != tc.want {
				t.Fatalf("ParseVersion(%q)=%+v，期望 %+v", tc.in, got, tc.want)
			}
		})
	}
}

func TestCompare(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"v1.0.0", "v1.0.0", 0},
		{"v1.0.1", "v1.0.0", 1},
		{"v1.1.0", "v1.0.9", 1},
		{"v2.0.0", "v1.99.99", 1},
		{"v1.0.0", "v1.0.1", -1},
		{"v1.0.0", "v1.0.0-rc.1", 1}, // 正式版高于预发布
		{"v1.0.0-rc.1", "v1.0.0-rc.2", -1},
		{"v1.2", "v1.2.0", 0},
	}
	for _, tc := range cases {
		t.Run(tc.a+"_vs_"+tc.b, func(t *testing.T) {
			a, _ := ParseVersion(tc.a)
			b, _ := ParseVersion(tc.b)
			if got := Compare(a, b); got != tc.want {
				t.Fatalf("Compare(%s,%s)=%d，期望 %d", tc.a, tc.b, got, tc.want)
			}
		})
	}
}

func TestIsNewer(t *testing.T) {
	cases := []struct {
		latest, current string
		want            bool
	}{
		{"v1.1.0", "v1.0.0", true},
		{"v1.0.0", "v1.0.0", false},
		{"v0.9.0", "v1.0.0", false},
		{"v1.0.0", "dev", true}, // 开发构建提示有更新
		{"not-a-version", "v1.0.0", false},
	}
	for _, tc := range cases {
		t.Run(tc.latest+"_over_"+tc.current, func(t *testing.T) {
			if got := IsNewer(tc.latest, tc.current); got != tc.want {
				t.Fatalf("IsNewer(%q,%q)=%v，期望 %v", tc.latest, tc.current, got, tc.want)
			}
		})
	}
}
