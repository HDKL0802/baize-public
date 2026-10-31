package notebook

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// legacyNoteFlag 迁移完成标记（写在 note_meta，保证只搬一次）
const legacyNoteFlag = "legacy_notes_imported"

// ImportLegacyNotes 把旧记忆库里的笔记一次性还原成 vault 里的 .md 文件。
//
// 旧实现把笔记当成记忆分块存在 memory.db（chunks 表 source='note'、kind='note'，
// source_ref 就是相对路径）。升级后文件才是正本，老数据不搬过来就等于"消失"了。
//
// 幂等：note_meta 里打标记，跑过就直接返回；目标路径已有文件的一律跳过，
// 绝不覆盖用户在 vault 里新写/新导入的笔记。
func (v *Vault) ImportLegacyNotes(memoryDBPath string) (ImportResult, error) {
	out := ImportResult{}
	if v.metaGet(legacyNoteFlag) == "1" {
		return out, nil
	}
	if _, err := os.Stat(memoryDBPath); err != nil {
		// 还没生成过记忆库：没什么可搬的
		v.metaSet(legacyNoteFlag, "1")
		return out, nil
	}

	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(memoryDBPath)+
		"?mode=ro&_pragma=busy_timeout(5000)")
	if err != nil {
		return out, fmt.Errorf("打开旧记忆库失败：%w", err)
	}
	defer db.Close()

	rows, err := db.Query(`SELECT namespace,source_ref,title,content
		FROM chunks WHERE source='note' AND kind='note'`)
	if err != nil {
		// 老库可能根本没有 chunks 表（或结构更老）：按"没有旧笔记"处理
		_ = rows
		v.metaSet(legacyNoteFlag, "1")
		return out, nil
	}

	var files []ImportFile
	for rows.Next() {
		var ns, ref, title, content string
		if err := rows.Scan(&ns, &ref, &title, &content); err != nil {
			rows.Close()
			return out, err
		}
		if strings.TrimSpace(content) == "" {
			out.Skipped++
			continue
		}
		p := legacyNotePath(ns, ref, title)
		rel, err := cleanPath(p)
		if err != nil {
			out.Skipped++
			out.Errors = append(out.Errors, fmt.Sprintf("%s：%v", p, err))
			continue
		}
		// 已有同名文件就别碰（用户可能已经在新界面里改过）
		if _, err := os.Stat(filepath.Join(v.root, filepath.FromSlash(rel))); err == nil {
			out.Skipped++
			continue
		}
		files = append(files, ImportFile{Path: rel, Content: content})
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return out, err
	}

	if len(files) == 0 {
		v.metaSet(legacyNoteFlag, "1")
		return out, nil
	}

	res, err := v.Import(files)
	if err != nil {
		return res, err
	}
	res.Skipped += out.Skipped
	res.Errors = append(out.Errors, res.Errors...)
	v.metaSet(legacyNoteFlag, "1")
	return res, nil
}

// legacyNotePath 还原旧笔记的落盘路径。
// 非默认命名空间的笔记放进 legacy/<namespace>/ 下，避免不同分区同名互相覆盖。
func legacyNotePath(ns, ref, title string) string {
	p := strings.TrimSpace(filepath.ToSlash(ref))
	if p == "" {
		base := strings.TrimSpace(title)
		if base == "" {
			base = "未命名"
		}
		p = sanitizeFileName(base) + ".md"
	}
	ns = strings.TrimSpace(ns)
	if ns != "" && ns != "default" {
		p = "legacy/" + sanitizeFileName(ns) + "/" + p
	}
	return p
}

// metaGet 读一个元数据项（内部加锁）
func (v *Vault) metaGet(k string) string {
	v.mu.Lock()
	defer v.mu.Unlock()
	var s string
	_ = v.db.QueryRow(`SELECT v FROM note_meta WHERE k=?`, k).Scan(&s)
	return s
}

// metaSet 写一个元数据项（内部加锁）
func (v *Vault) metaSet(k, val string) {
	v.mu.Lock()
	defer v.mu.Unlock()
	_, _ = v.db.Exec(`INSERT OR REPLACE INTO note_meta(k,v) VALUES(?,?)`, k, val)
}
