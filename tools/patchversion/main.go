//go:build windows

// patchversion 将 Windows PE 文件(exe/dll)的 VERSIONINFO 资源替换为
// RC/goversioninfo 兼容风格(语言 0409, 代码页 04B0)的版本块。
//
// 背景: wails build 内部用 go-winres 生成的 RT_VERSION 资源块,
// Explorer 和 version.dll 都能正常读取, 但 .NET Framework 的
// FileVersionInfo(即 PowerShell 的 (Get-Item).VersionInfo)无法解析,
// 导致 CompanyName 等属性显示为空。本工具用 Win32 UpdateResource API
// 直接替换该资源, 产出与 RC.EXE / NSIS / goversioninfo 一致的布局,
// 三类读取方(Explorer / version.dll / .NET)均正常。
//
// 用法:
//
//	go run ./tools/patchversion -exe "build/bin/Cluster Route.exe"
//	go run ./tools/patchversion -exe cluster-router-server.exe -original cluster-router-server.exe
//
// 版本信息默认读取 build/windows/info.json(wails 的信息文件, 唯一来源),
// 可用命令行参数覆盖任意字段。
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"unsafe"
)

const (
	rtVersion         = 16 // RT_VERSION
	langIDEnglishUS   = 0x0409
	charsetUnicode    = 0x04B0
	imageResourceName = 1 // 版本资源名固定为 1
)

var (
	kernel32           = syscall.NewLazyDLL("kernel32.dll")
	procBeginUpdateRes = kernel32.NewProc("BeginUpdateResourceW")
	procUpdateRes      = kernel32.NewProc("UpdateResourceW")
	procEndUpdateRes   = kernel32.NewProc("EndUpdateResourceW")
	procLoadLibraryEx  = kernel32.NewProc("LoadLibraryExW")
	procFreeLibrary    = kernel32.NewProc("FreeLibrary")
	procEnumResLangs   = kernel32.NewProc("EnumResourceLanguagesW")
)

type wailsInfoJSON struct {
	Fixed struct {
		FileVersion    string `json:"file_version"`
		ProductVersion string `json:"product_version"`
	} `json:"fixed"`
	Info map[string]struct {
		ProductVersion   string `json:"ProductVersion"`
		CompanyName      string `json:"CompanyName"`
		FileDescription  string `json:"FileDescription"`
		LegalCopyright   string `json:"LegalCopyright"`
		ProductName      string `json:"ProductName"`
		OriginalFilename string `json:"OriginalFilename"`
		Comments         string `json:"Comments"`
	} `json:"info"`
}

type versionInfo struct {
	FileVersion      string // "0.3.0.0"
	ProductVersion   string // "0.3.0"
	CompanyName      string
	FileDescription  string
	LegalCopyright   string
	ProductName      string
	OriginalFilename string
	Comments         string
	InternalName     string
}

// loadFromInfoJSON 从 wails 的 build/windows/info.json 读取默认值。
func loadFromInfoJSON(path string) (versionInfo, error) {
	var vi versionInfo
	data, err := os.ReadFile(path)
	if err != nil {
		return vi, err
	}
	var wi wailsInfoJSON
	if err := json.Unmarshal(data, &wi); err != nil {
		return vi, err
	}
	vi.FileVersion = wi.Fixed.FileVersion
	vi.ProductVersion = wi.Fixed.ProductVersion
	// info 中语言子键取第一个(通常为 "0000", 其内容与语言无关)
	for _, v := range wi.Info {
		vi.ProductVersion = v.ProductVersion
		vi.CompanyName = v.CompanyName
		vi.FileDescription = v.FileDescription
		vi.LegalCopyright = v.LegalCopyright
		vi.ProductName = v.ProductName
		vi.OriginalFilename = v.OriginalFilename
		vi.Comments = v.Comments
		break
	}
	return vi, nil
}

func u32(v uint32) []byte {
	return []byte{byte(v), byte(v >> 8), byte(v >> 16), byte(v >> 24)}
}

func u16(v uint16) []byte {
	return []byte{byte(v), byte(v >> 8)}
}

// utf16z 编码 UTF-16LE 字符串(含终止符), 保证 4 字节对齐的填充由调用方处理。
func utf16z(s string) []byte {
	u := utf16Encode(s)
	b := make([]byte, 0, len(u)*2+2)
	for _, c := range u {
		b = append(b, byte(c), byte(c>>8))
	}
	b = append(b, 0, 0)
	return b
}

func utf16Encode(s string) []uint16 {
	u := make([]uint16, 0, len(s)+1)
	for _, r := range s {
		u = append(u, uint16(r))
	}
	return u
}

// pad4 追加零字节使长度对齐到 4 字节。
func pad4(b []byte) []byte {
	for len(b)%4 != 0 {
		b = append(b, 0)
	}
	return b
}

// block 写一个版本信息节点头 + szKey, 并对齐; value 为原始字节(可为 nil)。
// wValueLength 单位: wType==1(文本)时为 WORD 数, 否则为字节数。
func block(key string, wType uint16, value []byte, children ...[]byte) []byte {
	valueLen := uint16(0)
	if wType == 1 {
		// 文本值: 字节数/2 (含终止符)
		valueLen = uint16(len(value) / 2)
	} else if value != nil {
		valueLen = uint16(len(value))
	}
	b := u16(uint16(0)) // wLength 占位, 最后回填
	b = append(b, u16(valueLen)...)
	b = append(b, u16(wType)...)
	b = append(b, utf16z(key)...)
	b = pad4(b)
	b = append(b, value...)
	for _, c := range children {
		b = pad4(b)
		b = append(b, c...)
	}
	// 回填 wLength(含自身)
	// 注意: 头是 6 字节, 上面先写了 2 字节占位, 总长即 len(b)
	return setU16(b, 0, uint16(len(b)))
}

func setU16(b []byte, off int, v uint16) []byte {
	b[off] = byte(v)
	b[off+1] = byte(v >> 8)
	return b
}

// stringEntry 写单个 StringFileInfo 下的字符串项。
func stringEntry(key, value string) []byte {
	v := utf16z(value)
	b := u16(0) // wLength 占位
	b = append(b, u16(uint16(len(v)/2))...)
	b = append(b, u16(1)...)
	b = append(b, utf16z(key)...)
	b = pad4(b)
	b = append(b, v...)
	return setU16(b, 0, uint16(len(b)))
}

// fixedFileInfo 生成 VS_FIXEDFILEINFO (52 字节)。
func fixedFileInfo(ver [4]uint16) []byte {
	b := u32(0xFEEF04BD) // dwSignature
	b = append(b, u32(0x00010000)...)                   // dwStrucVersion
	b = append(b, u32(uint32(ver[0])<<16|uint32(ver[1]))...) // dwFileVersionMS
	b = append(b, u32(uint32(ver[2])<<16|uint32(ver[3]))...) // dwFileVersionLS
	b = append(b, u32(uint32(ver[0])<<16|uint32(ver[1]))...) // dwProductVersionMS
	b = append(b, u32(uint32(ver[2])<<16|uint32(ver[3]))...) // dwProductVersionLS
	b = append(b, u32(0x0000003F)...)                   // dwFileFlagsMask
	b = append(b, u32(0x00000000)...)                   // dwFileFlags
	b = append(b, u32(0x00040004)...)                   // dwFileOS = VOS_NT_WINDOWS32
	b = append(b, u32(0x00000001)...)                   // dwFileType = VFT_APP
	b = append(b, u32(0x00000000)...)                   // dwFileSubtype
	b = append(b, u32(0x00000000)...)                   // dwFileDateMS
	b = append(b, u32(0x00000000)...)                   // dwFileDateLS
	return b
}

func parseVer4(s string) [4]uint16 {
	var v [4]uint16
	parts := strings.Split(s, ".")
	for i := 0; i < len(parts) && i < 4; i++ {
		n := 0
		fmt.Sscanf(parts[i], "%d", &n)
		v[i] = uint16(n)
	}
	return v
}

// buildVersionBlob 生成完整的 VS_VERSIONINFO 资源数据(布局与 RC.EXE/NSIS/goversioninfo 一致)。
func buildVersionBlob(vi versionInfo) []byte {
	ver4 := parseVer4(vi.FileVersion)

	// StringTable: 键为 "<langID><charsetID>" 8 位十六进制
	table := block(fmt.Sprintf("%04X%04X", langIDEnglishUS, charsetUnicode), 1, nil,
		stringEntry("Comments", vi.Comments),
		stringEntry("CompanyName", vi.CompanyName),
		stringEntry("FileDescription", vi.FileDescription),
		stringEntry("FileVersion", vi.ProductVersion),
		stringEntry("InternalName", vi.InternalName),
		stringEntry("LegalCopyright", vi.LegalCopyright),
		stringEntry("OriginalFilename", vi.OriginalFilename),
		stringEntry("ProductName", vi.ProductName),
		stringEntry("ProductVersion", vi.ProductVersion),
	)
	sfi := block("StringFileInfo", 1, nil, table)

	// VarFileInfo\Translation: [langID][charsetID] 两个 16 位值
	translation := append(u16(langIDEnglishUS), u16(charsetUnicode)...)
	vfi := block("VarFileInfo", 1, nil, block("Translation", 0, translation))

	return block("VS_VERSION_INFO", 0, fixedFileInfo(ver4), sfi, vfi)
}

// enumVersionLangs 枚举目标文件中 RT_VERSION/名字 1 现存的所有语言 ID。
// 对不存在的资源执行删除会污染更新会话 (后续 UpdateResource 返回
// ERROR_INTERNAL_ERROR), 因此必须先枚举再删除。
func enumVersionLangs(path string) []uint16 {
	p16, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return nil
	}
	hMod, _, _ := procLoadLibraryEx.Call(uintptr(unsafe.Pointer(p16)), 0, 0x2) // LOAD_LIBRARY_AS_DATAFILE
	if hMod == 0 {
		return nil
	}
	defer procFreeLibrary.Call(hMod)

	var langs []uint16
	cb := syscall.NewCallback(func(hMod, typ, name, lang, lParam uintptr) uintptr {
		langs = append(langs, uint16(lang))
		return 1 // 继续枚举
	})
	procEnumResLangs.Call(hMod, uintptr(rtVersion), uintptr(imageResourceName), cb, 0)
	return langs
}

func main() {
	exe := flag.String("exe", "", "target PE file (required)")
	infoJSON := flag.String("info", filepath.Join("build", "windows", "info.json"), "wails info.json (default source of version fields)")
	company := flag.String("company", "", "override CompanyName")
	product := flag.String("product", "", "override ProductName")
	version := flag.String("version", "", "override ProductVersion/FileVersion strings (x.y.z)")
	fileVersion := flag.String("fileversion", "", "override binary FileVersion x.y.z.w")
	desc := flag.String("description", "", "override FileDescription")
	copyright := flag.String("copyright", "", "override LegalCopyright")
	comments := flag.String("comments", "", "override Comments")
	original := flag.String("original", "", "override OriginalFilename")
	dryRun := flag.Bool("dry-run", false, "print version info without modifying the file")
	flag.Parse()

	if *exe == "" {
		fmt.Fprintln(os.Stderr, "must specify -exe")
		flag.Usage()
		os.Exit(2)
	}

	vi, err := loadFromInfoJSON(*infoJSON)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to read %s: %v\n(use command-line flags to specify values directly)\n", *infoJSON, err)
		os.Exit(1)
	}
	if *company != "" {
		vi.CompanyName = *company
	}
	if *product != "" {
		vi.ProductName = *product
	}
	if *version != "" {
		vi.ProductVersion = *version
		vi.FileVersion = *version + ".0"
	}
	if *fileVersion != "" {
		vi.FileVersion = *fileVersion
	}
	if *desc != "" {
		vi.FileDescription = *desc
	}
	if *copyright != "" {
		vi.LegalCopyright = *copyright
	}
	if *comments != "" {
		vi.Comments = *comments
	}
	if *original != "" {
		vi.OriginalFilename = *original
	}
	if vi.InternalName == "" {
		vi.InternalName = vi.ProductName
	}
	if vi.FileVersion == "" {
		vi.FileVersion = vi.ProductVersion + ".0"
	}

	blob := buildVersionBlob(vi)
	fmt.Printf("target: %s\n", *exe)
	fmt.Printf("writing VERSIONINFO (lang 0409 / cp 04B0, %d bytes):\n", len(blob))
	fmt.Printf("  CompanyName      = %s\n", vi.CompanyName)
	fmt.Printf("  ProductName      = %s\n", vi.ProductName)
	fmt.Printf("  FileVersion      = %s\n", vi.FileVersion)
	fmt.Printf("  ProductVersion   = %s\n", vi.ProductVersion)
	fmt.Printf("  FileDescription  = %s\n", vi.FileDescription)
	fmt.Printf("  LegalCopyright   = %s\n", vi.LegalCopyright)
	fmt.Printf("  OriginalFilename = %s\n", vi.OriginalFilename)
	if *dryRun {
		return
	}

	pathPtr, err := syscall.UTF16PtrFromString(*exe)
	if err != nil {
		fmt.Fprintf(os.Stderr, "invalid path: %v\n", err)
		os.Exit(1)
	}

	hUpd, _, err := procBeginUpdateRes.Call(uintptr(unsafe.Pointer(pathPtr)), 0, 0)
	if hUpd == 0 {
		fmt.Fprintf(os.Stderr, "BeginUpdateResource failed: %v (file in use?)\n", err)
		os.Exit(1)
	}
	ok := true
	fail := func(stage string, err error) {
		fmt.Fprintf(os.Stderr, "%s failed: %v\n", stage, err)
		procEndUpdateRes.Call(hUpd, 1) // 1 = discard pending changes
		ok = false
	}

	// 删除现存语言版本的资源 (wails/go-winres 写的是语言 0);
	// 之后再以语言 0409 写入, 保证文件里只有这一份可被所有读取方解析。
	for _, lang := range enumVersionLangs(*exe) {
		procUpdateRes.Call(hUpd, uintptr(rtVersion), uintptr(imageResourceName), uintptr(lang), 0, 0)
	}

	data := append([]byte(nil), blob...)
	rAdd, _, errAdd := procUpdateRes.Call(hUpd,
		uintptr(rtVersion), uintptr(imageResourceName), uintptr(langIDEnglishUS),
		uintptr(unsafe.Pointer(&data[0])), uintptr(len(data)))
	if rAdd == 0 {
		fail("UpdateResource", errAdd)
	}

	if ok {
		rEnd, _, errEnd := procEndUpdateRes.Call(hUpd, 0) // 0 = commit changes
		if rEnd == 0 {
			fmt.Fprintf(os.Stderr, "EndUpdateResource failed: %v\n", errEnd)
			os.Exit(1)
		}
		fmt.Println("done.")
	} else {
		os.Exit(1)
	}
}
