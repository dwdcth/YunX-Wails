// fetch.go：yunx_fetch 的 Go 侧实现——受控 HTTP(S) 拉取 + 字符集归一（gbk/gb18030/big5 → UTF-8）。
//
// 安全边界（MVP 红线）：
//   - 仅 http/https 协议（magnet/ftp/file/data 等一律拒绝——ftp 下载有专用引擎，不该由插件拉）；
//   - 10s 超时、8MB 响应上限（防插件把宿主当内存炸弹）；
//   - UA 固定桌面 Chrome（dytt 这类站对 UA 不敏感，但对非浏览器 UA 可能出验证页）。
package pluginsys

import (
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	"golang.org/x/text/encoding"
	"golang.org/x/text/encoding/simplifiedchinese"
	"golang.org/x/text/encoding/traditionalchinese"
	"golang.org/x/text/transform"
)

// desktopChromeUA 实测可直连 dytt8899（2026-11，curl 同 UA 验证通过）。
const desktopChromeUA = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36"

const (
	fetchTimeout  = 10 * time.Second
	fetchMaxBody  = 8 << 20 // 8MB
	fetchSniffLen = 1024    // meta charset 嗅探窗口（HTML 规范也是 1024）
)

// FetchResult yunx_fetch 返回给 JS 的形状：{status, headers, body}。
// headers 键统一小写（与浏览器 fetch/.GetResponseHeader 习惯一致）；body 已转 UTF-8。
type FetchResult struct {
	Status  int               `json:"status"`
	Headers map[string]string `json:"headers"`
	Body    string            `json:"body"`
}

// newFetchClient 独立 client（不复用核心/预览代理的连接池，插件拉取互不牵连）。
func newFetchClient() *http.Client {
	return &http.Client{
		Timeout: fetchTimeout,
	}
}

// httpFetch 默认拉取器。返回的 body 已按 Content-Type / meta charset 归一为 UTF-8。
func httpFetch(url string) (*FetchResult, error) {
	if !strings.HasPrefix(url, "http://") && !strings.HasPrefix(url, "https://") {
		return nil, fmt.Errorf("yunx_fetch 仅支持 http/https: %s", url)
	}
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("构造请求失败: %w", err)
	}
	req.Header.Set("User-Agent", desktopChromeUA)
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	req.Header.Set("Accept-Language", "zh-CN,zh;q=0.9")

	resp, err := newFetchClient().Do(req)
	if err != nil {
		return nil, fmt.Errorf("请求失败: %w", err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, fetchMaxBody+1))
	if err != nil {
		return nil, fmt.Errorf("读响应失败: %w", err)
	}
	if len(raw) > fetchMaxBody {
		return nil, fmt.Errorf("响应超过 %dMB 上限", fetchMaxBody>>20)
	}

	headers := make(map[string]string, len(resp.Header))
	for k, v := range resp.Header {
		if len(v) > 0 {
			headers[strings.ToLower(k)] = v[0]
		}
	}

	return &FetchResult{
		Status:  resp.StatusCode,
		Headers: headers,
		Body:    decodeToUTF8(raw, resp.Header.Get("Content-Type")),
	}, nil
}

// charsetRe 兜底嗅探 <meta charset=...> / <meta http-equiv=... charset=...>（响应头没给时用）。
var charsetRe = regexp.MustCompile(`(?i)charset\s*=\s*["']?\s*([a-z0-9_\-]+)`)

// decodeToUTF8 字符集归一：header charset 优先，缺省嗅探 HTML 前 1KB 的 meta 声明，
// 再缺省按 UTF-8（x/net/html 也只认 UTF-8，GBK 不转必然乱码——dytt 全站 gb2312 是主场景）。
func decodeToUTF8(raw []byte, contentType string) string {
	// UTF-8 BOM 剥除（否则 document.title 等首字段会带 \ufeff）
	if len(raw) >= 3 && raw[0] == 0xEF && raw[1] == 0xBB && raw[2] == 0xBF {
		raw = raw[3:]
	}
	name := charsetFromHeader(contentType)
	if name == "" {
		name = charsetFromMeta(raw)
	}
	if enc := encodingByName(name); enc != nil {
		if out, _, err := transform.Bytes(enc.NewDecoder(), raw); err == nil {
			return string(out)
		}
	}
	// 未知字符集/转换失败：原样返回（UTF-8 页面本就直达；真二进制乱码由插件自行判断）。
	return string(raw)
}

// charsetFromHeader 解析 Content-Type 里的 charset=xxx。x/net/html/charset 的
// ParseHttpContentType 不容错（畸形头直接 err），这里手撸前缀匹配更皮实。
func charsetFromHeader(contentType string) string {
	i := strings.IndexByte(contentType, ';')
	if i < 0 {
		return ""
	}
	if m := charsetRe.FindStringSubmatch(contentType[i:]); m != nil {
		return m[1]
	}
	return ""
}

// charsetFromMeta 嗅探 HTML 头部 meta 声明（大小写不敏感；gb2312/gbk 统一走 GB18030 超集）。
func charsetFromMeta(raw []byte) string {
	sniff := raw
	if len(sniff) > fetchSniffLen {
		sniff = sniff[:fetchSniffLen]
	}
	if m := charsetRe.FindSubmatch(sniff); m != nil {
		return string(m[1])
	}
	return ""
}

// encodingByName 字符集名 → 解码器。只开中文互联网最常见的几种，其余交默认 UTF-8。
func encodingByName(name string) encoding.Encoding {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "gb2312", "gbk", "gb18030":
		// GB18030 是 GBK/GB2312 的超集，覆盖 dytt 这类老站全部字符。
		return simplifiedchinese.GB18030
	case "big5", "big5-hkscs":
		return traditionalchinese.Big5
	default:
		return nil
	}
}
