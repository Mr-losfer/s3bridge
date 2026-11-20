package obs

import (
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"os"
	"path/filepath"
	"testing"
)

func TestThumbCacheGenerateAndHit(t *testing.T) {
	srcDir := t.TempDir()
	// 构造一张 400×300 的渐变图，缩放到 100 宽后高度应为 75。
	img := image.NewRGBA(image.Rect(0, 0, 400, 300))
	for y := 0; y < 300; y++ {
		for x := 0; x < 400; x++ {
			img.Set(x, y, color.RGBA{R: uint8(x % 256), G: uint8(y % 256), B: 120, A: 255})
		}
	}
	src := filepath.Join(srcDir, "pic.png")
	f, err := os.Create(src)
	if err != nil {
		t.Fatal(err)
	}
	if err := png.Encode(f, img); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()

	cache, err := NewThumbCache(t.TempDir(), 2, 40<<20)
	if err != nil {
		t.Fatal(err)
	}
	out, err := cache.Get("abcdef0123456789", src, 100)
	if err != nil {
		t.Fatalf("生成缩略图失败: %v", err)
	}
	of, err := os.Open(out)
	if err != nil {
		t.Fatal(err)
	}
	defer of.Close()
	cfg, err := jpeg.DecodeConfig(of)
	if err != nil {
		t.Fatalf("输出不是合法 JPEG: %v", err)
	}
	if cfg.Width != 100 || cfg.Height != 75 {
		t.Fatalf("缩略图尺寸 = %dx%d, want 100x75", cfg.Width, cfg.Height)
	}

	// 第二次应命中磁盘缓存：先删源文件，仍能取到。
	if err := os.Remove(src); err != nil {
		t.Fatal(err)
	}
	again, err := cache.Get("abcdef0123456789", src, 100)
	if err != nil || again != out {
		t.Fatalf("缓存未命中: %q %v", again, err)
	}
}

func TestThumbCacheUnsupported(t *testing.T) {
	dir := t.TempDir()
	bogus := filepath.Join(dir, "x.webp")
	if err := os.WriteFile(bogus, []byte("RIFFxxxxWEBPVP8 "), 0o600); err != nil {
		t.Fatal(err)
	}
	cache, err := NewThumbCache(t.TempDir(), 1, 40<<20)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cache.Get("deadbeefdeadbeef", bogus, 200); err != ErrThumbUnsupported {
		t.Fatalf("不支持格式应返回 ErrThumbUnsupported, 得到 %v", err)
	}
}

func TestThumbCacheSkipsSmallImage(t *testing.T) {
	dir := t.TempDir()
	img := image.NewRGBA(image.Rect(0, 0, 80, 80))
	src := filepath.Join(dir, "small.png")
	f, _ := os.Create(src)
	_ = png.Encode(f, img)
	_ = f.Close()

	cache, _ := NewThumbCache(t.TempDir(), 1, 40<<20)
	if _, err := cache.Get("0011223344556677", src, 480); err != ErrThumbUnsupported {
		t.Fatalf("小图应回退原图, 得到 %v", err)
	}
}
