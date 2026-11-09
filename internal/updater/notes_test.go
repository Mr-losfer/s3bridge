package updater

import (
	"strings"
	"testing"
)

const sampleChangelog = `## Changelog
### 新功能
* a1b2c3d feat: 首页新增版本检测与一键自动更新
* e4f5g6h feat: **重要：更新要点高亮显示**，支持结构化分组
### 修复
* 1234567 fix: 修复容器部署下二进制不可替换的问题
### 安全
* 7654321 fix: 修复 CVE-2026-0001 导致的权限提升风险
### 其他
* abcdef0 chore: 调整文档排版

---
**安装**：见 [安装文档](https://example.com/install)
**校验**：` + "`sha256sum -c checksums.txt`" + `
Full Changelog: https://example.com/compare
`

func TestParseNotes(t *testing.T) {
	sections := ParseNotes(sampleChangelog, 8, 40)
	if len(sections) != 4 {
		t.Fatalf("期望 4 个分组，实际 %d：%+v", len(sections), sections)
	}
	if sections[0].Title != "新功能" || sections[0].Kind != "feature" {
		t.Fatalf("首分组解析错误：%+v", sections[0])
	}
	if len(sections[0].Items) != 2 {
		t.Fatalf("新功能应有 2 条，实际 %d", len(sections[0].Items))
	}
	// 短 SHA 必须被剥离，避免更新说明里出现无意义的 commit id。
	if strings.Contains(sections[0].Items[0].Text, "a1b2c3d") {
		t.Fatalf("commit SHA 未剥离：%q", sections[0].Items[0].Text)
	}
	if sections[0].Items[0].Kind != "feature" {
		t.Fatalf("feat 应识别为 feature，实际 %s", sections[0].Items[0].Kind)
	}
	// 加粗内容应被切成 Bold 片段并标记为重点。
	item := sections[0].Items[1]
	if !item.Important {
		t.Fatal("含 **加粗** 的条目应标记为重要")
	}
	var bold bool
	for _, seg := range item.Segments {
		if seg.Bold && strings.Contains(seg.Text, "重要") {
			bold = true
		}
	}
	if !bold {
		t.Fatalf("加粗片段未解析：%+v", item.Segments)
	}
	// 安全条目高优先级。
	sec := sections[2]
	if sec.Kind != "security" || !sec.Items[0].Important || sec.Items[0].Kind != "security" {
		t.Fatalf("安全条目识别错误：%+v", sec)
	}
	// 页脚不应混进更新内容。
	for _, s := range sections {
		for _, it := range s.Items {
			if strings.Contains(it.Text, "**安装**") || strings.Contains(it.Text, "Full Changelog") {
				t.Fatalf("页脚被误解析为更新内容：%q", it.Text)
			}
		}
	}
}

func TestParseNotesLimitsAndEmpty(t *testing.T) {
	if got := ParseNotes("", 8, 40); len(got) != 0 {
		t.Fatalf("空内容应返回空分组，实际 %+v", got)
	}
	long := "### 新功能\n"
	for i := 0; i < 60; i++ {
		long += "* feat: 条目 " + string(rune('A'+i%26)) + "\n"
	}
	got := ParseNotes(long, 2, 5)
	total := 0
	for _, s := range got {
		total += len(s.Items)
	}
	if total != 5 {
		t.Fatalf("条目上限未生效：%d", total)
	}
}

func TestInlineSegments(t *testing.T) {
	segs := inlineSegments("使用 `s3bridge` 与 **加粗** 文本")
	var bold, code bool
	for _, s := range segs {
		if s.Bold && s.Text == "加粗" {
			bold = true
		}
		if s.Code && s.Text == "s3bridge" {
			code = true
		}
	}
	if !bold || !code {
		t.Fatalf("内联解析失败：%+v", segs)
	}
	if got := strings.Join(func() []string {
		out := []string{}
		for _, s := range segs {
			out = append(out, s.Text)
		}
		return out
	}(), ""); got != "使用 s3bridge 与 加粗 文本" {
		t.Fatalf("片段重组后文本不一致：%q", got)
	}
}

func TestCleanInlineStripsLinks(t *testing.T) {
	if got := cleanInline("见 [文档](https://example.com/x)"); got != "见 文档" {
		t.Fatalf("链接未清理：%q", got)
	}
}
