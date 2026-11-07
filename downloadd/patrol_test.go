package downloadd

// 巡检补充单测：base64 内联载荷分流 + aria2 全局选项映射。

import (
	"encoding/base64"
	"testing"
)

func b64Of(b []byte) string { return base64.StdEncoding.EncodeToString(b) }

func TestClassifyBase64Torrent(t *testing.T) {
	// 最小 bencode 种子：dict 以 'd' 开头，内含 4:name
	torrent := []byte("d4:name8:test.bin4:infodee")
	c := Classify("base64:a.torrent|" + b64Of(torrent))
	if c.Err != "" {
		t.Fatalf("应识别为种子: %s", c.Err)
	}
	if c.Kind != KindTorrent {
		t.Fatalf("Kind = %s, want torrent", c.Kind)
	}
	if c.Name != "test.bin" {
		t.Fatalf("Name = %q, want test.bin", c.Name)
	}
	if string(c.TorrentData) != string(torrent) {
		t.Fatal("TorrentData 与载荷不一致")
	}
}

func TestClassifyBase64MetalinkV3(t *testing.T) {
	v3 := []byte(`<?xml version="1.0"?><metalink version="3.0"><files><file name="x.iso"><size>10</size><resources><url>http://a/x.iso</url></resources></file></files></metalink>`)
	c := Classify("base64:x.metalink|" + b64Of(v3))
	if c.Err != "" || c.Kind != KindMetalink {
		t.Fatalf("应识别为 metalink: kind=%s err=%s", c.Kind, c.Err)
	}
}

func TestClassifyBase64Bad(t *testing.T) {
	for _, in := range []string{
		"base64:noname",          // 缺 |
		"base64:|abc",            // 空文件名
		"base64:a.torrent|!!!",   // 非 base64
		"base64:a.txt|" + b64Of([]byte("plain text")), // 非种子非 metalink
	} {
		if c := Classify(in); c.Err == "" {
			t.Errorf("%q 应报错，实际 kind=%s", in, c.Kind)
		}
	}
}

func TestClassifyBase64SniffWrongExt(t *testing.T) {
	// 扩展名写错但内容是种子 → 内容嗅探兜底仍应识别
	torrent := []byte("d4:name4:abcd4:infodee")
	c := Classify("base64:somename.bin|" + b64Of(torrent))
	if c.Kind != KindTorrent {
		t.Fatalf("扩展名 .bin + 种子内容应嗅探为 torrent，实际 %s（err=%s）", c.Kind, c.Err)
	}
}

func TestGlobalOptionsFromSettings(t *testing.T) {
	s := map[string]any{
		"btDhtEnabled":         true,
		"btTrackers":           "http://t1/a\n, http://t1/a,http://t2/b\n\n",
		"maxConcurrentDownloads": float64(5),
		"downloadSpeedLimit":     float64(2097152),
	}
	opts := globalOptionsFromSettings(s)
	if opts["bt-enable-dht"] != "true" {
		t.Errorf("bt-enable-dht = %q", opts["bt-enable-dht"])
	}
	if opts["bt-tracker"] != "http://t1/a,http://t2/b" {
		t.Errorf("bt-tracker = %q（应去空去重逗号连接）", opts["bt-tracker"])
	}
	if opts["max-concurrent-downloads"] != "5" {
		t.Errorf("max-concurrent-downloads = %q", opts["max-concurrent-downloads"])
	}
	if opts["max-overall-download-limit"] != "2097152" {
		t.Errorf("max-overall-download-limit = %q", opts["max-overall-download-limit"])
	}
}

func TestGlobalOptionsMissingKeysSkipped(t *testing.T) {
	opts := globalOptionsFromSettings(map[string]any{"darkMode": float64(1)})
	// bt-tracker 有默认兜底（磁力开箱即用），其余无关键不应产生选项
	if len(opts) != 1 || opts["bt-tracker"] == "" {
		t.Errorf("无关键时只应有 bt-tracker 默认值: %v", opts)
	}
	// 限速 0 = 显式解除
	opts = globalOptionsFromSettings(map[string]any{"downloadSpeedLimit": float64(0)})
	if opts["max-overall-download-limit"] != "0" {
		t.Errorf("限速 0 应显式置 \"0\"，实际 %v", opts)
	}
	// 并发非法/0 → 跳过
	opts = globalOptionsFromSettings(map[string]any{"maxConcurrentDownloads": float64(0)})
	if _, ok := opts["max-concurrent-downloads"]; ok {
		t.Error("并发 0 不应下发")
	}
}

func TestNormalizeTrackerListEmpty(t *testing.T) {
	if normalizeTrackerList("\n , \n") != "" {
		t.Error("全空输入应返回空串")
	}
	if normalizeTrackerList("a,b") != "a,b" {
		t.Error("简单逗号串应原样保留")
	}
}
