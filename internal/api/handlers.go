package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/Mr-losfer/s3bridge/internal/auth"
	"github.com/Mr-losfer/s3bridge/internal/docs"
	"github.com/Mr-losfer/s3bridge/internal/store"
	"github.com/Mr-losfer/s3bridge/internal/upload"
)

// namedTool 把 REST 请求转成 MCP 工具调用。
//
// 这是刻意的架构选择：MCP 工具是唯一的业务实现，REST 只是它的 HTTP 适配层，
// 因此「后台点按钮」与「AI 通过 MCP 调用」永远得到完全一致的结果与审计记录。
func (s *Server) namedTool(name string) Handler {
	return func(c *Ctx) error {
		return s.runTool(c, name, s.collectArgs(c))
	}
}

func (s *Server) collectArgs(c *Ctx) map[string]any {
	args := map[string]any{}
	for k, vals := range c.R.URL.Query() {
		if len(vals) > 0 {
			args[k] = vals[0]
		}
	}
	for k, v := range c.Params {
		args[k] = v
	}
	if c.R.Method == http.MethodPost || c.R.Method == http.MethodPut || c.R.Method == http.MethodPatch {
		var body map[string]any
		if err := json.NewDecoder(io.LimitReader(c.R.Body, 2<<20)).Decode(&body); err == nil {
			for k, v := range body {
				args[k] = v
			}
		}
	}
	return args
}

func (s *Server) runTool(c *Ctx, name string, args map[string]any) error {
	text, isErr := s.mcp.CallTool(c.R.Context(), name, args, c.Principal)
	var v any
	if err := json.Unmarshal([]byte(text), &v); err != nil {
		v = text
	}
	if isErr {
		msg := text
		if m, ok := v.(map[string]any); ok {
			if e, ok := m["error"].(string); ok {
				msg = e
			}
		}
		status := http.StatusBadRequest
		switch {
		case strings.Contains(msg, "权限不足"):
			status = http.StatusForbidden
		case strings.Contains(msg, "不存在"), strings.Contains(msg, "未注册"):
			status = http.StatusNotFound
		}
		return errf(status, "tool_error", "%s", msg)
	}
	c.JSON(http.StatusOK, v)
	return nil
}

// ---------- 认证 ----------

func (s *Server) login(c *Ctx) error {
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := c.Bind(&req); err != nil {
		return err
	}
	if req.Username == "" || req.Password == "" {
		return errf(http.StatusBadRequest, "bad_request", "用户名和密码不能为空")
	}
	key := clientIP(c.R) + ":" + req.Username
	if !s.router.LoginGuard().Allow(key) {
		return errf(http.StatusTooManyRequests, "rate_limited", "登录尝试过于频繁，请一分钟后再试")
	}
	token, user, err := s.core.Auth.Login(req.Username, req.Password)
	if err != nil {
		return errf(http.StatusUnauthorized, "bad_credentials", "用户名或密码错误，或账号已被禁用")
	}
	c.JSON(http.StatusOK, map[string]any{
		"token": token, "expiresIn": int(s.core.Cfg.SessionTTL.Seconds()), "user": user.Public(),
	})
	return nil
}

func (s *Server) me(c *Ctx) error {
	var u *store.PublicUser
	_ = s.core.Store.View(func(st *store.State) error {
		if x := store.UserByID(st, c.UserID()); x != nil {
			pub := x.Public()
			u = &pub
		}
		return nil
	})
	if u == nil {
		return errf(http.StatusNotFound, "not_found", "用户不存在")
	}
	c.JSON(http.StatusOK, map[string]any{
		"user": u, "via": c.Principal.Via, "permissions": u.Permissions,
	})
	return nil
}

func (s *Server) changePassword(c *Ctx) error {
	var req struct {
		OldPassword string `json:"oldPassword"`
		NewPassword string `json:"newPassword"`
	}
	if err := c.Bind(&req); err != nil {
		return err
	}
	if len(req.NewPassword) < 8 {
		return errf(http.StatusBadRequest, "bad_request", "新密码至少 8 位")
	}
	var hash string
	_ = s.core.Store.View(func(st *store.State) error {
		if u := store.UserByID(st, c.UserID()); u != nil {
			hash = u.PasswordHash
		}
		return nil
	})
	if !auth.VerifyPassword(hash, req.OldPassword) {
		return errf(http.StatusForbidden, "bad_credentials", "原密码不正确")
	}
	newHash, err := auth.HashPassword(req.NewPassword)
	if err != nil {
		return err
	}
	if err := s.core.Store.Update(func(st *store.State) error {
		u := store.UserByID(st, c.UserID())
		if u == nil {
			return errors.New("用户不存在")
		}
		u.PasswordHash = newHash
		u.UpdatedAt = store.NowUTC()
		return nil
	}); err != nil {
		return err
	}
	c.JSON(http.StatusOK, map[string]any{"ok": true})
	return nil
}

func (s *Server) logout(c *Ctx) error {
	// 会话是无状态 JWT，服务端无需维护黑名单；前端清除本地令牌即可。
	c.JSON(http.StatusOK, map[string]any{"ok": true, "note": "请清除本地保存的令牌"})
	return nil
}

func (s *Server) permissions(c *Ctx) error {
	c.JSON(http.StatusOK, map[string]any{
		"permissions": store.AllPermissions,
		"roles":       []store.Role{store.RoleAdmin, store.RoleEditor, store.RoleViewer},
		"defaults": map[string][]string{
			"admin":  permStrings(store.RolePermissions(store.RoleAdmin)),
			"editor": permStrings(store.RolePermissions(store.RoleEditor)),
			"viewer": permStrings(store.RolePermissions(store.RoleViewer)),
		},
	})
	return nil
}

func permStrings(m map[store.Permission]bool) []string {
	out := []string{}
	for _, p := range store.AllPermissions {
		if m[p] {
			out = append(out, string(p))
		}
	}
	return out
}

// ---------- 文档 ----------

func (s *Server) docsIndex(c *Ctx) error {
	c.JSON(http.StatusOK, map[string]any{
		"docs": docs.List(), "s3Endpoint": "/s3", "region": s.core.S3.Region(),
		"mcpEndpoint": "/mcp",
	})
	return nil
}

func (s *Server) docsGet(c *Ctx) error {
	name := c.Param("name")
	body, ok := docs.Get(name)
	if !ok {
		return errf(http.StatusNotFound, "not_found", "文档不存在: %s", name)
	}
	title := name
	for _, d := range docs.List() {
		if d.Name == name {
			title = d.Title
		}
	}
	c.JSON(http.StatusOK, map[string]any{"name": name, "title": title, "content": body})
	return nil
}

// ---------- 上传 / 对象（文件流） ----------

func (s *Server) upload(c *Ctx) error {
	c.R.Body = http.MaxBytesReader(c.W, c.R.Body, s.core.Cfg.MaxUploadBytes+(2<<20))
	if err := c.R.ParseMultipartForm(8 << 20); err != nil {
		return errf(http.StatusBadRequest, "bad_request", "解析 multipart 失败: %v", err)
	}
	if c.R.MultipartForm != nil {
		defer c.R.MultipartForm.RemoveAll()
	}
	file, header, err := c.R.FormFile("file")
	if err != nil {
		return errf(http.StatusBadRequest, "bad_request", "缺少 file 字段: %v", err)
	}
	defer file.Close()

	obj, err := s.core.Uploads.Put(c.R.Context(), upload.PutRequest{
		Reader: file, FileName: header.Filename,
		MimeType: header.Header.Get("Content-Type"), Size: header.Size,
		Bucket: c.R.FormValue("bucket"), ObjectKey: c.R.FormValue("objectKey"),
		PluginID: c.R.FormValue("plugin"), OwnerID: c.UserID(), IsAdmin: c.IsAdmin(),
	})
	if err != nil {
		return errf(http.StatusBadRequest, "upload_failed", "%v", err)
	}
	c.JSON(http.StatusCreated, obj)
	return nil
}

func (s *Server) download(c *Ctx) error {
	bucket, key := c.Query("bucket", ""), c.Query("key", "")
	if bucket == "" || key == "" {
		return errf(http.StatusBadRequest, "bad_request", "需要 bucket 与 key 参数")
	}
	obj, path, err := s.core.Uploads.Get(bucket, key, c.UserID(), c.IsAdmin())
	if err != nil {
		return errf(http.StatusNotFound, "not_found", "%v", err)
	}
	f, err := os.Open(path)
	if err != nil {
		return errf(http.StatusNotFound, "not_found", "对象数据不可用")
	}
	defer f.Close()
	c.W.Header().Set("Content-Type", obj.MimeType)
	c.W.Header().Set("Content-Length", fmt.Sprintf("%d", obj.Size))
	c.W.Header().Set("ETag", `"`+obj.SHA256+`"`)
	if c.Query("disposition", "inline") == "attachment" {
		c.W.Header().Set("Content-Disposition", `attachment; filename="`+safeFilename(obj.FileName)+`"`)
	}
	c.W.Header().Set("Cache-Control", "private, max-age=300")
	http.ServeContent(c.W, c.R, obj.FileName, obj.CreatedAt, f)
	return nil
}

func (s *Server) deleteObject(c *Ctx) error {
	bucket, key := c.Query("bucket", ""), c.Query("key", "")
	if bucket == "" || key == "" {
		return errf(http.StatusBadRequest, "bad_request", "需要 bucket 与 key 参数")
	}
	if err := s.core.Uploads.Delete(bucket, key, c.UserID(), c.IsAdmin()); err != nil {
		return errf(http.StatusNotFound, "not_found", "%v", err)
	}
	c.JSON(http.StatusOK, map[string]any{"ok": true})
	return nil
}

// ---------- 用户 / 密钥 ----------

func (s *Server) userDisable(c *Ctx) error {
	return s.runTool(c, "user_update", map[string]any{
		"id": c.Param("id"), "disabled": true,
	})
}

func (s *Server) userResetPassword(c *Ctx) error {
	if err := c.Require(store.PermUserManage); err != nil {
		return err
	}
	var req struct {
		Password string `json:"password"`
	}
	if err := c.Bind(&req); err != nil {
		return err
	}
	if len(req.Password) < 8 {
		return errf(http.StatusBadRequest, "bad_request", "密码至少 8 位")
	}
	hash, err := auth.HashPassword(req.Password)
	if err != nil {
		return err
	}
	if err := s.core.Store.Update(func(st *store.State) error {
		u := store.UserByID(st, c.Param("id"))
		if u == nil {
			return errors.New("用户不存在")
		}
		u.PasswordHash = hash
		u.UpdatedAt = store.NowUTC()
		return nil
	}); err != nil {
		return errf(http.StatusNotFound, "not_found", "%v", err)
	}
	c.JSON(http.StatusOK, map[string]any{"ok": true})
	return nil
}

func (s *Server) keyCreate(c *Ctx) error {
	return s.namedTool("apikey_create")(c)
}

func (s *Server) keyRevoke(c *Ctx) error {
	id := c.Param("id")
	if err := s.core.Store.Update(func(st *store.State) error {
		k := store.APIKeyByID(st, id)
		if k == nil {
			return errors.New("密钥不存在")
		}
		if !c.IsAdmin() && k.UserID != c.UserID() {
			return errors.New("无权撤销他人密钥")
		}
		k.Revoked = true
		return nil
	}); err != nil {
		return errf(http.StatusBadRequest, "bad_request", "%v", err)
	}
	c.JSON(http.StatusOK, map[string]any{"ok": true, "revoked": id})
	return nil
}

func (s *Server) s3Credentials(c *Ctx) error {
	var keys []map[string]any
	_ = s.core.Store.View(func(st *store.State) error {
		for _, k := range st.APIKeys {
			if k.UserID != c.UserID() && !c.IsAdmin() {
				continue
			}
			keys = append(keys, map[string]any{
				"id": k.ID, "name": k.Name, "accessKey": k.AccessKey,
				"secretHint": k.SecretHint, "revoked": k.Revoked,
				"createdAt": k.CreatedAt,
			})
		}
		return nil
	})
	c.JSON(http.StatusOK, map[string]any{
		"endpoint": "/s3", "region": s.core.S3.Region(),
		"accessKeys":  keys,
		"example":     fmt.Sprintf("aws --endpoint-url %s/s3 s3 ls", publicURL(c, s.core.Cfg.PublicBaseURL)),
		"note":        "SecretAccessKey 明文只在创建时显示一次；请到「API 密钥」页面创建新密钥。",
		"generatedAt": time.Now().UTC().Format(time.RFC3339),
	})
	return nil
}

func publicURL(c *Ctx, configured string) string {
	if configured != "" {
		return configured
	}
	scheme := "http"
	if c.R.TLS != nil {
		scheme = "https"
	}
	return scheme + "://" + c.R.Host
}

func safeFilename(name string) string {
	return strings.NewReplacer(`"`, "", "\r", "", "\n", "", "/", "_", "\\", "_").Replace(name)
}
