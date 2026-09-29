//go:build windows

package crypto

import (
	"errors"
	"fmt"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

const cryptProtectUIForbidden = 0x1

// dpapi 基于 Windows 数据保护 API, 密文与「当前用户 + 本机」绑定。
type dpapi struct{}

func newDPAPI() (Cryptor, error) {
	// 探测一次 DPAPI 可用性
	probe := []byte("probe")
	c := dpapi{}
	out, err := c.Encrypt(probe)
	if err != nil {
		return nil, err
	}
	back, err := c.Decrypt(out)
	if err != nil || string(back) != string(probe) {
		return nil, errors.New("dpapi roundtrip failed")
	}
	return c, nil
}

func (dpapi) Encrypt(plain []byte) ([]byte, error) {
	if len(plain) == 0 {
		return nil, errors.New("empty plaintext")
	}
	in := windows.DataBlob{Size: uint32(len(plain)), Data: &plain[0]}
	entropy := windows.DataBlob{Size: uint32(len(appEntropy)), Data: &appEntropy[0]}
	var out windows.DataBlob
	err := windows.CryptProtectData(&in, nil, &entropy, 0, nil, cryptProtectUIForbidden, &out)
	if err != nil {
		return nil, fmt.Errorf("CryptProtectData: %w", err)
	}
	defer windows.LocalFree(windows.Handle(unsafe.Pointer(out.Data)))
	buf := make([]byte, out.Size)
	copy(buf, unsafe.Slice(out.Data, out.Size))
	return buf, nil
}

func (dpapi) Decrypt(sealed []byte) ([]byte, error) {
	if len(sealed) == 0 {
		return nil, errors.New("empty blob")
	}
	in := windows.DataBlob{Size: uint32(len(sealed)), Data: &sealed[0]}
	entropy := windows.DataBlob{Size: uint32(len(appEntropy)), Data: &appEntropy[0]}
	var out windows.DataBlob
	err := windows.CryptUnprotectData(&in, nil, &entropy, 0, nil, cryptProtectUIForbidden, &out)
	if err != nil {
		return nil, fmt.Errorf("CryptUnprotectData: %w", err)
	}
	defer windows.LocalFree(windows.Handle(unsafe.Pointer(out.Data)))
	buf := make([]byte, out.Size)
	copy(buf, unsafe.Slice(out.Data, out.Size))
	return buf, nil
}

// readOSMachineID 读取注册表 MachineGuid, 仅作为 AES 回退方案的指纹来源。
func readOSMachineID() string {
	k, err := registry.OpenKey(registry.LOCAL_MACHINE, `SOFTWARE\Microsoft\Cryptography`, registry.QUERY_VALUE)
	if err != nil {
		return ""
	}
	defer k.Close()
	v, _, err := k.GetStringValue("MachineGuid")
	if err != nil {
		return ""
	}
	return "guid:" + v
}
