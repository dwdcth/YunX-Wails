package downloadd

// bencode.go：最小 bencode 解码（种子文件用）。解码为 map[string]any / []any / int64 / string，
// 列表保序（select-file 需与 aria2 的文件序号对齐）。

import (
	"fmt"
	"strings"
)

func bdecode(data []byte) (any, int, error) {
	if len(data) == 0 {
		return nil, 0, fmt.Errorf("bencode: 空数据")
	}
	switch c := data[0]; {
	case c == 'i': // i<number>e
		end := strings.IndexByte(string(data), 'e')
		if end < 0 {
			return nil, 0, fmt.Errorf("bencode: int 未闭合")
		}
		var n int64
		if _, err := fmt.Sscanf(string(data[1:end]), "%d", &n); err != nil {
			return nil, 0, fmt.Errorf("bencode: int 解析失败")
		}
		return n, end + 1, nil
	case c >= '0' && c <= '9': // <len>:<bytes>
		colon := strings.IndexByte(string(data), ':')
		if colon < 0 {
			return nil, 0, fmt.Errorf("bencode: string 缺冒号")
		}
		var n int
		if _, err := fmt.Sscanf(string(data[:colon]), "%d", &n); err != nil || n < 0 || colon+1+n > len(data) {
			return nil, 0, fmt.Errorf("bencode: string 长度非法")
		}
		return string(data[colon+1 : colon+1+n]), colon + 1 + n, nil
	case c == 'l': // l<items>e
		rest := data[1:]
		var list []any
		for len(rest) > 0 && rest[0] != 'e' {
			v, used, err := bdecode(rest)
			if err != nil {
				return nil, 0, err
			}
			list = append(list, v)
			rest = rest[used:]
		}
		if len(rest) == 0 {
			return nil, 0, fmt.Errorf("bencode: list 未闭合")
		}
		return list, len(data) - len(rest) + 1, nil
	case c == 'd': // d<kv>e（键保持出现顺序）
		rest := data[1:]
		dict := map[string]any{}
		for len(rest) > 0 && rest[0] != 'e' {
			k, used, err := bdecode(rest)
			if err != nil {
				return nil, 0, err
			}
			ks, ok := k.(string)
			if !ok {
				return nil, 0, fmt.Errorf("bencode: dict 键非字符串")
			}
			rest = rest[used:]
			v, used2, err := bdecode(rest)
			if err != nil {
				return nil, 0, err
			}
			dict[ks] = v
			rest = rest[used2:]
		}
		if len(rest) == 0 {
			return nil, 0, fmt.Errorf("bencode: dict 未闭合")
		}
		return dict, len(data) - len(rest) + 1, nil
	}
	return nil, 0, fmt.Errorf("bencode: 未知类型 %q", data[0])
}

// torrentInfo 解析种子的名字与文件清单（多文件取 info.files[].path.length 拼接；单文件取 info.name+length）。
// 返回的 Index 与 aria2 getFiles 的 1 基序号一致（按种子内出现顺序）。
func torrentInfo(data []byte) (name string, files []TorrentFile, total int64, err error) {
	root, _, err := bdecode(data)
	if err != nil {
		return "", nil, 0, err
	}
	dict, ok := root.(map[string]any)
	if !ok {
		return "", nil, 0, fmt.Errorf("种子根节点不是字典")
	}
	info, ok := dict["info"].(map[string]any)
	if !ok {
		return "", nil, 0, fmt.Errorf("种子缺 info")
	}
	// 名字：name.utf-8 优先
	name, _ = info["name"].(string)
	if n2, ok := info["name.utf-8"].(string); ok && n2 != "" {
		name = n2
	}
	if raw, ok := info["name"].(int64); ok {
		_ = raw // name 是字符串；此处仅防御
	}
	if name == "" {
		return "", nil, 0, fmt.Errorf("种子缺 name")
	}
	if fl, ok := info["files"].([]any); ok {
		for i, f := range fl {
			fd, ok := f.(map[string]any)
			if !ok {
				continue
			}
			var parts []string
			// path.utf-8 优先
			if pu, ok := fd["path.utf-8"].([]any); ok {
				for _, p := range pu {
					if s, ok := p.(string); ok {
						parts = append(parts, s)
					}
				}
			} else if pa, ok := fd["path"].([]any); ok {
				for _, p := range pa {
					if s, ok := p.(string); ok {
						parts = append(parts, s)
					}
				}
			}
			length, _ := fd["length"].(int64)
			if len(parts) == 0 {
				continue
			}
			files = append(files, TorrentFile{
				Index: i + 1, Path: strings.Join(parts, "/"), Length: length,
			})
			total += length
		}
		return name, files, total, nil
	}
	// 单文件
	length, _ := info["length"].(int64)
	files = []TorrentFile{{Index: 1, Path: name, Length: length}}
	return name, files, length, nil
}
