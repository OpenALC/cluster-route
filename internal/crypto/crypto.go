// Package crypto 提供本地密钥加密存储:
// Windows 使用 DPAPI(绑定当前登录用户, 换机/换用户不可解),
// 其他平台或 DPAPI 失败时回退为机器指纹派生的 AES-256-GCM。
package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"strings"
)

// appEntropy 将密文与应用绑定, 防止同机器其他程序调用 DPAPI 直接解密。
var appEntropy = []byte("cluster-router/v1/key-entropy/2026")

// sealed 前缀用于标识加密 blob 的版本与格式。
const sealedPrefix = "enc1:"

// Cryptor 是对称加解密接口。
type Cryptor interface {
	Encrypt(plaintext []byte) ([]byte, error)
	Decrypt(ciphertext []byte) ([]byte, error)
}

// New 按平台优先级构造 Cryptor。
func New(dataDir string) Cryptor {
	if c, err := newDPAPI(); err == nil {
		return c
	}
	return newMachineAES(dataDir)
}

// Seal 加密字符串并加前缀, 空串原样返回。
func Seal(c Cryptor, plain string) (string, error) {
	if plain == "" {
		return "", nil
	}
	b, err := c.Encrypt([]byte(plain))
	if err != nil {
		return "", err
	}
	return sealedPrefix + base64.StdEncoding.EncodeToString(b), nil
}

// Open 解开 Seal 产生的字符串。
func Open(c Cryptor, sealed string) (string, error) {
	if sealed == "" {
		return "", nil
	}
	if !strings.HasPrefix(sealed, sealedPrefix) {
		return "", errors.New("not a sealed blob")
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(sealed, sealedPrefix))
	if err != nil {
		return "", fmt.Errorf("bad blob: %w", err)
	}
	b, err := c.Decrypt(raw)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// machineAES 基于机器指纹派生 AES-256-GCM 密钥(非 Windows 回退方案)。
type machineAES struct {
	aead cipher.AEAD
}

func newMachineAES(dataDir string) *machineAES {
	key := sha256.Sum256(append([]byte(machineFingerprint()), appEntropy...))
	block, err := aes.NewCipher(key[:])
	if err != nil {
		panic(err) // AES 新建失败仅可能源于非法 key 长度, 此处恒为 32 字节
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		panic(err)
	}
	return &machineAES{aead: aead}
}

func (m *machineAES) Encrypt(plain []byte) ([]byte, error) {
	nonce := make([]byte, m.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	out := m.aead.Seal(nonce, nonce, plain, nil)
	return append([]byte{byte(m.aead.NonceSize())}, out...), nil
}

func (m *machineAES) Decrypt(sealed []byte) ([]byte, error) {
	if len(sealed) < 1 {
		return nil, errors.New("empty blob")
	}
	ns := int(sealed[0])
	if len(sealed) < ns+1 || ns > 255 {
		return nil, errors.New("bad blob layout")
	}
	return m.aead.Open(nil, sealed[1:ns+1], sealed[ns+1:], nil)
}

// machineFingerprint 返回跨重启稳定的机器标识。
func machineFingerprint() string {
	if s := readMachineID(); s != "" {
		return s
	}
	if h, err := os.Hostname(); err == nil {
		return "hostname:" + h
	}
	return "fallback:no-identity"
}

func readMachineID() string {
	return readOSMachineID()
}
