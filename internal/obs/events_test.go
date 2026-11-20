package obs

import (
	"fmt"
	"testing"
	"time"
)

func TestEventLogFilterAndCounts(t *testing.T) {
	log := mustEventLog(t)
	defer log.Close()

	now := time.Now().UTC()
	log.Append(Event{Time: now, Level: LevelError, Category: CatUpload, Message: "文件超过限制", Route: "/api/upload"})
	log.Append(Event{Time: now.Add(-time.Minute), Level: LevelWarn, Category: CatAuth, Message: "登录失败", Route: "/api/auth/login"})
	log.Append(Event{Time: now.Add(-2 * time.Hour), Level: LevelError, Category: CatS3, Message: "SignatureDoesNotMatch", Route: "/s3/{bucket}/{key...}"})

	all, total := log.List(EventFilter{Limit: 10})
	if total != 3 || len(all) != 3 {
		t.Fatalf("全量 = %d/%d, want 3", len(all), total)
	}
	if all[0].Message != "文件超过限制" {
		t.Fatalf("应返回最新在前，得到 %q", all[0].Message)
	}

	errs, n := log.List(EventFilter{Level: LevelError, Limit: 10})
	if n != 2 || len(errs) != 2 {
		t.Fatalf("Error 级别 = %d, want 2", n)
	}

	s3, n := log.List(EventFilter{Category: CatS3, Limit: 10})
	if n != 1 || len(s3) != 1 {
		t.Fatalf("S3 分类 = %d, want 1", n)
	}

	hit, n := log.List(EventFilter{Query: "signature", Limit: 10})
	if n != 1 || len(hit) != 1 {
		t.Fatalf("关键字检索 = %d, want 1", n)
	}

	recent, n := log.List(EventFilter{Since: now.Add(-time.Minute), Limit: 10})
	if n != 2 || len(recent) != 2 {
		t.Fatalf("时间过滤 = %d, want 2", n)
	}

	page, n := log.List(EventFilter{Offset: 2, Limit: 1})
	if n != 3 || len(page) != 1 {
		t.Fatalf("分页 = %d/%d, want total 3 len 1", len(page), n)
	}

	levels, cats := log.Counts(now.Add(-time.Hour))
	if levels[LevelError] != 1 || levels[LevelWarn] != 1 {
		t.Fatalf("级别计数 = %v", levels)
	}
	if cats[CatUpload] != 1 || cats[CatAuth] != 1 || cats[CatS3] != 0 {
		t.Fatalf("分类计数 = %v", cats)
	}
}

func TestEventLogRingOverflow(t *testing.T) {
	log := mustEventLog(t)
	defer log.Close()

	for i := 0; i < eventCapacity+50; i++ {
		log.Append(Event{Level: LevelWarn, Category: CatRequest, Message: fmt.Sprintf("e%d", i)})
	}
	items, total := log.List(EventFilter{Limit: 1})
	if total != eventCapacity {
		t.Fatalf("环形容量 = %d, want %d", total, eventCapacity)
	}
	if items[0].Message != fmt.Sprintf("e%d", eventCapacity+49) {
		t.Fatalf("最新事件 = %q", items[0].Message)
	}
}

func TestEventLogReloadTail(t *testing.T) {
	dir := t.TempDir()
	log, err := OpenEventLog(dir)
	if err != nil {
		t.Fatal(err)
	}
	log.Append(Event{Level: LevelError, Category: CatSystem, Message: "重启前的事件"})
	if err := log.Close(); err != nil {
		t.Fatal(err)
	}

	again, err := OpenEventLog(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer again.Close()
	items, total := again.List(EventFilter{Limit: 5})
	if total != 1 || len(items) != 1 || items[0].Message != "重启前的事件" {
		t.Fatalf("重启后未恢复事件: total=%d items=%+v", total, items)
	}
}

func TestCategoryLabelsCoverAll(t *testing.T) {
	for _, c := range Categories() {
		if _, ok := CategoryLabels[c]; !ok {
			t.Fatalf("分类 %s 缺少中文名", c)
		}
	}
	if len(Categories()) != 7 {
		t.Fatalf("分类数量 = %d, want 7", len(Categories()))
	}
}
