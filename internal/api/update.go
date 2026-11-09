package api

import (
	"net/http"

	"github.com/Mr-losfer/s3bridge/internal/store"
)

// 版本更新接口。
// status/check/dismiss 对所有登录用户开放（首页需要展示），
// apply 会替换可执行文件并重启服务，因此限定 system:manage。

func (s *Server) updateStatus(c *Ctx) error {
	c.JSON(http.StatusOK, s.core.Updater.Status())
	return nil
}

func (s *Server) updateCheck(c *Ctx) error {
	var req struct {
		Force bool `json:"force"`
	}
	_ = c.Bind(&req) // 空请求体是允许的
	force := req.Force || c.Query("force", "") == "1" || c.Query("force", "") == "true"
	st, err := s.core.Updater.Check(c.R.Context(), force)
	if err != nil {
		return errf(http.StatusBadGateway, "update_check_failed", "检测更新失败：%v", err)
	}
	c.JSON(http.StatusOK, st)
	return nil
}

func (s *Server) updateApply(c *Ctx) error {
	// 路由层已校验权限，这里再显式确认一次，避免后续改动遗漏。
	if err := c.Require(store.PermSystemManage); err != nil {
		return err
	}
	a, err := s.core.Updater.Apply(c.Actor())
	if err != nil {
		return errf(http.StatusConflict, "update_conflict", "%v", err)
	}
	c.JSON(http.StatusAccepted, a)
	return nil
}

func (s *Server) updateDismiss(c *Ctx) error {
	var req struct {
		Tag string `json:"tag"`
	}
	if err := c.Bind(&req); err != nil {
		return err
	}
	if req.Tag == "" {
		return errf(http.StatusBadRequest, "bad_request", "tag 不能为空")
	}
	s.core.Updater.Dismiss(req.Tag)
	c.JSON(http.StatusOK, s.core.Updater.Status())
	return nil
}
