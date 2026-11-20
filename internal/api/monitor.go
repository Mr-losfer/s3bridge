package api

import (
	"net/http"
	"time"

	"github.com/Mr-losfer/s3bridge/internal/obs"
	"github.com/Mr-losfer/s3bridge/internal/store"
)

// monitorSummary 返回监控总览：指标时序、接口排行、异常计数与运行时健康。
func (s *Server) monitorSummary(c *Ctx) error {
	if s.core.Obs == nil {
		return errf(http.StatusServiceUnavailable, "monitor_disabled", "监控采集未启用")
	}
	rg := obs.ParseRange(c.Query("range", "24h"))
	sum := s.core.Obs.Summary(rg, time.Now())
	recent, _ := s.core.Obs.Events(obs.EventFilter{
		Level: obs.LevelError, Limit: 5,
		Since: time.Now().Add(-24 * time.Hour),
	})
	c.JSON(http.StatusOK, map[string]any{
		"summary": sum, "recent": recent,
		"ranges": obs.Ranges, "categoryLabels": obs.CategoryLabels,
	})
	return nil
}

// monitorEvents 查询异常日志，支持级别、分类、关键字与时间范围过滤。
func (s *Server) monitorEvents(c *Ctx) error {
	if s.core.Obs == nil {
		return errf(http.StatusServiceUnavailable, "monitor_disabled", "监控采集未启用")
	}
	f := obs.EventFilter{
		Level:    c.Query("level", ""),
		Category: c.Query("category", ""),
		Query:    c.Query("q", ""),
		Limit:    c.IntQuery("limit", 100),
		Offset:   c.IntQuery("offset", 0),
	}
	if secs := c.IntQuery("seconds", 0); secs > 0 {
		f.Since = time.Now().Add(-time.Duration(secs) * time.Second)
	}
	items, total := s.core.Obs.Events(f)
	c.JSON(http.StatusOK, map[string]any{
		"items": items, "total": total,
		"categories": obs.CategoryLabels,
	})
	return nil
}

// monitorStorage 返回存储画像：
//   - summary 是实时内存聚合（不含磁盘扫描，毫秒级返回）
//   - deep 是最近一次深度体检结果（含孤儿、断链、重复），可能为 null
func (s *Server) monitorStorage(c *Ctx) error {
	fast := s.storageSummary()

	running, deep, errMsg, started, done := s.core.Auditor.State()
	c.JSON(http.StatusOK, map[string]any{
		"summary": fast, "deep": deep, "scanning": running, "scanError": errMsg,
		"scanStartedAt": started, "scanFinishedAt": done,
	})
	return nil
}

// storageSummary 返回存储画像的内存聚合结果，5 秒内复用。
func (s *Server) storageSummary() *obs.StorageReport {
	s.storageMu.Lock()
	defer s.storageMu.Unlock()
	if s.storageCache != nil && time.Since(s.storageAt) < 5*time.Second {
		return s.storageCache
	}
	var objects []store.Object
	var buckets []store.Bucket
	var settings store.Settings
	_ = s.core.Store.View(func(st *store.State) error {
		objects = append(objects, st.Objects...)
		buckets = append(buckets, st.Buckets...)
		settings = st.Settings
		return nil
	})
	rep := obs.BuildStorageReport(objects, buckets, obs.AuditOptions{
		ObjectDir:     "", // 空表示跳过磁盘遍历，只做内存聚合
		RetentionDays: settings.UploadRetentionDays,
		TopLimit:      20,
	})
	rep.Disk = obs.DiskStats(s.core.Cfg.ObjectDir)
	s.storageCache = rep
	s.storageAt = time.Now()
	return rep
}

// invalidateStorage 让下次读取重新聚合（体检完成或数据变化后调用）。
func (s *Server) invalidateStorage() {
	s.storageMu.Lock()
	s.storageCache = nil
	s.storageMu.Unlock()
}

// monitorStorageScan 触发一次深度体检（异步）。重复触发会返回 409。
func (s *Server) monitorStorageScan(c *Ctx) error {
	started := s.core.Auditor.Start(func() *obs.StorageReport {
		var objects []store.Object
		var buckets []store.Bucket
		var settings store.Settings
		_ = s.core.Store.View(func(st *store.State) error {
			objects = append(objects, st.Objects...)
			buckets = append(buckets, st.Buckets...)
			settings = st.Settings
			return nil
		})
		return obs.BuildStorageReport(objects, buckets, obs.AuditOptions{
			ObjectDir:     s.core.Cfg.ObjectDir,
			RetentionDays: settings.UploadRetentionDays,
			MaxScanFiles:  200000,
			TopLimit:      30,
		})
	})
	if !started {
		return errf(http.StatusConflict, "scan_running", "已有一个体检任务在执行，请稍候")
	}
	s.invalidateStorage()
	c.JSON(http.StatusAccepted, map[string]any{"ok": true, "started": true})
	return nil
}

// monitorStorageScanState 轮询深度体检进度。
func (s *Server) monitorStorageScanState(c *Ctx) error {
	running, deep, errMsg, started, done := s.core.Auditor.State()
	c.JSON(http.StatusOK, map[string]any{
		"scanning": running, "deep": deep, "scanError": errMsg,
		"scanStartedAt": started, "scanFinishedAt": done,
	})
	return nil
}
