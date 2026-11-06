package downloadd

import (
	"encoding/xml"
	"testing"

	"github.com/dpb587/metalink"
)

// TestMetalink3ToV4 v3（2 URL + sha-256 + size）→ v4 → 序列化 → dpb587 库反序列化回读断言。
func TestMetalink3ToV4(t *testing.T) {
	v3 := []byte(`<?xml version="1.0" encoding="UTF-8"?>
<metalink version="3.0" xmlns="http://www.metalinker.org/">
 <files>
  <file name="distro-2026.iso">
   <size>1048576</size>
   <verification>
    <hashes>
     <hash type="sha-256">aabbccdd00112233445566778899eeaabbccdd00112233445566778899eeff00</hash>
    </hashes>
   </verification>
   <resources>
    <url type="http" preference="90" location="CN">http://mirrors.example.com/distro-2026.iso</url>
    <url type="ftp" preference="50" location="us">ftp://mirror2.example.com/distro-2026.iso</url>
   </resources>
  </file>
 </files>
</metalink>`)

	if !isMetalinkV3(v3) {
		t.Fatal("v3 嗅探失败：应识别为 metalink v3")
	}

	v4, err := convertMetalink3ToV4(v3)
	if err != nil {
		t.Fatalf("转换失败: %v", err)
	}
	out, err := marshalMetalink4(v4)
	if err != nil {
		t.Fatalf("序列化 v4 失败: %v", err)
	}

	// 用 dpb587/metalink 原库 Unmarshal 回来断言（验证产出确为库能理解的 v4）
	var back metalink.Metalink
	if err := xml.Unmarshal(out, &back); err != nil {
		t.Fatalf("v4 产物无法被 dpb587/metalink 解析: %v\n产物:\n%s", err, out)
	}
	if len(back.Files) != 1 {
		t.Fatalf("文件数 = %d, want 1", len(back.Files))
	}
	f := back.Files[0]
	if f.Name != "distro-2026.iso" {
		t.Errorf("name = %q, want distro-2026.iso", f.Name)
	}
	if f.Size != 1048576 {
		t.Errorf("size = %d, want 1048576", f.Size)
	}
	if len(f.Hashes) != 1 {
		t.Fatalf("hashes = %d, want 1", len(f.Hashes))
	}
	if f.Hashes[0].Type != metalink.HashTypeSHA256 {
		t.Errorf("hash type = %q, want sha-256", f.Hashes[0].Type)
	}
	if f.Hashes[0].Hash != "aabbccdd00112233445566778899eeaabbccdd00112233445566778899eeff00" {
		t.Errorf("hash 值不匹配: %q", f.Hashes[0].Hash)
	}
	if len(f.URLs) != 2 {
		t.Fatalf("urls = %d, want 2", len(f.URLs))
	}
	if f.URLs[0].URL != "http://mirrors.example.com/distro-2026.iso" {
		t.Errorf("url[0] = %q", f.URLs[0].URL)
	}
	if f.URLs[0].Priority == nil || *f.URLs[0].Priority != 90 {
		t.Errorf("url[0].priority = %v, want 90", f.URLs[0].Priority)
	}
	if f.URLs[0].Location != "CN" {
		t.Errorf("url[0].location = %q, want CN（location 大写归一）", f.URLs[0].Location)
	}
	if f.URLs[1].URL != "ftp://mirror2.example.com/distro-2026.iso" {
		t.Errorf("url[1] = %q", f.URLs[1].URL)
	}
	if f.URLs[1].Priority == nil || *f.URLs[1].Priority != 50 {
		t.Errorf("url[1].priority = %v, want 50", f.URLs[1].Priority)
	}
	if f.URLs[1].Location != "US" {
		t.Errorf("url[1].location = %q, want US", f.URLs[1].Location)
	}
}

// TestMetalink3MultiFile 一个 metalink 多文件：转换后顺序与名字保持。
func TestMetalink3MultiFile(t *testing.T) {
	v3 := []byte(`<?xml version="1.0" encoding="UTF-8"?>
<metalink version="3.0">
 <files>
  <file name="a.deb"><size>10</size><resources><url type="http" preference="80">http://h/a.deb</url></resources></file>
  <file name="b.deb"><size>20</size><resources><url type="http" preference="70">http://h/b.deb</url></resources></file>
 </files>
</metalink>`)
	v4, err := convertMetalink3ToV4(v3)
	if err != nil {
		t.Fatalf("转换失败: %v", err)
	}
	if len(v4.Files) != 2 || v4.Files[0].Name != "a.deb" || v4.Files[1].Name != "b.deb" {
		t.Fatalf("多文件转换结果不对: %+v", v4.Files)
	}
	// 未知 hash 类型应被丢弃而不是报错
	v3WithOddHash := []byte(`<metalink version="3.0"><files><file name="x"><size>1</size>` +
		`<verification><hashes><hash type="crc32">abcd</hash></hashes></verification>` +
		`<resources><url type="http">http://h/x</url></resources></file></files></metalink>`)
	v4b, err := convertMetalink3ToV4(v3WithOddHash)
	if err != nil {
		t.Fatalf("含未知 hash 的转换失败: %v", err)
	}
	if len(v4b.Files[0].Hashes) != 0 {
		t.Errorf("未知 hash 类型应被丢弃, got %d", len(v4b.Files[0].Hashes))
	}
}

// TestIsMetalinkV3NotV4 v4 内容不应误判为 v3。
func TestIsMetalinkV3NotV4(t *testing.T) {
	v4 := []byte(xml.Header + `<metalink xmlns="urn:ietf:params:xml:ns:metalink" type="static" generator="x">` +
		`<file name="a"><url>http://h/a</url></file></metalink>`)
	if isMetalinkV3(v4) {
		t.Error("v4 内容被误判为 v3")
	}
}
