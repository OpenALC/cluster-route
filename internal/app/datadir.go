// 数据目录解析: 便携模式优先使用 exe 同目录下的 data;
// 该位置不可写(如安装到 Program Files)时回退到用户配置目录,
// 保证安装包形态与便携形态都能正常持久化。
package app

import (
	"os"
	"path/filepath"
)

// ResolveDataDir 返回本实例应使用的数据目录。
func ResolveDataDir() string {
	base := "./data"
	if exePath, err := os.Executable(); err == nil {
		base = filepath.Join(filepath.Dir(exePath), "data")
	}
	if probeWritable(base) == nil {
		return base
	}
	if cfg, err := os.UserConfigDir(); err == nil {
		alt := filepath.Join(cfg, "Cluster Route", "data")
		if probeWritable(alt) == nil {
			return alt
		}
		_ = os.MkdirAll(alt, 0o755)
		if probeWritable(alt) == nil {
			return alt
		}
	}
	return base
}

// probeWritable 探测目录可创建且可写(写入并删除探针文件)。
func probeWritable(dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	probe := filepath.Join(dir, ".write-test")
	if err := os.WriteFile(probe, []byte("ok"), 0o600); err != nil {
		return err
	}
	_ = os.Remove(probe)
	return nil
}
