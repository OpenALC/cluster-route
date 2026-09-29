//go:build !windows

package crypto

import "errors"

// newDPAPI 非 Windows 平台无 DPAPI, 返回错误使 New 回退到机器指纹 AES。
func newDPAPI() (Cryptor, error) {
	return nil, errors.New("dpapi: 仅 Windows 可用")
}
