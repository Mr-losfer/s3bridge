const TOKEN_KEY = 's3bridge.token'

export const getToken = () => localStorage.getItem(TOKEN_KEY) || ''
export const setToken = (t) => t ? localStorage.setItem(TOKEN_KEY, t) : localStorage.removeItem(TOKEN_KEY)
export const clearToken = () => localStorage.removeItem(TOKEN_KEY)
export const isLoggedIn = () => !!getToken()

/** 统一 REST 客户端：自动附加 Bearer、解析错误、401 时清理会话。 */
export async function api(path, { method = 'GET', body, headers = {}, signal } = {}) {
  const opts = { method, headers: { ...headers }, signal }
  if (getToken()) opts.headers.Authorization = 'Bearer ' + getToken()
  if (body !== undefined) {
    opts.headers['Content-Type'] = 'application/json'
    opts.body = JSON.stringify(body)
  }
  const res = await fetch('/api' + path, opts)
  const text = await res.text()
  let data
  try { data = text ? JSON.parse(text) : null } catch { data = text }
  if (!res.ok) {
    if (res.status === 401) {
      clearToken()
      window.dispatchEvent(new CustomEvent('s3bridge:unauthorized'))
    }
    const msg = (data && data.error) || `请求失败 (HTTP ${res.status})`
    const err = new Error(msg)
    err.status = res.status
    err.payload = data
    throw err
  }
  return data
}

/** 上传文件（multipart）。 */
export async function uploadFile({ file, bucket, plugin, objectKey, onProgress } = {}) {
  const fd = new FormData()
  fd.append('file', file, file.name)
  if (bucket) fd.append('bucket', bucket)
  if (plugin) fd.append('plugin', plugin)
  if (objectKey) fd.append('objectKey', objectKey)
  return new Promise((resolve, reject) => {
    const xhr = new XMLHttpRequest()
    xhr.open('POST', '/api/upload')
    if (getToken()) xhr.setRequestHeader('Authorization', 'Bearer ' + getToken())
    xhr.upload.onprogress = (e) => {
      if (e.lengthComputable && onProgress) onProgress(Math.round((e.loaded / e.total) * 100))
    }
    xhr.onload = () => {
      let data
      try { data = JSON.parse(xhr.responseText) } catch { data = {} }
      if (xhr.status >= 200 && xhr.status < 300) resolve(data)
      else reject(new Error(data.error || `上传失败 (HTTP ${xhr.status})`))
    }
    xhr.onerror = () => reject(new Error('网络错误，上传未完成'))
    xhr.send(fd)
  })
}

/** 通过带鉴权的 fetch 下载对象，避免把令牌放进 URL。 */
export async function downloadObject(bucket, key, fileName) {
  const q = new URLSearchParams({ bucket, key, disposition: 'attachment' })
  const res = await fetch('/api/objects/download?' + q.toString(), {
    headers: { Authorization: 'Bearer ' + getToken() },
  })
  if (!res.ok) throw new Error('下载失败')
  const blob = await res.blob()
  const url = URL.createObjectURL(blob)
  const a = document.createElement('a')
  a.href = url
  a.download = fileName || key.split('/').pop()
  document.body.appendChild(a)
  a.click()
  a.remove()
  setTimeout(() => URL.revokeObjectURL(url), 2000)
}

export async function mcpRPC(method, params, id = 1) {
  const res = await fetch('/mcp', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json', Authorization: 'Bearer ' + getToken() },
    body: JSON.stringify({ jsonrpc: '2.0', id, method, params }),
  })
  return res.json()
}
