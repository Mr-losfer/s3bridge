package mcp

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/Mr-losfer/s3bridge/internal/auth"
	"github.com/Mr-losfer/s3bridge/internal/docs"
	"github.com/Mr-losfer/s3bridge/internal/plugin"
	"github.com/Mr-losfer/s3bridge/internal/plugins/workbuddy"
	"github.com/Mr-losfer/s3bridge/internal/store"
	"github.com/Mr-losfer/s3bridge/internal/upload"
)

type Tool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
}

func strProp(desc string) map[string]any {
	return map[string]any{"type": "string", "description": desc}
}
func boolProp(desc string) map[string]any {
	return map[string]any{"type": "boolean", "description": desc}
}
func numProp(desc string) map[string]any {
	return map[string]any{"type": "number", "description": desc}
}
func objProp(desc string) map[string]any {
	return map[string]any{"type": "object", "description": desc}
}
func arrProp(desc string) map[string]any {
	return map[string]any{"type": "array", "description": desc, "items": map[string]any{"type": "string"}}
}

func schema(props map[string]any, required ...string) map[string]any {
	return map[string]any{"type": "object", "properties": props, "required": required}
}

// Tools 是 MCP 暴露的全部能力清单。
func (s *Server) Tools() []Tool {
	id := strProp("插件实例 ID，例如 workbuddy")
	return []Tool{
		{Name: "site_info", Description: "查看站点信息：版本、运行时长、S3 接入点、插件/用户/存储桶/对象数量、健康调度状态",
			InputSchema: schema(map[string]any{})},
		{Name: "site_stats", Description: "查看站点统计：按文件类型、存储桶、插件的分布与异常数量",
			InputSchema: schema(map[string]any{})},
		{Name: "system_settings_get", Description: "查看系统设置（站点名、默认插件、健康检测总开关、告警开关等）",
			InputSchema: schema(map[string]any{})},
		{Name: "system_settings_update", Description: "管理系统设置；修改健康检测总开关后会重建定时调度",
			InputSchema: schema(map[string]any{
				"siteName": strProp("站点名称"), "defaultPlugin": strProp("默认插件 ID"),
				"healthEnabled": boolProp("是否开启定时健康检测"), "healthIntervalSecs": numProp("全局默认检测间隔（秒）"),
				"alertOnFailure": boolProp("插件异常时是否生成告警"), "announcement": strProp("站点公告"),
			})},
		{Name: "system_update_status", Description: "查看版本更新状态：当前版本、GitHub Releases 最新版、结构化更新要点（含重点标注）、自动更新进度与部署策略",
			InputSchema: schema(map[string]any{})},
		{Name: "system_update_check", Description: "立即从 GitHub Releases 检测最新版本；返回是否有更新、版本号、发布时间与重要更新内容",
			InputSchema: schema(map[string]any{"force": boolProp("强制刷新，忽略 30 秒节流（默认 false）")})},
		{Name: "system_update_apply", Description: "一键自动更新到最新版本：下载发布资产 → 校验 SHA256 → 备份 → 原子替换 → 自动重启服务；需 system:manage 权限",
			InputSchema: schema(map[string]any{})},
		{Name: "system_update_dismiss", Description: "忽略指定版本的新版本弹窗（首页更新卡片仍然显示）",
			InputSchema: schema(map[string]any{"tag": strProp("要忽略的版本号，例如 v1.1.0")}, "tag")},

		{Name: "plugin_list", Description: "查看插件列表（按权重从高到低排序，即实际调度顺序；含状态、版本、可承接文件类型、参数警告）",
			InputSchema: schema(map[string]any{})},
		{Name: "plugin_get", Description: "查看单个插件详情，含参数 Schema、分组与脱敏后的当前值",
			InputSchema: schema(map[string]any{"id": id}, "id")},
		{Name: "plugin_docs", Description: "查看插件开发文档：契约、参数 Schema 定义、实现步骤、调试与发布流程，并附带当前已注册插件的参数模板",
			InputSchema: schema(map[string]any{"topic": strProp("可选：文档主题，留空返回全文")})},
		{Name: "s3_docs", Description: "查看 S3 开放接口文档：SigV4 凭据、支持的 API、SDK/aws-cli 示例",
			InputSchema: schema(map[string]any{})},
		{Name: "installation_docs", Description: "查看安装部署文档：Docker 单容器、Compose、Linux 二进制与源码构建",
			InputSchema: schema(map[string]any{})},
		{Name: "plugin_debug", Description: "插件调试：无副作用地执行参数校验、临时凭据有效期检查、上游连通性探测，返回分步诊断",
			InputSchema: schema(map[string]any{"id": id}, "id")},
		{Name: "plugin_patch", Description: "插件差量修改：只更新传入的字段，未传字段保持不变（params 按键合并）",
			InputSchema: schema(map[string]any{
				"id": id, "name": strProp("名称"), "description": strProp("描述"),
				"enabled":   boolProp("是否启用"),
				"priority":  numProp("调度权重，越大越优先被选中（同权重时健康者优先）"),
				"fileTypes": arrProp("可承接的文件类型：image/video/audio/document/archive/other，空数组表示全部"),
				"params":    objProp("要合并的参数键值对"), "health": objProp("定时检测配置"),
				"expectedRevision": numProp("乐观锁版本号，可选"),
			}, "id")},
		{Name: "plugin_replace", Description: "插件全量修改：用传入的完整配置替换现有配置，未提供的参数重置为默认值",
			InputSchema: schema(map[string]any{
				"id": id, "name": strProp("名称"), "description": strProp("描述"),
				"enabled":   boolProp("是否启用"),
				"priority":  numProp("调度权重，越大越优先被选中"),
				"fileTypes": arrProp("可承接的文件类型：image/video/audio/document/archive/other，空数组表示全部"),
				"params":    objProp("完整参数表"),
				"health":    objProp("完整定时检测配置"), "expectedRevision": numProp("乐观锁版本号，可选"),
			}, "id")},
		{Name: "plugin_update", Description: "插件更新：升级版本号并从插件清单同步新增参数模板，可重置为默认值",
			InputSchema: schema(map[string]any{
				"id": id, "version": strProp("目标版本，留空使用内置版本"),
				"syncParams": boolProp("补齐清单中新增参数"), "resetToDefault": boolProp("重置全部参数为默认值"),
				"note": strProp("更新说明"),
			}, "id")},
		{Name: "plugin_params_set", Description: "插件参数修改：按 key 覆盖指定参数（临时 Bearer Token 等在这里更新）",
			InputSchema: schema(map[string]any{"id": id, "params": objProp("参数键值对")}, "id", "params")},
		{Name: "plugin_toggle", Description: "启用或停用插件",
			InputSchema: schema(map[string]any{"id": id, "enabled": boolProp("true 启用 / false 停用")}, "id", "enabled")},
		{Name: "plugin_health_check", Description: "立即对指定插件执行一次健康检测并更新状态/告警",
			InputSchema: schema(map[string]any{"id": id}, "id")},
		{Name: "plugin_health_config", Description: "设置每个插件独立的定时检测（间隔、超时、失败阈值、探活模式）",
			InputSchema: schema(map[string]any{
				"id": id, "enabled": boolProp("是否开启"), "intervalSecs": numProp("检测间隔秒数，最小 10"),
				"timeoutSecs": numProp("单次超时秒数 1~120"), "failThreshold": numProp("连续失败几次标记为异常"),
				"probeMode": strProp("ping 或 upload-sample"),
			}, "id")},
		{Name: "plugin_changes", Description: "查看插件变更记录（差量/全量修改、更新、调试、参数修改的审计日志）",
			InputSchema: schema(map[string]any{"id": id, "limit": numProp("返回条数")})},
		{Name: "plugin_parse_token", Description: "解析 WorkBuddy Bearer Token 的 exp 与 sub，自动得到过期时间与 x-user-id",
			InputSchema: schema(map[string]any{"authorization": strProp("Bearer Token 或完整 Authorization 头")}, "authorization")},
		{Name: "plugin_registry", Description: "查看已编译进服务端的插件类型清单与完整参数 Schema，用于开发新插件",
			InputSchema: schema(map[string]any{})},
		{Name: "plugin_create", Description: "创建插件：从零建立新的上传插件实例（可附完整声明式源码、或从内置插件模板派生），创建后立即进入插件列表并可被 upload_test 调用",
			InputSchema: schema(map[string]any{
				"kind": strProp("插件 ID，全局唯一，小写字母开头，如 my-uploader"),
				"name": strProp("显示名称"), "description": strProp("说明"),
				"version": strProp("版本号"), "author": strProp("作者"),
				"enabled":    boolProp("创建后是否立即启用，默认 true"),
				"priority":   numProp("调度权重，越大越优先被选中，默认 100"),
				"fileTypes":  arrProp("可承接的文件类型：image/video/audio/document/archive/other，留空表示全部"),
				"definition": objProp("完整声明式插件源码（对象或 JSON 字符串）；留空生成可运行骨架"),
				"template":   strProp("从内置插件导出等价源码作为起点，如 workbuddy"),
				"params":     objProp("初始参数键值对"),
			}, "kind")},
		{Name: "plugin_code_get", Description: "读取插件源码：返回完整声明式定义、规范化 JSON 与参数 Schema；内置插件返回只读的等价模板",
			InputSchema: schema(map[string]any{"id": id}, "id")},
		{Name: "plugin_code_upload", Description: "上传/全量替换插件源码：即时编译生效，无需重启；仅自定义插件可改，带乐观锁与审计",
			InputSchema: schema(map[string]any{
				"id": id, "definition": objProp("完整声明式插件源码（对象或 JSON 字符串）"),
				"expectedRevision": numProp("乐观锁版本号，可选"),
			}, "id", "definition")},
		{Name: "plugin_code_patch", Description: "差量修改插件源码：只改指定字段，支持点号路径（如 request.headers.Authorization）与 params 按 key upsert，未涉及字段保持不变",
			InputSchema: schema(map[string]any{
				"id": id, "patch": objProp("要修改的字段，支持点号路径；值为 null 表示删除该字段"),
				"expectedRevision": numProp("乐观锁版本号，可选"),
			}, "id", "patch")},
		{Name: "plugin_validate", Description: "静态校验插件源码（不落库）：结构、模板变量、参数必填与请求预览，敏感值自动脱敏",
			InputSchema: schema(map[string]any{
				"definition": objProp("待校验的声明式插件源码（对象或 JSON 字符串）"),
				"params":     objProp("用于渲染预览的参数值"),
			}, "definition")},
		{Name: "plugin_export", Description: "导出插件源码：format=json 得到可移植定义，format=go 得到可内置进二进制的 Go 插件文件源码",
			InputSchema: schema(map[string]any{
				"id": id, "format": strProp("json 或 go，默认 json"),
			}, "id")},
		{Name: "plugin_delete", Description: "删除自定义插件（内置插件只能停用）；同时清理该插件的未确认告警",
			InputSchema: schema(map[string]any{"id": id}, "id")},

		{Name: "alerts_list", Description: "查看插件异常告警（未确认优先）",
			InputSchema: schema(map[string]any{"limit": numProp("返回条数")})},
		{Name: "alerts_ack", Description: "确认一条告警",
			InputSchema: schema(map[string]any{"id": strProp("告警 ID")}, "id")},

		{Name: "upload_test", Description: "后台上传测试：传入文件内容的 base64，走指定插件完成一次真实上传并返回标准结果",
			InputSchema: schema(map[string]any{
				"fileName": strProp("文件名，如 test.png"), "contentType": strProp("MIME 类型"),
				"contentBase64": strProp("文件内容的 Base64"), "bucket": strProp("存储桶，默认 default"),
				"pluginId": strProp("插件 ID，留空自动选择"),
			}, "fileName", "contentBase64")},
		{Name: "uploads_list", Description: "查看上传列表，可按存储桶、文件类型、插件、关键字过滤",
			InputSchema: schema(map[string]any{
				"bucket": strProp("存储桶"), "category": strProp("文件类型 image/video/audio/document/archive/other"),
				"pluginId": strProp("插件 ID"), "keyword": strProp("文件名关键字"), "limit": numProp("返回条数"),
			})},
		{Name: "bucket_list", Description: "查看存储桶列表及每个桶的对象数与总大小",
			InputSchema: schema(map[string]any{})},
		{Name: "bucket_create", Description: "创建存储桶",
			InputSchema: schema(map[string]any{
				"name": strProp("桶名，小写字母/数字/- _ ."), "description": strProp("说明"), "publicRead": boolProp("是否公开读"),
			}, "name")},
		{Name: "bucket_update", Description: "修改存储桶说明与公开读设置",
			InputSchema: schema(map[string]any{
				"name": strProp("桶名"), "description": strProp("说明"), "publicRead": boolProp("是否公开读"),
			}, "name")},
		{Name: "bucket_delete", Description: "删除空存储桶",
			InputSchema: schema(map[string]any{"name": strProp("桶名")}, "name")},

		{Name: "user_list", Description: "查看用户列表、角色与权限（管理员）",
			InputSchema: schema(map[string]any{})},
		{Name: "user_create", Description: "创建用户并指定角色与额外权限（管理员）",
			InputSchema: schema(map[string]any{
				"username": strProp("登录名"), "password": strProp("初始密码"), "displayName": strProp("显示名"),
				"role": strProp("admin / editor / viewer"), "extraPerms": arrProp("额外权限"),
			}, "username", "password", "role")},
		{Name: "user_update", Description: "修改用户角色、权限、显示名或禁用状态（管理员）",
			InputSchema: schema(map[string]any{
				"id": strProp("用户 ID"), "role": strProp("admin / editor / viewer"),
				"extraPerms": arrProp("额外权限"), "revokedPerms": arrProp("撤销权限"),
				"displayName": strProp("显示名"), "disabled": boolProp("是否禁用"),
			}, "id")},
		{Name: "apikey_list", Description: "查看 API Key（含 AccessKeyId，用于 S3 SigV4）",
			InputSchema: schema(map[string]any{})},
		{Name: "apikey_create", Description: "创建 API Key；返回 SecretAccessKey 明文，之后也可随时用 apikey_reveal 再取回",
			InputSchema: schema(map[string]any{
				"name": strProp("密钥名称"), "userId": strProp("所属用户 ID，管理员可指定"),
				"scopes": arrProp("权限范围，留空继承用户全部权限"), "expiresInDays": numProp("有效期天数，0 表示永不过期"),
			}, "name")},
		{Name: "apikey_reveal", Description: "随时查看指定 API Key 的完整凭据（AccessKeyId + SecretAccessKey 明文 / REST Bearer 令牌），仅本人或管理员可用",
			InputSchema: schema(map[string]any{
				"id": strProp("密钥 ID，可由 apikey_list 获取"),
			}, "id")},
	}
}

// Resources 把文档暴露为 MCP 资源。
func (s *Server) Resources() []map[string]any {
	out := make([]map[string]any, 0, 4)
	for _, d := range docs.List() {
		out = append(out, map[string]any{
			"uri": "docs://" + d.Name, "name": d.Title,
			"mimeType": "text/markdown", "description": "S3Bridge 官方文档：" + d.Title,
		})
	}
	return out
}

func (s *Server) Resource(uri string) (string, bool) {
	if !strings.HasPrefix(uri, "docs://") {
		return "", false
	}
	return docs.Get(strings.TrimPrefix(uri, "docs://"))
}

// CallTool 执行一个工具，返回文本结果与是否为错误。
func (s *Server) CallTool(ctx context.Context, name string, args map[string]any, p *auth.Principal) (string, bool) {
	c := s.core
	if c == nil {
		return jsonPretty(map[string]any{"error": "MCP 后端未初始化"}), true
	}
	actor := "system"
	if p != nil && p.Username != "" {
		actor = p.Username
	}
	perm := func(what store.Permission) error {
		if p == nil || !p.Can(what) {
			return fmt.Errorf("权限不足：需要 %s", what)
		}
		return nil
	}
	reload := func() { _ = c.Health.Reload() }
	fail := func(err error) (string, bool) { return jsonPretty(map[string]any{"error": err.Error()}), true }
	ok := func(v any) (string, bool) { return jsonPretty(v), false }

	switch name {
	// ---------- 站点 / 系统 ----------
	case "site_info":
		var info map[string]any
		_ = c.Store.View(func(st *store.State) error {
			healthy, unhealthy, alerts := 0, 0, 0
			for _, pl := range st.Plugins {
				if pl.Status == store.StatusHealthy {
					healthy++
				}
				if pl.Status == store.StatusUnhealthy {
					unhealthy++
				}
			}
			for _, a := range st.Alerts {
				if a.AckedAt == nil {
					alerts++
				}
			}
			var total int64
			for _, o := range st.Objects {
				total += o.Size
			}
			info = map[string]any{
				"siteName": st.Settings.SiteName, "version": c.Version,
				"startedAt":  c.StartedAt.Format(time.RFC3339),
				"uptime":     time.Since(c.StartedAt).Round(time.Second).String(),
				"s3Endpoint": "http(s)://<host>/s3", "s3Region": c.S3.Region(),
				"plugins": len(st.Plugins), "healthyPlugins": healthy, "unhealthyPlugins": unhealthy,
				"users": len(st.Users), "buckets": len(st.Buckets),
				"objects": len(st.Objects), "totalBytes": total,
				"openAlerts": alerts, "scheduler": c.Health.Status(),
			}
			return nil
		})
		if c.Updater != nil {
			us := c.Updater.Status()
			info["updateAvailable"] = us.UpdateAvailable
			info["latestVersion"] = ""
			if us.Latest != nil {
				info["latestVersion"] = us.Latest.Tag
			}
		}
		return ok(info)

	case "system_update_status":
		return ok(c.Updater.Status())

	case "system_update_check":
		force, _ := args["force"].(bool)
		st, err := c.Updater.Check(ctx, force)
		if err != nil {
			return fail(err)
		}
		return ok(st)

	case "system_update_apply":
		if err := perm(store.PermSystemManage); err != nil {
			return fail(err)
		}
		a, err := c.Updater.Apply(actor)
		if err != nil {
			return fail(err)
		}
		return ok(a)

	case "system_update_dismiss":
		tag, _ := args["tag"].(string)
		if tag == "" {
			return fail(errors.New("tag 不能为空"))
		}
		c.Updater.Dismiss(tag)
		return ok(map[string]any{"dismissed": tag, "note": "首页更新卡片仍会显示该版本"})

	case "site_stats":
		var stats map[string]any
		_ = c.Store.View(func(st *store.State) error {
			byCat, byBucket, byPlugin := map[string]int{}, map[string]int{}, map[string]int{}
			var size int64
			for _, o := range st.Objects {
				byCat[string(o.Category)]++
				byBucket[o.Bucket]++
				byPlugin[o.Provider]++
				size += o.Size
			}
			statuses := map[string]int{}
			for _, pl := range st.Plugins {
				statuses[string(pl.Status)]++
			}
			stats = map[string]any{
				"objects": len(st.Objects), "totalBytes": size,
				"byCategory": byCat, "byBucket": byBucket, "byPlugin": byPlugin,
				"pluginStatus": statuses, "alerts": len(st.Alerts), "changes": len(st.Changes),
			}
			return nil
		})
		return ok(stats)

	case "system_settings_get":
		var settings store.Settings
		_ = c.Store.View(func(st *store.State) error { settings = st.Settings; return nil })
		return ok(settings)

	case "system_settings_update":
		if err := perm(store.PermSystemManage); err != nil {
			return fail(err)
		}
		if err := c.Store.Update(func(st *store.State) error {
			setStr := func(k string, target *string) {
				if v := argString(args, k); v != "" {
					*target = v
				}
			}
			setStr("siteName", &st.Settings.SiteName)
			setStr("defaultPlugin", &st.Settings.DefaultPlugin)
			setStr("announcement", &st.Settings.Announcement)
			if v, ok := args["healthEnabled"]; ok {
				st.Settings.HealthEnabled = argBool(v, true)
			}
			if v, ok := args["alertOnFailure"]; ok {
				st.Settings.AlertOnFailure = argBool(v, true)
			}
			if n := argInt(args, "healthIntervalSecs", 0); n >= 10 {
				st.Settings.HealthIntervalSecs = n
			}
			return nil
		}); err != nil {
			return fail(err)
		}
		reload()
		var settings store.Settings
		_ = c.Store.View(func(st *store.State) error { settings = st.Settings; return nil })
		return ok(settings)

	// ---------- 插件 ----------
	case "plugin_list":
		if err := perm(store.PermPluginRead); err != nil {
			return fail(err)
		}
		list, err := c.Plugins.List()
		if err != nil {
			return fail(err)
		}
		return ok(list)

	case "plugin_get", "plugin_registry":
		if err := perm(store.PermPluginRead); err != nil {
			return fail(err)
		}
		if name == "plugin_registry" {
			var custom []map[string]any
			_ = c.Store.View(func(st *store.State) error {
				for _, p := range st.Plugins {
					if !p.Custom {
						continue
					}
					custom = append(custom, map[string]any{
						"id": p.ID, "kind": p.Kind, "name": p.Name, "version": p.Version,
						"enabled": p.Enabled, "revision": p.Revision,
					})
				}
				return nil
			})
			return ok(map[string]any{
				"registry":      c.Registry.List(),
				"customPlugins": custom,
				"permissions":   store.AllPermissions,
				"note":          "registry 是编译进二进制的内置类型；customPlugins 由 plugin_create 创建，源码可用 plugin_code_upload / plugin_code_patch 即时修改。",
			})
		}
		v, err := c.Plugins.Get(argString(args, "id"))
		if err != nil {
			return fail(err)
		}
		return ok(v)

	case "plugin_docs":
		body, _ := docs.Get("plugin-development")
		return ok(map[string]any{
			"document": body, "registry": c.Registry.List(),
			"note": "文档随服务端版本同步；参数 Schema 以 plugin_registry 返回的实时定义为准。",
		})

	case "s3_docs":
		body, _ := docs.Get("s3-api")
		return ok(map[string]any{"document": body, "region": c.S3.Region(), "endpoint": "/s3"})

	case "installation_docs":
		body, _ := docs.Get("installation")
		return ok(map[string]any{"document": body})

	case "plugin_debug":
		if err := perm(store.PermPluginDebug); err != nil {
			return fail(err)
		}
		report, err := c.Plugins.Debug(ctx, argString(args, "id"))
		if err != nil {
			return fail(err)
		}
		return ok(report)

	case "plugin_patch":
		if err := perm(store.PermPluginWrite); err != nil {
			return fail(err)
		}
		patch, err := patchFromArgs(args)
		if err != nil {
			return fail(err)
		}
		v, err := c.Plugins.Patch(argString(args, "id"), patch, actor)
		if err != nil {
			return fail(err)
		}
		reload()
		return ok(summary(v))

	case "plugin_replace":
		if err := perm(store.PermPluginWrite); err != nil {
			return fail(err)
		}
		full, err := fullFromArgs(args)
		if err != nil {
			return fail(err)
		}
		v, err := c.Plugins.Replace(argString(args, "id"), full, actor)
		if err != nil {
			return fail(err)
		}
		reload()
		return ok(summary(v))

	case "plugin_update":
		if err := perm(store.PermPluginWrite); err != nil {
			return fail(err)
		}
		v, err := c.Plugins.Update(argString(args, "id"), plugin.UpdateSpec{
			Version:        argString(args, "version"),
			SyncParams:     argBool(args["syncParams"], true),
			ResetToDefault: argBool(args["resetToDefault"], false),
			Note:           argString(args, "note"),
		}, actor)
		if err != nil {
			return fail(err)
		}
		return ok(summary(v))

	case "plugin_params_set":
		if err := perm(store.PermPluginWrite); err != nil {
			return fail(err)
		}
		params, okMap := args["params"].(map[string]any)
		if !okMap {
			return fail(errors.New("params 必须是对象"))
		}
		v, err := c.Plugins.SetParams(argString(args, "id"), anyToStringMap(params), actor)
		if err != nil {
			return fail(err)
		}
		return ok(summary(v))

	case "plugin_toggle":
		if err := perm(store.PermPluginWrite); err != nil {
			return fail(err)
		}
		v, err := c.Plugins.Toggle(argString(args, "id"), argBool(args["enabled"], false), actor)
		if err != nil {
			return fail(err)
		}
		reload()
		return ok(summary(v))

	case "plugin_health_check":
		if err := perm(store.PermPluginWrite); err != nil {
			return fail(err)
		}
		id := argString(args, "id")
		if err := c.Health.RunNow(ctx, id); err != nil {
			return fail(err)
		}
		v, err := c.Plugins.Get(id)
		if err != nil {
			return fail(err)
		}
		return ok(summary(v))

	case "plugin_health_config":
		if err := perm(store.PermPluginWrite); err != nil {
			return fail(err)
		}
		id := argString(args, "id")
		cur, err := c.Plugins.Get(id)
		if err != nil {
			return fail(err)
		}
		h := cur.Health
		if v, ok := args["enabled"]; ok {
			h.Enabled = argBool(v, true)
		}
		if n := argInt(args, "intervalSecs", 0); n > 0 {
			h.IntervalSecs = n
		}
		if n := argInt(args, "timeoutSecs", 0); n > 0 {
			h.TimeoutSecs = n
		}
		if n := argInt(args, "failThreshold", 0); n > 0 {
			h.FailThreshold = n
		}
		if v := argString(args, "probeMode"); v != "" {
			h.ProbeMode = v
		}
		v, err := c.Plugins.SetHealth(id, h, actor)
		if err != nil {
			return fail(err)
		}
		reload()
		return ok(summary(v))

	case "plugin_changes":
		if err := perm(store.PermPluginRead); err != nil {
			return fail(err)
		}
		limit := argInt(args, "limit", 30)
		id := argString(args, "id")
		var changes []store.ChangeLog
		_ = c.Store.View(func(st *store.State) error {
			for i := len(st.Changes) - 1; i >= 0 && len(changes) < limit; i-- {
				if id == "" || st.Changes[i].PluginID == id {
					changes = append(changes, st.Changes[i])
				}
			}
			return nil
		})
		return ok(changes)

	case "plugin_parse_token":
		raw := argString(args, "authorization")
		exp, err := workbuddy.ParseTokenExpiry(raw)
		if err != nil {
			return fail(err)
		}
		sub, _ := workbuddy.ParseTokenSubject(raw)
		return ok(map[string]any{
			"tokenExpiresAt": exp.Format(time.RFC3339),
			"unix":           exp.Unix(), "xUserId": sub,
			"hint": "把 tokenExpiresAt 填入 token_expires_at，把 xUserId 填入 x_user_id。",
		})

	// ---------- 插件开发：创建 / 源码 / 差量 / 校验 / 导出 / 删除 ----------
	case "plugin_create":
		if err := perm(store.PermPluginWrite); err != nil {
			return fail(err)
		}
		spec := plugin.CreateSpec{
			Kind: argString(args, "kind"), Name: argString(args, "name"),
			Version: argString(args, "version"), Description: argString(args, "description"),
			Author: argString(args, "author"), Enabled: argBool(args["enabled"], true),
		}
		if v, ok := args["priority"]; ok && v != nil {
			n := int(argIntVal(v))
			spec.Priority = &n
		}
		if v, ok := args["fileTypes"]; ok && v != nil {
			types, err := stringList(v, "fileTypes")
			if err != nil {
				return fail(err)
			}
			spec.FileTypes = types
		}
		if _, ok := args["definition"]; ok && args["definition"] != nil {
			raw, err := definitionFromArgs(args, "definition")
			if err != nil {
				return fail(err)
			}
			d, err := plugin.ParseDefinition(raw)
			if err != nil {
				return fail(err)
			}
			spec.Definition = d
		}
		if spec.Definition == nil && argString(args, "template") != "" {
			src, _, err := c.Plugins.SourceJSON(argString(args, "template"))
			if err != nil {
				return fail(fmt.Errorf("读取模板 %s: %w", argString(args, "template"), err))
			}
			d, err := plugin.ParseDefinition([]byte(src))
			if err != nil {
				return fail(err)
			}
			spec.Definition = d
		}
		if m, ok := args["params"].(map[string]any); ok {
			spec.Params = anyToStringMap(m)
		}
		v, err := c.Plugins.Create(spec, actor)
		if err != nil {
			return fail(err)
		}
		reload()
		return ok(map[string]any{
			"plugin": summary(v), "created": v.ID,
			"next": "用 plugin_code_upload / plugin_code_patch 完善源码 → plugin_validate → plugin_debug → upload_test 验证 → plugin_toggle 启用",
		})

	case "plugin_code_get":
		if err := perm(store.PermPluginRead); err != nil {
			return fail(err)
		}
		id := argString(args, "id")
		src, editable, err := c.Plugins.SourceJSON(id)
		if err != nil {
			return fail(err)
		}
		v, err := c.Plugins.Get(id)
		if err != nil {
			return fail(err)
		}
		var def any
		_ = json.Unmarshal([]byte(src), &def)
		note := "源码可编辑：用 plugin_code_upload 全量替换或 plugin_code_patch 差量修改。"
		if !editable {
			note = "内置插件由 Go 代码实现，源码只读；可用 plugin_create 的 template 参数派生自定义插件后再改。"
		}
		return ok(map[string]any{
			"id": id, "custom": v.Custom, "editable": editable, "revision": v.Revision,
			"source": def, "sourceJSON": src, "manifest": v.Manifest, "note": note,
		})

	case "plugin_code_upload":
		if err := perm(store.PermPluginWrite); err != nil {
			return fail(err)
		}
		id := argString(args, "id")
		raw, err := definitionFromArgs(args, "definition")
		if err != nil {
			return fail(err)
		}
		v, err := c.Plugins.SetDefinition(id, raw, revisionFromArgs(args), actor)
		if err != nil {
			return fail(err)
		}
		reload()
		src, _, _ := c.Plugins.SourceJSON(id)
		return ok(map[string]any{
			"plugin": summary(v), "sourceJSON": src,
			"note": "源码已即时编译生效，无需重启服务；可用 plugin_debug / upload_test 验证",
		})

	case "plugin_code_patch":
		if err := perm(store.PermPluginWrite); err != nil {
			return fail(err)
		}
		id := argString(args, "id")
		patch, isMap := args["patch"].(map[string]any)
		if !isMap {
			return fail(errors.New("patch 必须是对象"))
		}
		v, err := c.Plugins.PatchDefinition(id, patch, revisionFromArgs(args), actor)
		if err != nil {
			return fail(err)
		}
		reload()
		src, _, _ := c.Plugins.SourceJSON(id)
		return ok(map[string]any{
			"plugin": summary(v), "sourceJSON": src,
			"note": "仅指定字段被修改，其余源码保持不变；改动已写入 ChangeLog",
		})

	case "plugin_validate":
		raw, err := definitionFromArgs(args, "definition")
		if err != nil {
			return fail(err)
		}
		params := map[string]string{}
		if m, ok := args["params"].(map[string]any); ok {
			params = anyToStringMap(m)
		}
		result, err := plugin.ValidateDefinition(raw, params)
		if err != nil {
			return fail(err)
		}
		return ok(result)

	case "plugin_export":
		if err := perm(store.PermPluginRead); err != nil {
			return fail(err)
		}
		id := argString(args, "id")
		src, _, err := c.Plugins.SourceJSON(id)
		if err != nil {
			return fail(err)
		}
		d, err := plugin.ParseDefinition([]byte(src))
		if err != nil {
			return fail(err)
		}
		format := argString(args, "format")
		if format == "" {
			format = "json"
		}
		content, filename := src, d.Kind+".plugin.json"
		if format == "go" {
			content, err = plugin.ExportGo(d)
			if err != nil {
				return fail(err)
			}
			filename = strings.ReplaceAll(d.Kind, "-", "") + "/plugin.go"
		} else if format != "json" {
			return fail(fmt.Errorf("format 仅支持 json / go，收到 %q", format))
		}
		return ok(map[string]any{
			"id": id, "format": format, "filename": filename, "content": content,
			"note": "json 可用 plugin_create 的 definition 参数直接导入；go 可放进 internal/plugins/ 内置化",
		})

	case "plugin_delete":
		if err := perm(store.PermPluginWrite); err != nil {
			return fail(err)
		}
		id := argString(args, "id")
		if err := c.Plugins.Delete(id, actor); err != nil {
			return fail(err)
		}
		reload()
		return ok(map[string]any{"deleted": id, "note": "变更记录已保留，可用 plugin_changes 回溯"})

	// ---------- 告警 ----------
	case "alerts_list":
		if err := perm(store.PermPluginRead); err != nil {
			return fail(err)
		}
		limit := argInt(args, "limit", 50)
		var alerts []store.Alert
		_ = c.Store.View(func(st *store.State) error {
			for i := range st.Alerts {
				if st.Alerts[i].AckedAt == nil {
					alerts = append(alerts, st.Alerts[i])
				}
			}
			for i := range st.Alerts {
				if st.Alerts[i].AckedAt != nil && len(alerts) < limit {
					alerts = append(alerts, st.Alerts[i])
				}
			}
			if len(alerts) > limit {
				alerts = alerts[:limit]
			}
			return nil
		})
		return ok(alerts)

	case "alerts_ack":
		if err := perm(store.PermPluginWrite); err != nil {
			return fail(err)
		}
		if err := c.Plugins.AckAlert(argString(args, "id"), actor); err != nil {
			return fail(err)
		}
		return ok(map[string]any{"acked": true})

	// ---------- 上传 / 存储桶 ----------
	case "upload_test":
		if err := perm(store.PermUploadWrite); err != nil {
			return fail(err)
		}
		raw, err := base64.StdEncoding.DecodeString(argString(args, "contentBase64"))
		if err != nil {
			return fail(fmt.Errorf("contentBase64 解码失败: %w", err))
		}
		owner := ""
		if p != nil {
			owner = p.UserID
		}
		obj, err := c.Uploads.Put(ctx, upload.PutRequest{
			Reader: bytes.NewReader(raw), FileName: argString(args, "fileName"),
			MimeType: argString(args, "contentType"), Size: int64(len(raw)),
			Bucket: argString(args, "bucket"), PluginID: argString(args, "pluginId"),
			OwnerID: owner, IsAdmin: p != nil && p.IsAdmin(),
		})
		if err != nil {
			return fail(err)
		}
		return ok(obj)

	case "uploads_list":
		if err := perm(store.PermUploadRead); err != nil {
			return fail(err)
		}
		owner := ""
		if p != nil {
			owner = p.UserID
		}
		res, err := c.Uploads.List(upload.ListFilter{
			Bucket: argString(args, "bucket"), Category: store.FileCategory(argString(args, "category")),
			PluginID: argString(args, "pluginId"), Keyword: argString(args, "keyword"),
			Limit: argInt(args, "limit", 50), OwnerID: owner, IsAdmin: p != nil && p.IsAdmin(),
		})
		if err != nil {
			return fail(err)
		}
		return ok(res)

	case "bucket_list":
		if err := perm(store.PermBucketRead); err != nil {
			return fail(err)
		}
		type row struct {
			store.Bucket
			Objects   int   `json:"objects"`
			TotalSize int64 `json:"totalSize"`
		}
		var rows []row
		_ = c.Store.View(func(st *store.State) error {
			for _, b := range st.Buckets {
				r := row{Bucket: b}
				for _, o := range st.Objects {
					if o.Bucket == b.Name {
						r.Objects++
						r.TotalSize += o.Size
					}
				}
				rows = append(rows, r)
			}
			return nil
		})
		return ok(rows)

	case "bucket_create":
		if err := perm(store.PermBucketWrite); err != nil {
			return fail(err)
		}
		bname := argString(args, "name")
		if err := upload.ValidateBucketName(bname); err != nil {
			return fail(err)
		}
		owner := ""
		if p != nil {
			owner = p.UserID
		}
		b := store.Bucket{
			ID: "bkt_" + bname, Name: bname, OwnerID: owner,
			Description: argString(args, "description"), PublicRead: argBool(args["publicRead"], false),
			CreatedAt: store.NowUTC(),
		}
		if err := c.Store.Update(func(st *store.State) error {
			if store.BucketByName(st, bname) != nil {
				return errors.New("存储桶已存在")
			}
			st.Buckets = append(st.Buckets, b)
			return nil
		}); err != nil {
			return fail(err)
		}
		return ok(b)

	case "bucket_update":
		if err := perm(store.PermBucketWrite); err != nil {
			return fail(err)
		}
		bname := argString(args, "name")
		var out store.Bucket
		if err := c.Store.Update(func(st *store.State) error {
			b := store.BucketByName(st, bname)
			if b == nil {
				return errors.New("存储桶不存在")
			}
			if v, ok := args["description"]; ok {
				b.Description = fmt.Sprint(v)
			}
			if v, ok := args["publicRead"]; ok {
				b.PublicRead = argBool(v, false)
			}
			out = *b
			return nil
		}); err != nil {
			return fail(err)
		}
		return ok(out)

	case "bucket_delete":
		if err := perm(store.PermBucketWrite); err != nil {
			return fail(err)
		}
		bname := argString(args, "name")
		if err := c.Store.Update(func(st *store.State) error {
			for _, o := range st.Objects {
				if o.Bucket == bname {
					return errors.New("存储桶非空，请先删除对象")
				}
			}
			for i := range st.Buckets {
				if st.Buckets[i].Name == bname {
					st.Buckets = append(st.Buckets[:i], st.Buckets[i+1:]...)
					return nil
				}
			}
			return errors.New("存储桶不存在")
		}); err != nil {
			return fail(err)
		}
		return ok(map[string]any{"deleted": bname})

	// ---------- 用户 / 密钥 ----------
	case "user_list":
		if err := perm(store.PermUserManage); err != nil {
			return fail(err)
		}
		var users []store.PublicUser
		_ = c.Store.View(func(st *store.State) error {
			for i := range st.Users {
				users = append(users, st.Users[i].Public())
			}
			return nil
		})
		return ok(users)

	case "user_create":
		if err := perm(store.PermUserManage); err != nil {
			return fail(err)
		}
		username, password := argString(args, "username"), argString(args, "password")
		if username == "" || len(password) < 8 {
			return fail(errors.New("username 不能为空，password 至少 8 位"))
		}
		role := store.Role(argString(args, "role"))
		if role != store.RoleAdmin && role != store.RoleEditor && role != store.RoleViewer {
			return fail(errors.New("role 必须是 admin / editor / viewer"))
		}
		hash, err := auth.HashPassword(password)
		if err != nil {
			return fail(err)
		}
		u := store.User{
			ID: "usr_" + randomID(10), Username: username,
			DisplayName:  pick(argString(args, "displayName"), username),
			PasswordHash: hash, Role: role, ExtraPerms: anyToPerms(args["extraPerms"]),
			CreatedAt: store.NowUTC(), UpdatedAt: store.NowUTC(),
		}
		if err := c.Store.Update(func(st *store.State) error {
			if store.UserByName(st, username) != nil {
				return errors.New("用户名已存在")
			}
			st.Users = append(st.Users, u)
			return nil
		}); err != nil {
			return fail(err)
		}
		return ok(u.Public())

	case "user_update":
		if err := perm(store.PermUserManage); err != nil {
			return fail(err)
		}
		id := argString(args, "id")
		var out store.PublicUser
		if err := c.Store.Update(func(st *store.State) error {
			u := store.UserByID(st, id)
			if u == nil {
				return errors.New("用户不存在")
			}
			if r := store.Role(argString(args, "role")); r != "" {
				if r != store.RoleAdmin && r != store.RoleEditor && r != store.RoleViewer {
					return errors.New("非法角色")
				}
				u.Role = r
			}
			if v, ok := args["extraPerms"]; ok {
				u.ExtraPerms = anyToPerms(v)
			}
			if v, ok := args["revokedPerms"]; ok {
				u.RevokedPerms = anyToPerms(v)
			}
			if v := argString(args, "displayName"); v != "" {
				u.DisplayName = v
			}
			if v, ok := args["disabled"]; ok {
				u.Disabled = argBool(v, false)
			}
			u.UpdatedAt = store.NowUTC()
			out = u.Public()
			return nil
		}); err != nil {
			return fail(err)
		}
		return ok(out)

	case "apikey_list":
		var keys []map[string]any
		_ = c.Store.View(func(st *store.State) error {
			for _, k := range st.APIKeys {
				if p != nil && !p.IsAdmin() && k.UserID != p.UserID {
					continue
				}
				keys = append(keys, map[string]any{
					"id": k.ID, "name": k.Name, "userId": k.UserID, "accessKey": k.AccessKey,
					"secretHint": k.SecretHint, "scopes": k.Scopes, "revoked": k.Revoked,
					"createdAt": k.CreatedAt, "lastUsedAt": k.LastUsedAt, "expiresAt": k.ExpiresAt,
				})
			}
			return nil
		})
		return ok(keys)

	case "apikey_create":
		userID := argString(args, "userId")
		if p != nil {
			if userID == "" || !p.IsAdmin() {
				userID = p.UserID
			}
		}
		if userID == "" {
			return fail(errors.New("缺少 userId"))
		}
		var expires *time.Time
		if days := argInt(args, "expiresInDays", 0); days > 0 {
			t := time.Now().UTC().AddDate(0, 0, days)
			expires = &t
		}
		key, secret, err := c.Auth.CreateAPIKey(userID, argString(args, "name"), anyToPerms(args["scopes"]), expires)
		if err != nil {
			return fail(err)
		}
		return ok(map[string]any{
			"id": key.ID, "accessKeyId": key.AccessKey,
			"secretAccessKey": secret,
			"note":            "SecretAccessKey 已加密保存，可随时用 apikey_reveal 再次取回。",
			"s3":              map[string]any{"endpoint": "/s3", "region": c.S3.Region()},
		})

	case "apikey_reveal":
		id := argString(args, "id")
		if id == "" {
			return fail(errors.New("缺少 id"))
		}
		var out map[string]any
		err := c.Store.View(func(st *store.State) error {
			k := store.APIKeyByID(st, id)
			if k == nil {
				return errors.New("密钥不存在")
			}
			if p != nil && !p.IsAdmin() && k.UserID != p.UserID {
				return errors.New("无权查看他人密钥")
			}
			secret, err := c.Auth.Secret(k)
			if err != nil {
				return fmt.Errorf("解密密钥: %w", err)
			}
			out = map[string]any{
				"id": k.ID, "name": k.Name, "userId": k.UserID,
				"accessKeyId": k.AccessKey, "secretAccessKey": secret,
				"bearerToken": secret, "scopes": k.Scopes,
				"revoked": k.Revoked, "expiresAt": k.ExpiresAt,
				"createdAt": k.CreatedAt, "lastUsedAt": k.LastUsedAt,
				"endpoint": "/s3", "region": c.S3.Region(),
			}
			return nil
		})
		if err != nil {
			return fail(err)
		}
		return ok(out)

	default:
		return jsonPretty(map[string]any{"error": "未知工具: " + name, "hint": "调用 tools/list 查看可用工具"}), true
	}
}

// ---------- helpers ----------

func summary(v plugin.View) map[string]any {
	labels := v.FileTypeLabels()
	return map[string]any{
		"id": v.ID, "name": v.Name, "version": v.Version, "kind": v.Kind,
		"enabled": v.Enabled, "priority": v.Priority,
		"fileTypes": plugin.FileTypeStrings(v.FileTypes), "fileTypeLabels": labels,
		"status": v.Status, "statusMessage": v.StatusMessage,
		"revision": v.Revision, "warning": v.Warning, "health": v.Health,
		"custom": v.Custom, "editable": v.Editable, "updatedAt": v.UpdatedAt,
	}
}

// definitionFromArgs 同时接受对象与 JSON 字符串，方便 Agent 直接传结构化定义。
func definitionFromArgs(args map[string]any, key string) ([]byte, error) {
	v, ok := args[key]
	if !ok || v == nil {
		return nil, fmt.Errorf("缺少 %s", key)
	}
	switch x := v.(type) {
	case string:
		if strings.TrimSpace(x) == "" {
			return nil, fmt.Errorf("%s 不能为空", key)
		}
		return []byte(x), nil
	case map[string]any:
		return json.Marshal(x)
	default:
		return nil, fmt.Errorf("%s 必须是对象或 JSON 字符串", key)
	}
}

func revisionFromArgs(args map[string]any) *int64 {
	if v, ok := args["expectedRevision"]; ok && v != nil {
		n := argIntVal(v)
		return &n
	}
	return nil
}

func patchFromArgs(args map[string]any) (plugin.Patch, error) {
	p := plugin.Patch{}
	if v, ok := args["name"]; ok {
		s := fmt.Sprint(v)
		p.Name = &s
	}
	if v, ok := args["description"]; ok {
		s := fmt.Sprint(v)
		p.Description = &s
	}
	if v, ok := args["enabled"]; ok {
		b := argBool(v, false)
		p.Enabled = &b
	}
	if v, ok := args["priority"]; ok {
		n := int(argIntVal(v))
		p.Priority = &n
	}
	if v, ok := args["fileTypes"]; ok {
		list, err := stringList(v, "fileTypes")
		if err != nil {
			return p, err
		}
		types, err := plugin.ParseFileTypes(list)
		if err != nil {
			return p, err
		}
		p.FileTypes = &types
	}
	if v, ok := args["expectedRevision"]; ok {
		n := argIntVal(v)
		p.ExpectedRevision = &n
	}
	if v, ok := args["params"]; ok {
		m, ok := v.(map[string]any)
		if !ok {
			return p, errors.New("params 必须是对象")
		}
		p.Params = anyToStringMap(m)
	}
	if v, ok := args["health"]; ok {
		m, ok := v.(map[string]any)
		if !ok {
			return p, errors.New("health 必须是对象")
		}
		h, err := healthFromMap(m)
		if err != nil {
			return p, err
		}
		p.Health = &h
	}
	return p, nil
}

func fullFromArgs(args map[string]any) (plugin.Full, error) {
	f := plugin.Full{
		Name: argString(args, "name"), Description: argString(args, "description"),
		Enabled: argBool(args["enabled"], true), Priority: argInt(args, "priority", 100),
	}
	if v, ok := args["fileTypes"]; ok {
		list, err := stringList(v, "fileTypes")
		if err != nil {
			return f, err
		}
		types, err := plugin.ParseFileTypes(list)
		if err != nil {
			return f, err
		}
		f.FileTypes = types
	}
	if v, ok := args["params"]; ok {
		m, ok := v.(map[string]any)
		if !ok {
			return f, errors.New("params 必须是对象")
		}
		f.Params = anyToStringMap(m)
	}
	if v, ok := args["health"]; ok {
		m, ok := v.(map[string]any)
		if !ok {
			return f, errors.New("health 必须是对象")
		}
		h, err := healthFromMap(m)
		if err != nil {
			return f, err
		}
		f.Health = h
	}
	if v, ok := args["expectedRevision"]; ok {
		n := argIntVal(v)
		f.ExpectedRevision = &n
	}
	return f, nil
}

func healthFromMap(m map[string]any) (store.HealthConfig, error) {
	h := store.HealthConfig{
		Enabled:       argBool(m["enabled"], true),
		IntervalSecs:  argInt(m, "intervalSecs", 300),
		TimeoutSecs:   argInt(m, "timeoutSecs", 8),
		FailThreshold: argInt(m, "failThreshold", 2),
		ProbeMode:     argString(m, "probeMode"),
	}
	if h.ProbeMode == "" {
		h.ProbeMode = "ping"
	}
	return h, nil
}

func argString(m map[string]any, key string) string {
	v, ok := m[key]
	if !ok || v == nil {
		return ""
	}
	return fmt.Sprint(v)
}

func argBool(v any, def bool) bool {
	switch x := v.(type) {
	case bool:
		return x
	case string:
		if b, err := strconv.ParseBool(x); err == nil {
			return b
		}
		return x == "true" || x == "1" || x == "yes"
	case float64:
		return x != 0
	case nil:
		return def
	}
	return def
}

func argInt(m map[string]any, key string, def int) int {
	if v, ok := m[key]; ok {
		return int(argIntVal(v))
	}
	return def
}

func argIntVal(v any) int64 {
	switch x := v.(type) {
	case float64:
		return int64(x)
	case int:
		return int64(x)
	case int64:
		return x
	case string:
		if n, err := strconv.ParseInt(x, 10, 64); err == nil {
			return n
		}
	}
	return 0
}

func anyToStringMap(m map[string]any) map[string]string {
	out := make(map[string]string, len(m))
	for k, v := range m {
		switch x := v.(type) {
		case nil:
			continue
		case bool:
			out[k] = strconv.FormatBool(x)
		case float64:
			out[k] = strconv.FormatFloat(x, 'f', -1, 64)
		default:
			out[k] = fmt.Sprint(x)
		}
	}
	return out
}

func anyToPerms(v any) []store.Permission {
	var out []store.Permission
	switch x := v.(type) {
	case []any:
		for _, item := range x {
			out = append(out, store.Permission(fmt.Sprint(item)))
		}
	case []string:
		for _, item := range x {
			out = append(out, store.Permission(item))
		}
	}
	return out
}

// stringList 接受数组或逗号分隔字符串，用于 plugin_* 的 fileTypes 等参数。
func stringList(v any, key string) ([]string, error) {
	switch x := v.(type) {
	case []any:
		out := make([]string, 0, len(x))
		for _, item := range x {
			out = append(out, fmt.Sprint(item))
		}
		return out, nil
	case []string:
		return x, nil
	case string:
		if strings.TrimSpace(x) == "" {
			return []string{}, nil
		}
		parts := strings.Split(x, ",")
		out := make([]string, 0, len(parts))
		for _, p := range parts {
			if s := strings.TrimSpace(p); s != "" {
				out = append(out, s)
			}
		}
		return out, nil
	case nil:
		return nil, nil
	default:
		return nil, fmt.Errorf("%s 必须是字符串数组或逗号分隔字符串", key)
	}
}

func jsonPretty(v any) string {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return "{\n  \"error\": \"序列化失败\"\n}"
	}
	return string(b)
}

func pick(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func randomID(n int) string {
	const alphabet = "abcdefghijklmnopqrstuvwxyz0123456789"
	b := make([]byte, n)
	if _, err := rand.Read(b); err == nil {
		for i := range b {
			b[i] = alphabet[int(b[i])%len(alphabet)]
		}
		return string(b)
	}
	return strconv.FormatInt(time.Now().UnixNano(), 36)[:n]
}
