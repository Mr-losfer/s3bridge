export function escapeHtml(s) {
  return String(s ?? '').replace(/[&<>"']/g, (c) => ({
    '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;',
  }[c]))
}

export function formatBytes(n) {
  const v = Number(n || 0)
  if (v < 1024) return v + ' B'
  const units = ['KB', 'MB', 'GB', 'TB']
  let i = -1, x = v
  do { x /= 1024; i++ } while (x >= 1024 && i < units.length - 1)
  return (x >= 10 ? x.toFixed(0) : x.toFixed(1)) + ' ' + units[i]
}

export function relTime(iso) {
  if (!iso) return '—'
  const d = new Date(iso), diff = (Date.now() - d.getTime()) / 1000
  if (diff < 60) return '刚刚'
  if (diff < 3600) return Math.floor(diff / 60) + ' 分钟前'
  if (diff < 86400) return Math.floor(diff / 3600) + ' 小时前'
  if (diff < 86400 * 30) return Math.floor(diff / 86400) + ' 天前'
  return d.toLocaleDateString('zh-CN')
}

export function fmtTime(iso) {
  if (!iso) return '—'
  return new Date(iso).toLocaleString('zh-CN', { hour12: false })
}

export function fmtDateShort(iso) {
  if (!iso) return '—'
  return new Date(iso).toLocaleString('zh-CN', { hour12: false, month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit' })
}

export const statusMeta = {
  healthy: { label: '正常', cls: 'healthy' },
  unhealthy: { label: '异常', cls: 'unhealthy' },
  degraded: { label: '波动', cls: 'degraded' },
  unknown: { label: '待检测', cls: 'unknown' },
  disabled: { label: '已停用', cls: 'disabled' },
}

export function statusText(s) { return (statusMeta[s] || statusMeta.unknown).label }

export const categoryLabels = {
  image: '图片', video: '视频', audio: '音频', document: '文档', archive: '压缩包', other: '其他',
}
export function categoryLabel(c) { return categoryLabels[c] || c || '其他' }

export function truncate(s, n = 60) {
  const v = String(s ?? '')
  return v.length > n ? v.slice(0, n) + '…' : v
}

export function pct(n, total) {
  if (!total) return 0
  return Math.min(100, Math.round((n / total) * 100))
}

/** 轻量 Markdown 渲染：标题/列表/表格/引用/代码块/行内样式。输入先转义，输出可安全 v-html。 */
export function renderMarkdown(src) {
  const lines = escapeHtml(src || '').split('\n')
  const out = []
  let inCode = false, codeLang = '', listType = '', inTable = false
  const inline = (t) => t
    .replace(/`([^`]+)`/g, '<code>$1</code>')
    .replace(/\*\*([^*]+)\*\*/g, '<strong>$1</strong>')
    .replace(/\[([^\]]+)\]\(([^)]+)\)/g, (m, text, href) =>
      /^https?:\/\//.test(href) ? `<a href="${href}" target="_blank" rel="noopener">${text}</a>` : `<span>${text}</span>`)
  const closeList = () => { if (listType) { out.push(`</${listType}>`); listType = '' } }
  const closeTable = () => { if (inTable) { out.push('</tbody></table>'); inTable = false } }

  for (let i = 0; i < lines.length; i++) {
    const line = lines[i]
    if (/^```/.test(line.trim())) {
      if (inCode) { out.push('</code></pre>'); inCode = false }
      else { closeList(); closeTable(); codeLang = line.trim().slice(3); inCode = true; out.push('<pre><code>') }
      continue
    }
    if (inCode) { out.push(line + '\n'); continue }

    const h = line.match(/^(#{1,4})\s+(.*)$/)
    if (h) {
      closeList(); closeTable()
      const lv = Math.min(h[1].length + 1, 4)
      out.push(`<h${lv}>${inline(h[2])}</h${lv}>`)
      continue
    }
    if (/^\s*$/.test(line)) { closeList(); closeTable(); continue }

    // 表格：| a | b | 后跟 |---|---|
    if (/^\s*\|/.test(line) && lines[i + 1] && /^\s*\|[\s:|-]+\|/.test(lines[i + 1])) {
      closeList(); closeTable()
      const cells = line.trim().slice(1, -1).split('|').map((c) => c.trim())
      out.push('<table><thead><tr>' + cells.map((c) => `<th>${inline(c)}</th>`).join('') + '</tr></thead><tbody>')
      inTable = true; i++
      continue
    }
    if (inTable && /^\s*\|/.test(line)) {
      const cells = line.trim().slice(1, -1).split('|').map((c) => c.trim())
      out.push('<tr>' + cells.map((c) => `<td>${inline(c)}</td>`).join('') + '</tr>')
      continue
    }
    closeTable()

    const quote = line.match(/^>\s?(.*)$/)
    if (quote) { closeList(); out.push(`<blockquote>${inline(quote[1])}</blockquote>`); continue }

    const ul = line.match(/^\s*[-*+]\s+(.*)$/)
    const ol = line.match(/^\s*\d+\.\s+(.*)$/)
    if (ul || ol) {
      const type = ul ? 'ul' : 'ol'
      if (listType !== type) { closeList(); out.push(`<${type}>`); listType = type }
      out.push(`<li>${inline((ul || ol)[1])}</li>`)
      continue
    }
    closeList()
    if (/^---+$/.test(line.trim())) { out.push('<hr />'); continue }
    out.push(`<p>${inline(line)}</p>`)
  }
  closeList(); closeTable()
  if (inCode) out.push('</code></pre>')
  return out.join('\n')
}
