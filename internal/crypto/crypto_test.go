// 密钥加解密回归测试: 供应商 Key 与路由密钥都依赖 Seal/Open,
// 任何格式变化(前缀、base64、空串语义)都会导致已有 config.json 无法解密。
package crypto

import (
	"encoding/base64"
	"strings"
	"testing"
)

func TestSealOpenRoundTrip(t *testing.T) {
	c := New(t.TempDir())
	const plain = "sk-test-0123456789"
	sealed, err := Seal(c, plain)
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	if !strings.HasPrefix(sealed, "enc1:") {
		t.Fatalf("密文缺少 enc1: 前缀: %q", sealed)
	}
	if strings.Contains(sealed, plain) {
		t.Fatal("密文中出现了明文")
	}
	got, err := Open(c, sealed)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if got != plain {
		t.Fatalf("往返后不一致: %q != %q", got, plain)
	}
}

func TestSealEmpty(t *testing.T) {
	c := New(t.TempDir())
	s, err := Seal(c, "")
	if err != nil || s != "" {
		t.Fatalf("空串应原样返回空, got %q err=%v", s, err)
	}
	if v, err := Open(c, ""); err != nil || v != "" {
		t.Fatalf("空密文应返回空, got %q err=%v", v, err)
	}
}

func TestOpenRejectsBadInput(t *testing.T) {
	c := New(t.TempDir())
	if _, err := Open(c, "plain-text-no-prefix"); err == nil {
		t.Fatal("无前缀的字符串不应被当作密文接受")
	}
	if _, err := Open(c, "enc1:!!!not-base64!!!"); err == nil {
		t.Fatal("非法 base64 应报错")
	}
	if _, err := Open(c, "enc1:"); err == nil {
		t.Fatal("空负载应报错")
	}
}

// TestOpenRejectsTamperedCiphertext 密文被篡改必须解密失败(AEAD 完整性)。
// 注意必须在解码后的字节上翻转: base64 末尾字符的低位可能是填充位,
// 改动它得到的明文完全相同, 那样测不到任何东西。
func TestOpenRejectsTamperedCiphertext(t *testing.T) {
	c := New(t.TempDir())
	sealed, err := Seal(c, "sk-tamper")
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(sealed, "enc1:"))
	if err != nil {
		t.Fatalf("解码密文: %v", err)
	}
	raw[len(raw)/2] ^= 0xFF
	tampered := "enc1:" + base64.StdEncoding.EncodeToString(raw)
	if _, err := Open(c, tampered); err == nil {
		t.Fatal("被篡改的密文应解密失败")
	}
}

// TestDifferentCryptorInstancesInteroperate 同一数据目录重建 Cryptor(等价重启)后仍能解密。
func TestDifferentCryptorInstancesInteroperate(t *testing.T) {
	dir := t.TempDir()
	sealed, err := Seal(New(dir), "sk-restart")
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	got, err := Open(New(dir), sealed)
	if err != nil {
		t.Fatalf("重启后 Open: %v", err)
	}
	if got != "sk-restart" {
		t.Fatalf("重启后解密结果不一致: %q", got)
	}
}
