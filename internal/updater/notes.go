package updater

import (
	"regexp"
	"strings"
)

// Segment 是更新说明一行的内联片段。Bold 片段由前端渲染成高亮 <mark>，
// 这样「重要部分高亮」由后端结构化标注，前端不需要执行任何 HTML/Markdown。
type Segment struct {
	Text string `json:"text"`
	Bold bool   `json:"bold,omitempty"`
	Code bool   `json:"code,omitempty"`
}

type NoteItem struct {
	Kind      string    `json:"kind"` // breaking/security/important/feature/fix/other
	Important bool      `json:"important"`
	Text      string    `json:"text"`
	Segments  []Segment `json:"segments"`
}

type NoteSection struct {
	Title string     `json:"title"`
	Kind  string     `json:"kind"`
	Items []NoteItem `json:"items"`
}

var (
	commitSHA = regexp.MustCompile(`^[0-9a-f]{7,40}\s+`)
	mdLink    = regexp.MustCompile(`\[([^\]]+)\]\([^)]*\)`)
	mdHead    = regexp.MustCompile(`^\s*#{1,6}\s*`)
	listHead  = regexp.MustCompile(`^\s*(?:[-*+]\s+|\d+[.)]\s+)`)
)

// ParseNotes 把 GoReleaser 生成的 Markdown changelog 解析为结构化分组。
// 只提取标题与列表项，忽略正文段落与页脚，避免把安装说明混进「更新内容」。
func ParseNotes(body string, maxSections, maxItems int) []NoteSection {
	if maxSections <= 0 {
		maxSections = 8
	}
	if maxItems <= 0 {
		maxItems = 40
	}
	sections := make([]NoteSection, 0, 4)
	total := 0
	current := -1

	for _, raw := range strings.Split(strings.ReplaceAll(body, "\r\n", "\n"), "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "<!--") {
			continue
		}
		// 页脚：分隔线、安装/校验指引、Full Changelog 链接。
		if isFooter(line) {
			continue
		}
		if mdHead.MatchString(line) {
			if len(sections) >= maxSections {
				break
			}
			title := strings.TrimSpace(mdHead.ReplaceAllString(line, ""))
			title = cleanInline(title)
			if title == "" || strings.EqualFold(title, "changelog") || strings.EqualFold(title, "What's Changed") {
				continue
			}
			sections = append(sections, NoteSection{Title: title, Kind: sectionKind(title)})
			current = len(sections) - 1
			continue
		}
		if !listHead.MatchString(line) {
			continue // 只渲染列表项，跳过普通段落
		}
		text := cleanInline(listHead.ReplaceAllString(line, ""))
		text = commitSHA.ReplaceAllString(text, "") // 去掉 GoReleaser 附带的短 SHA
		text = strings.TrimSpace(text)
		if text == "" || total >= maxItems {
			continue
		}
		kind, important := classify(text)
		segments := inlineSegments(text)
		for _, seg := range segments {
			if seg.Bold {
				important = true // 加粗内容视为重点
			}
		}
		if current < 0 {
			sections = append(sections, NoteSection{Title: "更新内容", Kind: "other"})
			current = len(sections) - 1
		}
		if sections[current].Kind == "breaking" && kind == "other" {
			kind = "breaking"
			important = true
		}
		sections[current].Items = append(sections[current].Items, NoteItem{
			Kind: kind, Important: important, Text: text, Segments: segments,
		})
		total++
	}

	// 丢弃没有任何条目的分组。
	out := sections[:0]
	for _, s := range sections {
		if len(s.Items) > 0 {
			out = append(out, s)
		}
	}
	return out
}

func isFooter(line string) bool {
	if line == "---" || line == "***" || strings.HasPrefix(line, "**安装**") ||
		strings.HasPrefix(line, "**校验**") || strings.HasPrefix(line, "Full Changelog") {
		return true
	}
	return false
}

func cleanInline(s string) string {
	s = mdLink.ReplaceAllString(s, "$1")
	return strings.TrimSpace(s)
}

func sectionKind(title string) string {
	t := strings.ToLower(title)
	switch {
	case strings.ContainsAny(t, "破坏 不兼容 迁移 重要") || strings.Contains(t, "breaking"):
		return "breaking"
	case strings.Contains(t, "安全") || strings.Contains(t, "security"):
		return "security"
	case strings.Contains(t, "新功能") || strings.Contains(t, "feature") || strings.Contains(t, "特性"):
		return "feature"
	case strings.Contains(t, "修复") || strings.Contains(t, "fix"):
		return "fix"
	default:
		return "other"
	}
}

func classify(text string) (string, bool) {
	t := strings.ToLower(text)
	switch {
	case hasAny(t, "breaking change", "breaking:", "不兼容", "破坏性", "需要迁移", "必读"):
		return "breaking", true
	case hasAny(t, "cve-", "安全", "security", "漏洞", "越权", "凭据泄露", "权限提升"):
		return "security", true
	case hasAny(t, "重要", "注意", "!:", "升级前"):
		return "important", true
	case hasAny(t, "feat", "新增", "新功能", "支持", "添加", "引入"):
		return "feature", false
	case hasAny(t, "fix", "修复", "修正", "优化", "改进", "规避"):
		return "fix", false
	default:
		return "other", false
	}
}

func hasAny(s string, words ...string) bool {
	for _, w := range words {
		if strings.Contains(s, w) {
			return true
		}
	}
	return false
}

// inlineSegments 把 **加粗** 与 `代码` 切成片段，供前端精确高亮。
func inlineSegments(s string) []Segment {
	var out []Segment
	var buf strings.Builder
	flush := func() {
		if buf.Len() > 0 {
			out = append(out, Segment{Text: buf.String()})
			buf.Reset()
		}
	}
	for i := 0; i < len(s); {
		if strings.HasPrefix(s[i:], "**") {
			if end := strings.Index(s[i+2:], "**"); end >= 0 && end > 0 {
				flush()
				out = append(out, Segment{Text: s[i+2 : i+2+end], Bold: true})
				i += 2 + end + 2
				continue
			}
		}
		if s[i] == '`' {
			if end := strings.IndexByte(s[i+1:], '`'); end >= 0 {
				flush()
				out = append(out, Segment{Text: s[i+1 : i+1+end], Code: true})
				i += 1 + end + 1
				continue
			}
		}
		buf.WriteByte(s[i])
		i++
	}
	flush()
	if len(out) == 0 {
		out = append(out, Segment{Text: s})
	}
	return out
}
