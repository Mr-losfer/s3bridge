package runtime

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/Mr-losfer/s3bridge/internal/plugin/sdk"
)

// extractSDK 把内嵌的 SDK 源码写到数据目录，供插件模块 replace。
// 版本号变化时才重写，避免每次启动都改动文件（会触发不必要的重新编译）。
func extractSDK(dir string) error {
	marker := filepath.Join(dir, ".sdk-version")
	if raw, err := os.ReadFile(marker); err == nil && strings.TrimSpace(string(raw)) == sdk.Version {
		return nil
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("创建 SDK 目录: %w", err)
	}
	// 版本变化时先清掉旧 .go，避免上一版删除或改名的文件继续参与编译。
	if olds, err := os.ReadDir(dir); err == nil {
		for _, e := range olds {
			if !e.IsDir() && strings.HasSuffix(e.Name(), ".go") {
				_ = os.Remove(filepath.Join(dir, e.Name()))
			}
		}
	}
	entries, err := fs.ReadDir(sdk.Source, ".")
	if err != nil {
		return fmt.Errorf("读取内嵌 SDK: %w", err)
	}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || name == "embed.go" || strings.HasSuffix(name, "_test.go") {
			continue
		}
		if !strings.HasSuffix(name, ".go") {
			continue
		}
		raw, err := sdk.Source.ReadFile(name)
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(dir, name), raw, 0o600); err != nil {
			return fmt.Errorf("写出 SDK 文件 %s: %w", name, err)
		}
	}
	mod := "module github.com/Mr-losfer/s3bridge/sdk\n\ngo 1.21\n"
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte(mod), 0o600); err != nil {
		return err
	}
	return os.WriteFile(marker, []byte(sdk.Version+"\n"), 0o600)
}
