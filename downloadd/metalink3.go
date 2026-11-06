// metalink3.go：Metalink v3（RFC 草案版，命名空间 http://www.metalinker.org/）→
// v4（RFC 5854）转换。dpb587/metalink 库只有 v4 结构体，v3 在这里手工解析。
//
// v3 结构：<metalink version="3.0"><files><file name="...">
//   <size>…</size>
//   <verification><hashes><hash type="sha-256">HEX</hash></hashes></verification>
//   <resources><url type="http" preference="90" location="cn">http://…</url></resources>
// </file></files></metalink>
package downloadd

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"strings"

	"github.com/dpb587/metalink"
)

// v3 命名空间与版本标识（嗅探依据）。
const (
	metalinkV3Namespace = "http://www.metalinker.org/"
	metalinkV3Version   = "3.0"
)

// ---- v3 手工结构（encoding/xml） ----

type metalinkV3 struct {
	XMLName xml.Name        `xml:"metalink"`
	Version string          `xml:"version,attr"`
	Files   metalinkV3Files `xml:"files"`
}

type metalinkV3Files struct {
	Files []metalinkV3File `xml:"file"`
}

type metalinkV3File struct {
	Name         string                `xml:"name,attr"`
	Size         uint64                `xml:"size"`       // <size>字节</size>
	Verification metalinkV3Verification `xml:"verification"`
	Resources    metalinkV3Resources   `xml:"resources"`
}

type metalinkV3Verification struct {
	Hashes []metalinkV3Hash `xml:"hashes>hash"`
}

type metalinkV3Hash struct {
	Type string `xml:"type,attr"`
	Hash string `xml:",chardata"`
}

type metalinkV3Resources struct {
	URLs []metalinkV3URL `xml:"url"`
}

type metalinkV3URL struct {
	Type       string `xml:"type,attr"` // http/ftp/bittorrent…（v4 无此概念，丢弃）
	Preference uint   `xml:"preference,attr"`
	Location   string `xml:"location,attr"`
	URL        string `xml:",chardata"`
}

// isMetalinkV3 嗅探：含 <metalink 且 version="3.0"（或 v3 命名空间）。
func isMetalinkV3(data []byte) bool {
	if !bytes.Contains(data, []byte("<metalink")) {
		return false
	}
	return bytes.Contains(data, []byte(`version="3.0"`)) ||
		bytes.Contains(data, []byte(`version='3.0'`)) ||
		bytes.Contains(data, []byte(metalinkV3Namespace))
}

// convertMetalink3ToV4 解析 v3 并填充 dpb587/metalink 的 v4 结构。
// hash type 小写归一到 v4 常量（md5/sha-1/sha-256/sha-512，不认识的丢弃）；
// preference → Priority（值透传）、location → Location 透传。
func convertMetalink3ToV4(data []byte) (*metalink.Metalink, error) {
	var v3 metalinkV3
	if err := xml.Unmarshal(data, &v3); err != nil {
		return nil, fmt.Errorf("解析 metalink v3: %w", err)
	}
	out := &metalink.Metalink{}
	for _, f := range v3.Files.Files {
		if f.Name == "" {
			continue
		}
		v4file := metalink.File{Name: f.Name, Size: f.Size}
		for _, h := range f.Verification.Hashes {
			ht := normalizeHashType(h.Type)
			if ht == "" || strings.TrimSpace(h.Hash) == "" {
				continue
			}
			v4file.Hashes = append(v4file.Hashes, metalink.Hash{Type: ht, Hash: strings.TrimSpace(h.Hash)})
		}
		for _, u := range f.Resources.URLs {
			u = trimV3URL(u)
			if u.URL == "" {
				continue
			}
			pref := u.Preference
			v4file.URLs = append(v4file.URLs, metalink.URL{
				Location: u.Location,
				Priority: &pref,
				URL:      u.URL,
			})
		}
		if len(v4file.URLs) == 0 && f.Size == 0 {
			continue // 既没地址又没大小的空条目无意义
		}
		out.Files = append(out.Files, v4file)
	}
	if len(out.Files) == 0 {
		return nil, fmt.Errorf("metalink v3 中没有可下载的文件")
	}
	return out, nil
}

// trimV3URL 清洗 v3 url 元素（文本含换行/空白）。
func trimV3URL(u metalinkV3URL) metalinkV3URL {
	u.URL = strings.TrimSpace(u.URL)
	u.Location = strings.ToUpper(strings.TrimSpace(u.Location)) // v4 location 是国家码大写
	return u
}

// normalizeHashType v3 hash type → v4 常量（小写归一；未知类型返回空表示丢弃）。
func normalizeHashType(t string) metalink.HashType {
	switch strings.ToLower(strings.TrimSpace(t)) {
	case "md5":
		return metalink.HashTypeMD5
	case "sha-1", "sha1":
		return metalink.HashTypeSHA1
	case "sha-256", "sha256":
		return metalink.HashTypeSHA256
	case "sha-512", "sha512":
		return metalink.HashTypeSHA512
	}
	return ""
}

// marshalMetalink4 序列化 v4（带 xml 头，便于直接落盘/送 aria2 addMetalink）。
func marshalMetalink4(m *metalink.Metalink) ([]byte, error) {
	out, err := xml.Marshal(m)
	if err != nil {
		return nil, fmt.Errorf("序列化 metalink v4: %w", err)
	}
	return append([]byte(xml.Header), out...), nil
}

// parseMetalink4 反序列化 v4（提交路径与测试共用：v4 内容直接用）。
func parseMetalink4(data []byte) (*metalink.Metalink, error) {
	var m metalink.Metalink
	if err := xml.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("解析 metalink v4: %w", err)
	}
	return &m, nil
}
