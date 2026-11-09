// Package mcp 实现 Model Context Protocol (JSON-RPC 2.0) 服务端。
//
// 同一份 Server 既挂在主服务的 /mcp（Streamable HTTP，单条 POST），
// 也由 cmd/s3bridge-mcp 以 stdio 方式驱动，因此工具行为完全一致。
package mcp

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/Mr-losfer/s3bridge/internal/app"
	"github.com/Mr-losfer/s3bridge/internal/auth"
)

const ProtocolVersion = "2024-11-05"

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type Content struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type CallResult struct {
	Content []Content `json:"content"`
	IsError bool      `json:"isError,omitempty"`
}

type Server struct {
	core    *app.Core
	version string
}

func New(core *app.Core) *Server {
	version := "dev"
	if core != nil && core.Version != "" {
		version = core.Version
	}
	return &Server{core: core, version: version}
}

// Handle 处理一条 JSON-RPC 消息；notification 返回 nil（不写响应）。
func (s *Server) Handle(ctx context.Context, data []byte, p *auth.Principal) []byte {
	var req rpcRequest
	if err := json.Unmarshal(data, &req); err != nil {
		return marshalRPC(rpcResponse{JSONRPC: "2.0", Error: &rpcError{Code: -32700, Message: "JSON 解析失败: " + err.Error()}})
	}
	if req.JSONRPC == "" {
		req.JSONRPC = "2.0"
	}
	resp := rpcResponse{JSONRPC: "2.0", ID: req.ID}
	switch req.Method {
	case "initialize":
		resp.Result = map[string]any{
			"protocolVersion": ProtocolVersion,
			"capabilities": map[string]any{
				"tools":     map[string]any{"listChanged": false},
				"resources": map[string]any{"subscribe": false, "listChanged": false},
			},
			"serverInfo": map[string]any{"name": "s3bridge", "version": s.version},
			"instructions": "S3Bridge 插件化上传聚合网关。除站点/插件/用户/存储桶管理外，" +
				"可用 plugin_create、plugin_code_get、plugin_code_upload、plugin_code_patch、" +
				"plugin_validate、plugin_export、plugin_delete 通过声明式插件源码从零开发新插件并即时生效；" +
				"再用 plugin_debug 与 upload_test 验证，无需重启服务。",
		}
	case "notifications/initialized", "notifications/cancelled":
		return nil
	case "ping":
		resp.Result = map[string]any{}
	case "tools/list":
		resp.Result = map[string]any{"tools": s.Tools()}
	case "tools/call":
		resp.Result = s.handleToolCall(ctx, req.Params, p)
	case "resources/list":
		resp.Result = map[string]any{"resources": s.Resources()}
	case "resources/read":
		resp.Result = s.handleResourceRead(req.Params)
	default:
		resp.Error = &rpcError{Code: -32601, Message: fmt.Sprintf("未知方法: %s", req.Method)}
	}
	if len(req.ID) == 0 {
		return nil
	}
	return marshalRPC(resp)
}

func (s *Server) handleToolCall(ctx context.Context, raw json.RawMessage, p *auth.Principal) CallResult {
	var params struct {
		Name      string         `json:"name"`
		Arguments map[string]any `json:"arguments"`
	}
	if err := json.Unmarshal(raw, &params); err != nil {
		return textResult("参数解析失败: "+err.Error(), true)
	}
	if params.Arguments == nil {
		params.Arguments = map[string]any{}
	}
	text, isErr := s.CallTool(ctx, params.Name, params.Arguments, p)
	return textResult(text, isErr)
}

func (s *Server) handleResourceRead(raw json.RawMessage) CallResult {
	var params struct {
		URI string `json:"uri"`
	}
	if err := json.Unmarshal(raw, &params); err != nil {
		return textResult("参数解析失败: "+err.Error(), true)
	}
	body, ok := s.Resource(params.URI)
	if !ok {
		return textResult("资源不存在: "+params.URI, true)
	}
	return CallResult{Content: []Content{{Type: "text", Text: body}}}
}

func textResult(text string, isErr bool) CallResult {
	return CallResult{Content: []Content{{Type: "text", Text: text}}, IsError: isErr}
}

func marshalRPC(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		return []byte(`{"jsonrpc":"2.0","error":{"code":-32603,"message":"序列化失败"}}`)
	}
	return b
}
