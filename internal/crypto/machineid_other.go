//go:build !windows

package crypto

import "os"

// readOSMachineID 读取 Linux/类 Unix 机器 ID, 仅作为 AES 回退方案的指纹来源。
func readOSMachineID() string {
	for _, p := range []string{"/etc/machine-id", "/var/lib/dbus/machine-id"} {
		if b, err := os.ReadFile(p); err == nil && len(b) > 0 {
			return "mid:" + string(b)
		}
	}
	return ""
}
