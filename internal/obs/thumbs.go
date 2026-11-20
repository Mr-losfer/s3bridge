package obs

import (
	"errors"
	"fmt"
	"image"
	"image/draw"
	_ "image/gif"
	"image/jpeg"
	_ "image/png"
	"math"
	"os"
	"path/filepath"
	"sync"
)

// ErrThumbUnsupported 表示该格式无法用标准库解码（如 WebP/AVIF）。
// 调用方应回退到原图，而不是返回错误给用户。
var ErrThumbUnsupported = errors.New("该图片格式不支持生成缩略图")

// ThumbCache 负责生成并缓存缩略图。
//
// 生成走「按需 + 磁盘缓存」：首次请求某张图时解码缩放并写盘，之后直接命中文件。
// 并发用信号量限制，避免批量加载相册时把 CPU 与内存打满。
type ThumbCache struct {
	dir          string
	sem          chan struct{}
	inflight     sync.Map // sha+width -> chan struct{}
	maxSrcPixels int64
}

// NewThumbCache 创建缩略图缓存；dir 通常是 {DataDir}/thumbs。
func NewThumbCache(dir string, concurrency int, maxSrcPixels int64) (*ThumbCache, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("创建缩略图目录: %w", err)
	}
	if concurrency <= 0 {
		concurrency = 2
	}
	if maxSrcPixels <= 0 {
		maxSrcPixels = 40 << 20 // 4000 万像素，超出直接回退原图
	}
	return &ThumbCache{dir: dir, sem: make(chan struct{}, concurrency), maxSrcPixels: maxSrcPixels}, nil
}

// Path 返回缩略图的缓存路径。
func (t *ThumbCache) Path(sha string, width int) string {
	if len(sha) < 2 {
		return ""
	}
	return filepath.Join(t.dir, fmt.Sprint(width), sha[:2], sha+".jpg")
}

// Get 返回缩略图文件路径；不存在时生成。
// 返回 ErrThumbUnsupported 时调用方应回退到原图。
func (t *ThumbCache) Get(sha, srcPath string, width int) (string, error) {
	if sha == "" {
		return "", ErrThumbUnsupported
	}
	dst := t.Path(sha, width)
	if info, err := os.Stat(dst); err == nil && info.Size() > 0 {
		return dst, nil
	}
	key := fmt.Sprintf("%s@%d", sha, width)
	// 简化 singleflight：同一张图并发请求只生成一次。
	ch := make(chan struct{})
	if _, loaded := t.inflight.LoadOrStore(key, ch); loaded {
		<-ch
		if info, err := os.Stat(dst); err == nil && info.Size() > 0 {
			return dst, nil
		}
		return "", ErrThumbUnsupported
	}
	defer func() {
		t.inflight.Delete(key)
		close(ch)
	}()

	t.sem <- struct{}{}
	defer func() { <-t.sem }()

	if err := generateThumb(srcPath, dst, width, t.maxSrcPixels); err != nil {
		return "", err
	}
	return dst, nil
}

func generateThumb(srcPath, dst string, width int, maxPixels int64) error {
	f, err := os.Open(srcPath)
	if err != nil {
		return ErrThumbUnsupported
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || info.Size() == 0 {
		return ErrThumbUnsupported
	}
	cfg, _, err := image.DecodeConfig(f)
	if err != nil {
		return ErrThumbUnsupported // WebP/AVIF/CMYK 等标准库不支持的格式
	}
	if int64(cfg.Width)*int64(cfg.Height) > maxPixels {
		return ErrThumbUnsupported
	}
	if cfg.Width <= width {
		return ErrThumbUnsupported // 原图本来就小，直接回退原图更清晰
	}
	if _, err := f.Seek(0, 0); err != nil {
		return ErrThumbUnsupported
	}
	src, _, err := image.Decode(f)
	if err != nil {
		return ErrThumbUnsupported
	}
	out := downscale(src, width)
	tmp := dst + ".tmp"
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return err
	}
	of, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if err := jpeg.Encode(of, out, &jpeg.Options{Quality: 82}); err != nil {
		_ = of.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := of.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, dst)
}

// downscale 用双线性插值缩放图像，只缩小不放大。
func downscale(src image.Image, width int) *image.RGBA {
	b := src.Bounds()
	sw, sh := b.Dx(), b.Dy()
	if sw <= width || width <= 0 {
		width = sw
	}
	height := int(math.Round(float64(sh) * float64(width) / float64(sw)))
	if height < 1 {
		height = 1
	}

	// 先转成 RGBA：后续按像素切片访问，避免 At() 的接口开销。
	srcRGBA := image.NewRGBA(image.Rect(0, 0, sw, sh))
	draw.Draw(srcRGBA, srcRGBA.Bounds(), src, b.Min, draw.Src)

	dst := image.NewRGBA(image.Rect(0, 0, width, height))
	xRatio := float64(sw) / float64(width)
	yRatio := float64(sh) / float64(height)
	for y := 0; y < height; y++ {
		sy := (float64(y)+0.5)*yRatio - 0.5
		y0 := int(math.Floor(sy))
		fy := sy - float64(y0)
		if y0 < 0 {
			y0, fy = 0, 0
		}
		y1 := y0 + 1
		if y1 >= sh {
			y1 = sh - 1
		}
		for x := 0; x < width; x++ {
			sx := (float64(x)+0.5)*xRatio - 0.5
			x0 := int(math.Floor(sx))
			fx := sx - float64(x0)
			if x0 < 0 {
				x0, fx = 0, 0
			}
			x1 := x0 + 1
			if x1 >= sw {
				x1 = sw - 1
			}
			i00 := srcRGBA.PixOffset(x0, y0)
			i10 := srcRGBA.PixOffset(x1, y0)
			i01 := srcRGBA.PixOffset(x0, y1)
			i11 := srcRGBA.PixOffset(x1, y1)
			p := srcRGBA.Pix
			for c := 0; c < 3; c++ {
				top := float64(p[i00+c])*(1-fx) + float64(p[i10+c])*fx
				bot := float64(p[i01+c])*(1-fx) + float64(p[i11+c])*fx
				v := top*(1-fy) + bot*fy
				dst.Pix[dst.PixOffset(x, y)+c] = uint8(v + 0.5)
			}
			dst.Pix[dst.PixOffset(x, y)+3] = 255
		}
	}
	return dst
}
