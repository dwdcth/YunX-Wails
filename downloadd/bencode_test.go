package downloadd

// bencode/torrentInfo 单测：多文件种子（含 .utf-8 名）、单文件、坏数据。

import (
	"fmt"
	"testing"
)

func TestBencodeDecodePrimitives(t *testing.T) {
	for _, c := range []struct {
		in   string
		want any
	}{
		{"i42e", int64(42)},
		{"i-7e", int64(-7)},
		{"4:spam", "spam"},
		{"0:", ""},
		{"li1ei2ee", []any{int64(1), int64(2)}},
		{"d1:ai1ee", map[string]any{"a": int64(1)}},
	} {
		got, _, err := bdecode([]byte(c.in))
		if err != nil {
			t.Errorf("%q: %v", c.in, err)
			continue
		}
		if fmt.Sprint(got) != fmt.Sprint(c.want) {
			t.Errorf("%q = %v, want %v", c.in, got, c.want)
		}
	}
	for _, bad := range []string{"", "x", "i12", "5:ab", "d1:a", "l"} {
		if _, _, err := bdecode([]byte(bad)); err == nil {
			t.Errorf("%q 应报错", bad)
		}
	}
}

func mkMultiTorrent() []byte {
	return []byte("d8:announce13:http://tr/ann4:infod5:filesld4:pathl3:dir5:a.mp4e6:lengthi1000eed4:pathl3:dir5:b.srt" +
		"e6:lengthi2000eee4:name12:我的种子ee")
}

func TestTorrentInfoMulti(t *testing.T) {
	name, files, total, err := torrentInfo(mkMultiTorrent())
	if err != nil {
		t.Fatal(err)
	}
	if name != "我的种子" {
		t.Errorf("name = %q", name)
	}
	if len(files) != 2 || files[0].Path != "dir/a.mp4" || files[0].Index != 1 || files[1].Index != 2 {
		t.Errorf("files = %+v", files)
	}
	if total != 3000 {
		t.Errorf("total = %d", total)
	}
}

func TestTorrentInfoSingle(t *testing.T) {
	data := []byte("d4:infod4:name8:solo.mp46:lengthi500eee")
	name, files, total, err := torrentInfo(data)
	if err != nil || name != "solo.mp4" || len(files) != 1 || files[0].Index != 1 || total != 500 {
		t.Fatalf("name=%s files=%+v total=%d err=%v", name, files, total, err)
	}
}

func TestTorrentInfoUTF8NamePriority(t *testing.T) {
	data := []byte("d4:infod10:name.utf-89:真名字4:name6:fakexx6:lengthi1eee")
	name, _, _, err := torrentInfo(data)
	if err != nil || name != "真名字" {
		t.Fatalf("utf-8 名应优先: %s %v", name, err)
	}
}

func TestTorrentInfoBad(t *testing.T) {
	for _, bad := range []string{"", "i1e", "d4:infod6:lengthi1eee"} {
		if _, _, _, err := torrentInfo([]byte(bad)); err == nil {
			t.Errorf("%q 应报错", bad)
		}
	}
}
