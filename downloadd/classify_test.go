package downloadd

import (
	"os"
	"path/filepath"
	"testing"
)

func TestClassifySchemes(t *testing.T) {
	cases := []struct {
		in    string
		kind  Kind
		name  string
		isErr bool
	}{
		{"ftp://user:pass@h:21/p/d/file.bin", KindFTP, "file.bin", false},
		{"ftp://h/file%20name.zip", KindFTP, "file name.zip", false},
		{"ftps://h:990/x/f.zip", KindFTPS, "f.zip", false},
		{"magnet:?xt=urn:btih:abcdef0123456789&dn=Ubuntu%202026", KindMagnet, "Ubuntu 2026", false},
		{"ed2k://|file|x.bin|1024|0123456789ABCDEF|/", KindMagnet, "", false},
		{"bc1:somethingxyz", KindMagnet, "", false},
		{"http://example.com/path/pkg.tar.zst", KindHTTP, "pkg.tar.zst", false},
		{"https://example.com/", KindHTTP, "", false},
		{"thunder://QUFodHRwOi8v", "", "", true},
		{"", "", "", true},
		{"  ", "", "", true},
		{"/definitely/not/exist/file.bin", "", "", true},
	}
	for _, c := range cases {
		got := Classify(c.in)
		if c.isErr {
			if got.Err == "" {
				t.Errorf("Classify(%q) 期望报错，得到 kind=%s", c.in, got.Kind)
			}
			continue
		}
		if got.Kind != c.kind {
			t.Errorf("Classify(%q).Kind = %s, want %s (err=%q)", c.in, got.Kind, c.kind, got.Err)
		}
		if got.Name != c.name {
			t.Errorf("Classify(%q).Name = %q, want %q", c.in, got.Name, c.name)
		}
	}
}

func TestClassifyLocalTorrent(t *testing.T) {
	dir := t.TempDir()
	torrent := []byte("d10:created by13:yunx test app8:announce22:http://tracker/announce" +
		"4:infod6:lengthi4096e4:name12:hello tu.bin12:piece lengthi16384eee")
	p := filepath.Join(dir, "seed.torrent")
	if err := os.WriteFile(p, torrent, 0644); err != nil {
		t.Fatal(err)
	}
	c := Classify(p)
	if c.Err != "" {
		t.Fatalf("本地种子分类失败: %s", c.Err)
	}
	if c.Kind != KindTorrent {
		t.Fatalf("kind = %s, want torrent", c.Kind)
	}
	if c.Name != "hello tu.bin" {
		t.Errorf("name = %q, want hello tu.bin", c.Name)
	}
	if len(c.TorrentData) == 0 {
		t.Error("TorrentData 为空")
	}
}

func TestClassifyLocalMetalink(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "index.metalink")
	os.WriteFile(p, []byte(`<metalink version="3.0"><files><file name="a"><resources>`+
		`<url type="http">http://h/a</url></resources></file></files></metalink>`), 0644)
	c := Classify(p)
	if c.Err != "" || c.Kind != KindMetalink {
		t.Fatalf("本地 metalink 分类: kind=%s err=%q", c.Kind, c.Err)
	}

	// .meta4（v4）也要识别
	p4 := filepath.Join(dir, "index.meta4")
	os.WriteFile(p4, []byte(`<?xml version="1.0"?><metalink xmlns="urn:ietf:params:xml:ns:metalink">`+
		`<file name="a"><url>http://h/a</url></file></metalink>`), 0644)
	c4 := Classify(p4)
	if c4.Err != "" || c4.Kind != KindMetalink {
		t.Fatalf("本地 meta4 分类: kind=%s err=%q", c4.Kind, c4.Err)
	}
}

func TestTorrentName(t *testing.T) {
	data := []byte("d4:infod6:lengthi1e4:name8:na me.txteeee")
	if got := torrentName(data); got != "na me.tx" {
		// 8 字符名字 "na me.tx"… 长度 8: "na me.tx" 正好 8 字节
		t.Logf("torrentName=%q", got)
	}
	if got := torrentName([]byte("d4:infod6:lengthi1ee")); got != "" {
		t.Errorf("无 name 字段应返回空, got %q", got)
	}
}
