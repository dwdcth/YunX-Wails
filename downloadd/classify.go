// classify.go：普通下载入口分流——把用户输入（URL / magnet / ed2k / 本地种子或 metalink 文件）
// 分类成引擎可执行的动作。规则：
//   - ftp:// ftps://          → FTP 引擎（Go 内置）
//   - magnet: ed2k: bc1:      → aria2 addUri
//   - 本地 .torrent / http(s) URL 以 .torrent 结尾 → aria2 addTorrent（内容嗅探兜底）
//   - 本地/URL .metalink/.meta4 或内容含 <metalink → aria2 addMetalink（v3 先转 v4）
//   - 其余 http(s)            → aria2 addUri
package downloadd

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"strings"
	"time"
)

// Kind 分类结果类型。
type Kind string

const (
	KindFTP      Kind = "ftp"      // Go 内置 FTP 引擎
	KindFTPS     Kind = "ftps"     // Go 内置 FTP 引擎（隐式 TLS）
	KindMagnet   Kind = "magnet"   // magnet/ed2k/bc1 → aria2 addUri
	KindTorrent  Kind = "torrent"  // .torrent → aria2 addTorrent
	KindMetalink Kind = "metalink" // .metalink/.meta4 → aria2 addMetalink
	KindHTTP     Kind = "http"     // 普通 http(s) → aria2 addUri
)

// Classification 分类结果：Kind 决定引擎，URL/TorrentData/MetalinkData 携带执行所需载荷。
type Classification struct {
	Kind         Kind
	URL          string // 归一化后的下载地址（ftp/magnet/http 用）
	Name         string // 可提前得知的文件名（ftp 路径末段 / magnet dn= / 无则空）
	TorrentData  []byte // 种子文件内容（本地读出或从 URL 拉取）
	MetalinkData []byte // metalink 文件内容
	Err          string // 分类失败原因（友好文案）
}

// maxFetchSize 从 URL 拉取 .torrent/.metalink 的大小上限（种子/索引文件远小于此，防误用）。
const maxFetchSize = 64 << 20

// Classify 分流主入口。
func Classify(input string) Classification {
	s := strings.TrimSpace(input)
	if s == "" {
		return Classification{Err: "下载链接为空"}
	}
	lower := strings.ToLower(s)

	// ---- scheme 前缀类 ----
	switch {
	case strings.HasPrefix(lower, "ftp://"):
		return Classification{Kind: KindFTP, URL: s, Name: nameFromURLPath(s)}
	case strings.HasPrefix(lower, "ftps://"):
		return Classification{Kind: KindFTPS, URL: s, Name: nameFromURLPath(s)}
	case strings.HasPrefix(lower, "magnet:"):
		return Classification{Kind: KindMagnet, URL: s, Name: nameFromMagnet(s)}
	case strings.HasPrefix(lower, "ed2k:"), strings.HasPrefix(lower, "bc1:"):
		return Classification{Kind: KindMagnet, URL: s}
	case strings.HasPrefix(lower, "http://"), strings.HasPrefix(lower, "https://"):
		return classifyHTTP(s)
	}

	// ---- 本地文件（无 scheme）----
	if st, err := os.Stat(s); err == nil && !st.IsDir() {
		return classifyLocalFile(s)
	}
	if strings.HasPrefix(lower, "file://") {
		if u, err := url.Parse(s); err == nil {
			p := u.Path
			if st, err := os.Stat(p); err == nil && !st.IsDir() {
				return classifyLocalFile(p)
			}
		}
	}
	return Classification{Err: "不支持的链接类型（支持 http/https/ftp/ftps/magnet/ed2k、本地 .torrent/.metalink 文件）"}
}

// classifyLocalFile 本地文件分流：扩展名优先，内容嗅探兜底。
func classifyLocalFile(p string) Classification {
	data, err := os.ReadFile(p)
	if err != nil {
		return Classification{Err: "读取文件失败: " + err.Error()}
	}
	return classifyContent(p, data)
}

// classifyContent 按扩展名 + 内容嗅探归类（本地文件与 URL 拉取后共用）。
func classifyContent(src string, data []byte) Classification {
	ext := strings.ToLower(path.Ext(src))
	switch ext {
	case ".torrent":
		if looksLikeMetalink(data) {
			return metalinkClass(data)
		}
		if !looksLikeTorrent(data) {
			return Classification{Err: "不是有效的种子文件"}
		}
		return Classification{Kind: KindTorrent, TorrentData: data, Name: torrentName(data)}
	case ".metalink", ".meta4", ".metalink3":
		return metalinkClass(data)
	}
	// 扩展名不认识 → 内容嗅探
	if looksLikeTorrent(data) {
		return Classification{Kind: KindTorrent, TorrentData: data, Name: torrentName(data)}
	}
	if looksLikeMetalink(data) {
		return metalinkClass(data)
	}
	return Classification{Err: "无法识别的文件类型（支持 .torrent / .metalink）"}
}

func metalinkClass(data []byte) Classification {
	return Classification{Kind: KindMetalink, MetalinkData: data}
}

// classifyHTTP http(s) URL 分流：.torrent/.metalink/.meta4 后缀才拉取内容，其余直接 addUri。
func classifyHTTP(s string) Classification {
	u, err := url.Parse(s)
	if err != nil || u.Host == "" {
		return Classification{Err: "无效的 http 链接"}
	}
	ext := strings.ToLower(path.Ext(u.Path))
	if ext == ".torrent" || ext == ".metalink" || ext == ".meta4" {
		data, err := fetchBytes(s)
		if err != nil {
			return Classification{Err: "拉取文件失败: " + err.Error()}
		}
		return classifyContent(u.Path, data)
	}
	return Classification{Kind: KindHTTP, URL: s, Name: nameFromURLPath(s)}
}

// fetchBytes 拉取远端 .torrent/.metalink（15s 超时，64MB 上限）。
func fetchBytes(rawURL string) ([]byte, error) {
	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Get(rawURL)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, maxFetchSize))
}

// looksLikeTorrent bencode 嗅探：以 'd' 开头且含 "4:info" / "8:announce"。
func looksLikeTorrent(data []byte) bool {
	if len(data) == 0 || data[0] != 'd' {
		return false
	}
	return bytes.Contains(data, []byte("4:info")) || bytes.Contains(data, []byte("8:announce"))
}

// looksLikeMetalink metalink 嗅探：内容含 <metalink（v3/v4 由转换层判别）。
func looksLikeMetalink(data []byte) bool {
	return bytes.Contains(data, []byte("<metalink"))
}

// nameFromURLPath 取 URL 路径末段并反转义（%20 等）。
func nameFromURLPath(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	seg := path.Base(u.Path)
	if seg == "." || seg == "/" || seg == "" {
		return ""
	}
	if dec, err := url.PathUnescape(seg); err == nil {
		return dec
	}
	return seg
}

// nameFromMagnet 提取 magnet 的 dn= 显示名。
func nameFromMagnet(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	for _, v := range u.Query()["dn"] {
		if v != "" {
			return v
		}
	}
	// 兼容未标准化的 xt=...&dn=...（url.Parse 的 Query 已处理 ; 分隔之外的裸串）
	for _, kv := range strings.Split(u.RawQuery, "&") {
		if dec, err := url.QueryUnescape(kv); err == nil && strings.HasPrefix(strings.ToLower(dec), "dn=") {
			return dec[3:]
		}
	}
	return ""
}

// torrentName 从 bencode 种子里抠 4:name 的字符串值（top-level info 字典里；
// 只用于展示，不做完整解析——健壮性优先，抠不到返回空由轮询补）。
func torrentName(data []byte) string {
	markers := [][]byte{[]byte("4:name"), []byte("4:name.utf-8")}
	for _, m := range markers {
		idx := 0
		for {
			i := indexOf(data[idx:], m)
			if i < 0 {
				break
			}
			i += idx
			// 期望形态: 4:name<len>:<bytes>（len 1~4 位数字）
			rest := data[i+len(m):]
			ci := 0
			for ci < len(rest) && rest[ci] >= '0' && rest[ci] <= '9' && ci < 4 {
				ci++
			}
			if ci > 0 && ci < len(rest) && rest[ci] == ':' {
				n := 0
				for _, c := range rest[:ci] {
					n = n*10 + int(c-'0')
				}
				if n > 0 && n <= 4096 && ci+1+n <= len(rest) {
					name := string(rest[ci+1 : ci+1+n])
					// 排除嵌套误报：名字里不应有路径分隔符与控制字符
					if !strings.ContainsAny(name, "/\\\x00") {
						return name
					}
				}
			}
			idx = i + len(m)
		}
	}
	return ""
}

func indexOf(hay, needle []byte) int {
	return bytes.Index(hay, needle)
}
