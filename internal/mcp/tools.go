package mcp

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/Mr-losfer/s3bridge/internal/auth"
	"github.com/Mr-losfer/s3bridge/internal/docs"
	"github.com/Mr-losfer/s3bridge/internal/obs"
	"github.com/Mr-losfer/s3bridge/internal/plugin"
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
				"imageValidityEnabled":       boolProp("是否开启图片有效性巡检（默认开启）"),
				"imageValidityIntervalHours": numProp("巡检间隔（小时），默认 72"),
				"imageValidityFailThreshold": numProp("连续失败多少次判定失效，默认 2"),
				"accessMode":                 strProp("直链出站方式：redirect（302 跳上游，默认，不占本机带宽）或 proxy（本站中转）"),
				"invalidImageUrl":            strProp("图片失效后的占位图地址；传空串清空，留空不传则不改"),
				"openUploadRandomBucket":     boolProp("开放上传未指定 bucket 时随机挑桶（默认 true）"),
				"openUploadUsePlugins":       boolProp("开放上传走插件上传（默认 true）；关闭则直接本地归档"),
				"openUploadFallbackLocal":    boolProp("插件不可用或上传失败时回退本地归档（默认 true）"),
			})},
		{Name: "system_update_status", Description: "查看版本更新状态：当前版本、GitHub Releases 最新版、结构化更新要点（含重点标注）、自动更新进度与部署策略",
			InputSchema: schema(map[string]any{})},
		{Name: "system_update_check", Description: "立即从 GitHub Releases 检测最新版本；返回是否有更新、版本号、发布时间与重要更新内容",
			InputSchema: schema(map[string]any{"force": boolProp("强制刷新，忽略 30 秒节流（默认 false）")})},
		{Name: "system_update_apply", Description: "一键自动更新到最新版本：下载发布资产 → 校验 SHA256 → 备份 → 原子替换 → 自动重启服务；需 system:manage 权限",
			InputSchema: schema(map[string]any{})},
		{Name: "system_update_dismiss", Description: "忽略指定版本的新版本弹窗（首页更新卡片仍然显示）",
			InputSchema: schema(map[string]any{"tag": strProp("要忽略的版本号，例如 v1.1.0")}, "tag")},

		{Name: "monitor_summary", Description: "查看监控总览：请求量、错误率、延迟分位（P50/P95/P99）、上传与流量、最慢/最易错接口、运行时健康（goroutine/内存/磁盘）与最近异常；range 支持 1h/6h/24h/7d/30d",
			InputSchema: schema(map[string]any{"range": strProp("时间范围：1h/6h/24h/7d/30d，默认 24h")})},
		{Name: "monitor_storage", Description: "查看存储画像：桶容量排行、文件类型分布、Top 大文件、重复内容、断链（元数据有文件无）、孤儿文件（文件有元数据无）、超期对象与磁盘占用；可触发深度体检",
			InputSchema: schema(map[string]any{"scan": boolProp("是否触发一次深度体检（异步，默认 false）"), "days": numProp("超期判定天数，默认取系统保留策略")})},
		{Name: "monitor_errors", Description: "查询异常日志：按级别（error/warn）、分类（auth/upload/plugin/s3/storage/system/request）与关键字过滤，返回状态码、路由、耗时、操作者与错误消息",
			InputSchema: schema(map[string]any{
				"level": strProp("级别：error 或 warn"), "category": strProp("分类：auth/upload/plugin/s3/storage/system/request"),
				"q": strProp("关键字（匹配消息、路由、操作者）"), "limit": numProp("返回条数，默认 30"),
				"seconds": numProp("只查最近 N 秒"),
			})},
		{Name: "gallery_list", Description: "按时间倒序列出全站图片（跨存储桶），返回文件名、桶、对象键、大小、上传者与缩略图地址，便于快速巡视相册",
			InputSchema: schema(map[string]any{
				"bucket": strProp("只看指定存储桶"), "q": strProp("按文件名/对象键搜索"),
				"limit": numProp("返回条数，默认 24，最大 100"),
			})},

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
		{Name: "plugin_registry", Description: "查看内置插件模板清单与完整参数 Schema，用于开发新插件",
			InputSchema: schema(map[string]any{})},
		{Name: "plugin_create", Description: "创建 Go 插件：生成 plugin.json + main.go 骨架并立即加载。服务端内嵌解释器执行源码，不需要 Go 工具链，可用全部标准库（任意请求、PUT、加解密）",
			InputSchema: schema(map[string]any{
				"id":   strProp("插件 ID，全局唯一，小写字母开头，如 my-uploader"),
				"name": strProp("显示名称"), "description": strProp("说明"),
				"version": strProp("版本号，默认 1.0.0"), "author": strProp("作者"),
				"template":    strProp("模板：blank（空白骨架）或 http-multipart（HTTP 表单上传），默认 blank"),
				"files":       objProp("附加/覆盖的源码文件，键为文件名（如 helper.go），值为源码"),
				"params":      objProp("初始参数键值对"),
				"enabled":     boolProp("创建后是否立即启用，默认 false"),
				"priority":    numProp("调度权重，越大越优先被选中，默认 100"),
				"fileTypes":   arrProp("可承接的文件类型：image/video/audio/document/archive/other，留空表示全部"),
				"timeoutSecs": numProp("单次调用超时（秒），默认 60"),
			}, "id")},
		{Name: "plugin_files", Description: "列出插件文件夹内的源码与说明文件（含大小、是否入口、是否可编辑）",
			InputSchema: schema(map[string]any{"id": id}, "id")},
		{Name: "plugin_file_read", Description: "读取单个插件文件；plugin.json 与 go.mod 只读，可用于了解参数 Schema 与模块配置",
			InputSchema: schema(map[string]any{"id": id, "name": strProp("文件名，如 main.go")}, "id", "name")},
		{Name: "plugin_file_write", Description: "写入插件文件并自动加载（.go 文件）；加载失败会保留源码并返回「文件:行:列」错误，供下一轮修复",
			InputSchema: schema(map[string]any{
				"id": id, "name": strProp("文件名，如 main.go"), "content": strProp("完整文件内容"),
			}, "id", "name", "content")},
		{Name: "plugin_file_delete", Description: "删除插件文件（至少保留一个 .go 文件），删除后自动重新加载",
			InputSchema: schema(map[string]any{"id": id, "name": strProp("文件名")}, "id", "name")},
		{Name: "plugin_file_rename", Description: "重命名插件文件；若重命名的是入口文件，会同步更新 plugin.json 的 entry",
			InputSchema: schema(map[string]any{"id": id, "from": strProp("原文件名"), "to": strProp("新文件名")}, "id", "from", "to")},
		{Name: "plugin_build", Description: "重新加载插件并返回加载日志、耗时与源码 SHA-256；用于手工触发或排查",
			InputSchema: schema(map[string]any{"id": id}, "id")},
		{Name: "plugin_validate", Description: "在临时目录加载一份插件源码（不落库、不影响运行中的插件），返回加载结果与错误输出",
			InputSchema: schema(map[string]any{
				"id":       strProp("可选：以现有插件的清单为基准"),
				"files":    objProp("文件名 → 源码；至少包含一个 .go"),
				"manifest": objProp("可选清单覆盖（params/timeoutSecs/fileTypes 等）"),
			}, "files")},
		{Name: "plugin_manifest_update", Description: "更新插件清单：参数 Schema、文件类型、超时、图片有效性周期等；不触发重新加载",
			InputSchema: schema(map[string]any{"id": id, "manifest": objProp("完整 Manifest（id 会被强制为当前插件）")}, "id", "manifest")},
		{Name: "plugin_export", Description: "导出插件全部源码与清单，便于备份、迁移或在另一实例重建",
			InputSchema: schema(map[string]any{"id": id}, "id")},
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
			if v, ok := args["imageValidityEnabled"]; ok {
				st.Settings.ImageValidityEnabled = argBool(v, true)
			}
			if n := argInt(args, "imageValidityIntervalHours", 0); n >= 1 && n <= 720 {
				st.Settings.ImageValidityIntervalHours = n
			}
			if n := argInt(args, "imageValidityFailThreshold", 0); n >= 1 && n <= 10 {
				st.Settings.ImageValidityFailThreshold = n
			}
			if v, ok := args["webdavEnabled"]; ok {
				st.Settings.WebDAVEnabled = argBool(v, true)
			}
			if v, ok := args["ftpEnabled"]; ok {
				st.Settings.FTPEnabled = argBool(v, false)
			}
			setStr("ftpAddr", &st.Settings.FTPAddr)
			setStr("ftpPassivePorts", &st.Settings.FTPPassivePorts)
			setStr("ftpPublicIp", &st.Settings.FTPPublicIP)
			setStr("openUploadBucket", &st.Settings.OpenUploadBucket)
			// accessMode 只接受两个取值，写错直接报错而不是静默退回默认。
			if v := strings.TrimSpace(argString(args, "accessMode")); v != "" {
				if v != store.AccessRedirect && v != store.AccessProxy {
					return fmt.Errorf("accessMode 只能是 %s 或 %s", store.AccessRedirect, store.AccessProxy)
				}
				st.Settings.AccessMode = v
			}
			// 失效图允许显式清空，所以不能用 setStr（它只在非空时覆盖）。
			if _, ok := args["invalidImageUrl"]; ok {
				st.Settings.InvalidImageURL = strings.TrimSpace(argString(args, "invalidImageUrl"))
			}
			if v, ok := args["openUploadRandomBucket"]; ok {
				st.Settings.OpenUploadRandomBucket = argBool(v, true)
			}
			if v, ok := args["openUploadUsePlugins"]; ok {
				st.Settings.OpenUploadUsePlugins = argBool(v, true)
			}
			if v, ok := args["openUploadFallbackLocal"]; ok {
				st.Settings.OpenUploadFallbackLocal = argBool(v, true)
			}
			return nil
		}); err != nil {
			return fail(err)
		}
		var vset store.Settings
		_ = c.Store.View(func(st *store.State) error { vset = st.Settings; return nil })
		_ = c.ApplyIntegrations()
		if c.Validity != nil {
			c.Validity.Configure(vset.ImageValidityEnabled,
				time.Duration(vset.ImageValidityIntervalHours)*time.Hour, vset.ImageValidityFailThreshold)
		}
		reload()
		var settings store.Settings
		_ = c.Store.View(func(st *store.State) error { settings = st.Settings; return nil })
		return ok(settings)

	// ---------- 监控 / 相册 ----------
	case "monitor_summary":
		if err := perm(store.PermSystemManage); err != nil {
			return fail(err)
		}
		if c.Obs == nil {
			return fail(errors.New("监控采集未启用"))
		}
		rg := obs.ParseRange(argString(args, "range"))
		sum := c.Obs.Summary(rg, time.Now())
		recent, _ := c.Obs.Events(obs.EventFilter{Level: obs.LevelError, Limit: 5, Since: time.Now().Add(-24 * time.Hour)})
		return ok(map[string]any{"summary": sum, "recent": recent, "hint": "series 为时序点，slowest/errRoutes 分别是慢接口与错误接口排行"})

	case "monitor_storage":
		if err := perm(store.PermSystemManage); err != nil {
			return fail(err)
		}
		var objects []store.Object
		var buckets []store.Bucket
		var settings store.Settings
		_ = c.Store.View(func(st *store.State) error {
			objects = append(objects, st.Objects...)
			buckets = append(buckets, st.Buckets...)
			settings = st.Settings
			return nil
		})
		days := argInt(args, "days", settings.UploadRetentionDays)
		deep := argBool(args["scan"], false)
		opts := obs.AuditOptions{RetentionDays: days, TopLimit: 15}
		if deep {
			opts.ObjectDir = c.Cfg.ObjectDir
			opts.MaxScanFiles = 200000
			opts.TopLimit = 20
		}
		rep := obs.BuildStorageReport(objects, buckets, opts)
		if !deep {
			rep.Disk = obs.DiskStats(c.Cfg.ObjectDir)
		}
		return ok(map[string]any{"report": rep, "deepScan": deep,
			"hint": "deepScan=false 时只做内存聚合；传 scan=true 可检测孤儿文件与断链，耗时随对象数增长"})

	case "monitor_errors":
		if err := perm(store.PermSystemManage); err != nil {
			return fail(err)
		}
		if c.Obs == nil {
			return fail(errors.New("监控采集未启用"))
		}
		f := obs.EventFilter{
			Level: argString(args, "level"), Category: argString(args, "category"),
			Query: argString(args, "q"), Limit: argInt(args, "limit", 30),
		}
		if secs := argInt(args, "seconds", 0); secs > 0 {
			f.Since = time.Now().Add(-time.Duration(secs) * time.Second)
		}
		items, total := c.Obs.Events(f)
		return ok(map[string]any{"items": items, "total": total, "categories": obs.CategoryLabels})

	case "gallery_list":
		if err := perm(store.PermUploadRead); err != nil {
			return fail(err)
		}
		limit := argInt(args, "limit", 24)
		if limit <= 0 || limit > 100 {
			limit = 24
		}
		bucket := argString(args, "bucket")
		keyword := strings.ToLower(argString(args, "q"))
		owner := ""
		isAdmin := p != nil && p.IsAdmin()
		if p != nil {
			owner = p.UserID
		}
		out := []map[string]any{}
		total := 0
		var totalBytes int64
		_ = c.Store.View(func(st *store.State) error {
			for i := len(st.Objects) - 1; i >= 0; i-- {
				o := st.Objects[i]
				if o.Category != store.CategoryImage {
					continue
				}
				if !isAdmin && o.OwnerID != owner {
					continue
				}
				if bucket != "" && o.Bucket != bucket {
					continue
				}
				if keyword != "" && !strings.Contains(strings.ToLower(o.FileName+o.ObjectKey), keyword) {
					continue
				}
				total++
				totalBytes += o.Size
				if len(out) >= limit {
					continue
				}
				out = append(out, map[string]any{
					"id": o.ID, "bucket": o.Bucket, "objectKey": o.ObjectKey,
					"fileName": o.FileName, "size": o.Size, "mimeType": o.MimeType,
					"createdAt": o.CreatedAt, "ownerId": o.OwnerID,
					"thumb": "/api/objects/thumb?bucket=" + url.QueryEscape(o.Bucket) + "&key=" + url.QueryEscape(o.ObjectKey) + "&w=480",
				})
			}
			return nil
		})
		return ok(map[string]any{"items": out, "total": total, "totalBytes": totalBytes})

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
				"registry":      plugin.BuiltinTemplates(),
				"customPlugins": custom,
				"permissions":   store.AllPermissions,
				"note":          "registry 是随二进制发布的内置插件模板；customPlugins 由 plugin_create 创建，源码文件用 plugin_file_write 即时修改并自动重新加载。",
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
			"document": body, "registry": plugin.BuiltinTemplates(),
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
		exp, sub, err := plugin.ParseBearerToken(raw)
		if err != nil {
			return fail(err)
		}
		return ok(map[string]any{
			"tokenExpiresAt": exp.Format(time.RFC3339),
			"unix":           exp.Unix(), "xUserId": sub,
			"hint": "把 tokenExpiresAt 填入 token_expires_at，把 xUserId 填入 x_user_id。",
		})

	// ---------- 插件开发：文件夹 / 源码 / 构建 / 校验 / 导出 / 删除 ----------
	case "plugin_create":
		if err := perm(store.PermPluginWrite); err != nil {
			return fail(err)
		}
		spec := plugin.CreateSpec{
			ID: argString(args, "id"), Name: argString(args, "name"),
			Version: argString(args, "version"), Description: argString(args, "description"),
			Author: argString(args, "author"), Template: argString(args, "template"),
			Enabled: argBool(args["enabled"], false),
		}
		if n := argInt(args, "timeoutSecs", 0); n > 0 {
			spec.TimeoutSecs = n
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
		if m, ok := args["files"].(map[string]any); ok {
			spec.Files = anyToStringMap(m)
		}
		if m, ok := args["params"].(map[string]any); ok {
			spec.Params = anyToStringMap(m)
		}
		v, err := c.Plugins.Create(ctx, spec, actor)
		if err != nil {
			return fail(err)
		}
		reload()
		return ok(map[string]any{
			"plugin": summary(v), "created": v.ID,
			"next": "用 plugin_file_write 修改 main.go → plugin_build 查看加载结果 → plugin_debug 查看请求追踪 → upload_test 验证",
		})

	case "plugin_files":
		if err := perm(store.PermPluginRead); err != nil {
			return fail(err)
		}
		id := argString(args, "id")
		files, err := c.Plugins.Files(id)
		if err != nil {
			return fail(err)
		}
		return ok(map[string]any{"id": id, "files": files})

	case "plugin_file_read":
		if err := perm(store.PermPluginRead); err != nil {
			return fail(err)
		}
		fc, err := c.Plugins.FileGet(argString(args, "id"), argString(args, "name"))
		if err != nil {
			return fail(err)
		}
		return ok(fc)

	case "plugin_file_write":
		if err := perm(store.PermPluginWrite); err != nil {
			return fail(err)
		}
		id, name := argString(args, "id"), argString(args, "name")
		content := argString(args, "content")
		v, report, err := c.Plugins.FilePut(ctx, id, name, content, actor)
		if err != nil && report == nil {
			return fail(err)
		}
		reload()
		res := map[string]any{"plugin": summary(v), "build": report}
		if report != nil && !report.OK {
			res["error"] = "加载失败，源码已保存；请按 output 修复后再次写入"
		}
		return ok(res)

	case "plugin_file_delete":
		if err := perm(store.PermPluginWrite); err != nil {
			return fail(err)
		}
		v, report, err := c.Plugins.FileDelete(ctx, argString(args, "id"), argString(args, "name"), actor)
		if err != nil && report == nil {
			return fail(err)
		}
		reload()
		return ok(map[string]any{"plugin": summary(v), "build": report})

	case "plugin_file_rename":
		if err := perm(store.PermPluginWrite); err != nil {
			return fail(err)
		}
		v, report, err := c.Plugins.FileRename(ctx, argString(args, "id"), argString(args, "from"), argString(args, "to"), actor)
		if err != nil && report == nil {
			return fail(err)
		}
		reload()
		return ok(map[string]any{"plugin": summary(v), "build": report})

	case "plugin_build":
		if err := perm(store.PermPluginWrite); err != nil {
			return fail(err)
		}
		report, err := c.Plugins.Build(ctx, argString(args, "id"), actor)
		if err != nil && report == nil {
			return fail(err)
		}
		reload()
		return ok(report)

	case "plugin_validate":
		if err := perm(store.PermPluginRead); err != nil {
			return fail(err)
		}
		m, okFiles := args["files"].(map[string]any)
		if !okFiles || len(m) == 0 {
			return fail(errors.New("files 必须是非空对象：文件名 → 源码"))
		}
		files := anyToStringMap(m)
		var mf plugin.Manifest
		if id := argString(args, "id"); id != "" {
			v, err := c.Plugins.Get(id)
			if err != nil {
				return fail(err)
			}
			mf = v.Manifest
		} else {
			mf = plugin.Manifest{Schema: plugin.ManifestSchema, ID: "validate", Name: "validate", Version: "0.0.0"}
		}
		if raw, ok := args["manifest"]; ok && raw != nil {
			b, _ := json.Marshal(raw)
			if err := json.Unmarshal(b, &mf); err != nil {
				return fail(fmt.Errorf("manifest 不合法: %w", err))
			}
		}
		report, names, err := c.Plugins.ValidateSource(ctx, files, mf)
		if err != nil {
			return fail(err)
		}
		return ok(map[string]any{"build": report, "files": names})

	case "plugin_manifest_update":
		if err := perm(store.PermPluginWrite); err != nil {
			return fail(err)
		}
		id := argString(args, "id")
		raw, okManifest := args["manifest"]
		if !okManifest {
			return fail(errors.New("缺少 manifest"))
		}
		b, _ := json.Marshal(raw)
		var mf plugin.Manifest
		if err := json.Unmarshal(b, &mf); err != nil {
			return fail(fmt.Errorf("manifest 不合法: %w", err))
		}
		v, err := c.Plugins.UpdateManifest(id, mf, actor)
		if err != nil {
			return fail(err)
		}
		reload()
		return ok(summary(v))

	case "plugin_export":
		if err := perm(store.PermPluginRead); err != nil {
			return fail(err)
		}
		id := argString(args, "id")
		files, mf, err := c.Plugins.Export(id)
		if err != nil {
			return fail(err)
		}
		return ok(map[string]any{
			"id": id, "files": files, "manifest": mf,
			"note": "把 files 逐项用 plugin_file_write 写入另一实例即可复刻该插件",
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
		keys := []map[string]any{}
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
		"built": v.Built, "builtAt": v.BuiltAt, "buildSha": v.BuildSHA,
		"buildError": v.BuildError, "validityHours": v.ValidityHours,
	}
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
