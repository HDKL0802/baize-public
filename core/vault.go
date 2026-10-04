package core

import (
	"crypto/rand"
	"errors"
	"fmt"
	"math/big"
	"sort"
	"strconv"
	"strings"
)

/* ---------- 规范化 ---------- */

// normalizePass 整理一条密码记录（对应 JS 的 normalizeVault 单条逻辑）：
//   - 补 history / group
//   - 当前密码被清空过，就把历史里最近的一条提上来当当前密码
//   - 清掉空密码与和当前密码重复的历史记录，并按时间倒序
func normalizePass(p *Pass) bool {
	changed := false
	if p.History == nil {
		p.History = []PassHistory{}
		changed = true
	}
	if p.Password == "" && len(p.History) > 0 {
		p.Password = p.History[0].Pwd
		p.History = p.History[1:]
		changed = true
	}
	before := len(p.History)
	p.History = filterHistory(p.History, p.Password)
	if len(p.History) != before {
		changed = true
	}
	return changed
}

// filterHistory 去掉空密码/与当前密码重复的，去重后按时间倒序
func filterHistory(list []PassHistory, current string) []PassHistory {
	out := make([]PassHistory, 0, len(list))
	seen := map[string]bool{}
	for _, h := range list {
		if h.Pwd == "" || h.Pwd == current || seen[h.Pwd] {
			continue
		}
		seen[h.Pwd] = true
		out = append(out, h)
	}
	sort.SliceStable(out, func(a, b int) bool { return out[a].At > out[b].At })
	return out
}

func historyHas(list []PassHistory, pwd string) bool {
	for _, h := range list {
		if h.Pwd == pwd {
			return true
		}
	}
	return false
}

/* ---------- 合并（同「平台 + 账号」一条记录） ---------- */

// MergePass 把一条密码并进库里，规则与 App 的 mergePassword 完全一致：
//   - 同「平台 + 账号」不存在 → 新增（history 去掉空值/与当前重复的）
//   - 已存在 → 归属/URL/备注只补不覆盖；新密码成为当前，旧密码沉到历史最前；
//     最后统一去重并按时间倒序
//
// 返回 "new" | "updated" | "same" 与记录下标。
func MergePass(list *[]Pass, item Pass) (string, int) {
	if list == nil {
		return "same", -1
	}
	now := Now()
	key := func(x Pass) string { return x.Title + "|" + x.Account }

	idx := -1
	for i := range *list {
		if key((*list)[i]) == key(item) {
			idx = i
			break
		}
	}

	if idx < 0 {
		p := Pass{
			ID:        firstNonEmpty(item.ID, NewID()),
			Title:     item.Title,
			Account:   item.Account,
			Group:     item.Group,
			Password:  item.Password,
			URL:       item.URL,
			Note:      item.Note,
			Source:    firstNonEmpty(item.Source, SourceManual),
			CreatedAt: orNow(item.CreatedAt, now),
			UpdatedAt: now,
		}
		for _, h := range item.History {
			if h.Pwd == "" || h.Pwd == p.Password {
				continue
			}
			p.History = append(p.History, PassHistory{Pwd: h.Pwd, At: orNow(h.At, now)})
		}
		p.History = filterHistory(p.History, p.Password)
		*list = append(*list, p)
		return "new", len(*list) - 1
	}

	dup := &(*list)[idx]
	if item.Group != "" && dup.Group == "" { // 归属只补不覆盖
		dup.Group = item.Group
	}
	if item.URL != "" && dup.URL == "" {
		dup.URL = item.URL
	}
	if item.Note != "" && dup.Note == "" {
		dup.Note = item.Note
	}
	for _, h := range item.History {
		if h.Pwd == "" || h.Pwd == dup.Password || historyHas(dup.History, h.Pwd) {
			continue
		}
		dup.History = append(dup.History, PassHistory{Pwd: h.Pwd, At: orNow(h.At, now)})
	}

	result := "same"
	if item.Password != "" && item.Password != dup.Password {
		if dup.Password != "" {
			dup.History = append([]PassHistory{{Pwd: dup.Password, At: now}}, dup.History...)
		}
		dup.Password = item.Password
		dup.UpdatedAt = now
		result = "updated"
	}
	dup.History = filterHistory(dup.History, dup.Password)
	return result, idx
}

// MergePass 把一条密码并进当前文档
func (d *Doc) MergePass(item Pass) (string, int) {
	return MergePass(&d.Vault, item)
}

/* ---------- 增删改查 ---------- */

// AddPass 直接新增一条（不做同账号合并，用于编辑保存场景）
func (d *Doc) AddPass(p Pass) Pass {
	p.Normalize()
	d.Vault = append(d.Vault, p)
	return p
}

// UpdatePass 覆盖一条记录的字段（保留历史：改密码时旧密码自动进历史）
func (d *Doc) UpdatePass(id string, patch Pass) error {
	i := d.passIndex(id)
	if i < 0 {
		return fmt.Errorf("找不到密码记录：%s", id)
	}
	rec := &d.Vault[i]
	now := Now()
	if patch.Password != "" && patch.Password != rec.Password {
		if rec.Password != "" {
			rec.History = append([]PassHistory{{Pwd: rec.Password, At: now}}, rec.History...)
		}
		rec.Password = patch.Password
	}
	if patch.Title != "" {
		rec.Title = patch.Title
	}
	if patch.Account != "" {
		rec.Account = patch.Account
	}
	rec.Group = patch.Group
	if patch.URL != "" {
		rec.URL = patch.URL
	}
	if patch.Note != "" {
		rec.Note = patch.Note
	}
	rec.UpdatedAt = now
	rec.History = filterHistory(rec.History, rec.Password)
	return nil
}

// RemovePass 删除一条
func (d *Doc) RemovePass(id string) error {
	i := d.passIndex(id)
	if i < 0 {
		return fmt.Errorf("找不到密码记录：%s", id)
	}
	d.Vault = append(d.Vault[:i], d.Vault[i+1:]...)
	return nil
}

// UseHistory 把某条历史密码换回当前（当前那条沉到历史最前）
func (d *Doc) UseHistory(id string, idx int) error {
	i := d.passIndex(id)
	if i < 0 {
		return fmt.Errorf("找不到密码记录：%s", id)
	}
	rec := &d.Vault[i]
	if idx < 0 || idx >= len(rec.History) {
		return fmt.Errorf("历史密码序号 %d 超出范围", idx)
	}
	picked := rec.History[idx]
	old := rec.Password
	rest := append([]PassHistory{}, rec.History[:idx]...)
	rest = append(rest, rec.History[idx+1:]...)
	rec.Password = picked.Pwd
	if old != "" {
		rest = append([]PassHistory{{Pwd: old, At: Now()}}, rest...)
	}
	rec.History = filterHistory(rest, rec.Password)
	rec.UpdatedAt = Now()
	return nil
}

// FindPass 定位密码记录：序号 | 平台 | 平台/账号 | id/id 前缀
func (d *Doc) FindPass(ref string) (int, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return -1, errors.New("缺少密码标识（序号/平台/平台+账号/id）")
	}
	if n, err := strconv.Atoi(ref); err == nil {
		if n >= 1 && n <= len(d.Vault) {
			return n - 1, nil
		}
		return -1, fmt.Errorf("序号 %d 超出范围（当前共 %d 条密码）", n, len(d.Vault))
	}
	if title, account, ok := strings.Cut(ref, "/"); ok {
		title, account = strings.TrimSpace(title), strings.TrimSpace(account)
		hit := -1
		for i := range d.Vault {
			if d.Vault[i].Title == title && d.Vault[i].Account == account {
				if hit >= 0 {
					return -1, fmt.Errorf("「%s / %s」匹配到多条记录，请用 id 定位", title, account)
				}
				hit = i
			}
		}
		if hit >= 0 {
			return hit, nil
		}
		return -1, fmt.Errorf("找不到「%s / %s」这条密码记录", title, account)
	}
	hits := []int{}
	for i := range d.Vault {
		if d.Vault[i].Title == ref {
			hits = append(hits, i)
		}
	}
	if len(hits) == 1 {
		return hits[0], nil
	}
	if len(hits) > 1 {
		accounts := make([]string, 0, len(hits))
		for _, i := range hits {
			accounts = append(accounts, fmt.Sprintf("「%s/%s」", d.Vault[i].Title, d.Vault[i].Account))
		}
		return -1, fmt.Errorf("「%s」有 %d 条记录（%s），请用「平台/账号」或 id 指定是哪一个",
			ref, len(hits), strings.Join(accounts, "、"))
	}
	ids := make([]string, len(d.Vault))
	for i := range d.Vault {
		ids[i] = d.Vault[i].ID
	}
	if i, err := MatchIDPrefix(ids, ref); err == nil {
		return i, nil
	}
	return -1, fmt.Errorf("找不到匹配的记录：%s（可用 序号 | 平台 | 平台/账号 | id）", ref)
}

func (d *Doc) passIndex(id string) int {
	for i := range d.Vault {
		if d.Vault[i].ID == id {
			return i
		}
	}
	return -1
}

/* ---------- 检索与分组 ---------- */

// SearchPass 关键词检索（平台/账号/归属/URL/备注）
func SearchPass(list []Pass, query string) []Pass {
	q := strings.ToLower(strings.TrimSpace(query))
	if q == "" {
		return list
	}
	out := []Pass{}
	for _, v := range list {
		hay := strings.ToLower(v.Title + " " + v.Account + " " + v.Group + " " + v.URL + " " + v.Note)
		if strings.Contains(hay, q) {
			out = append(out, v)
		}
	}
	return out
}

// PassGroup 按归属分的一组
type PassGroup struct {
	Group string
	Pass  []Pass
}

// GroupPass 按归属分组：非空归属按字典序在前，未分组排最后。
// 注：App 界面用中文拼音排序（localeCompare 'zh'），这里用码点序，顺序可能略有差别。
func GroupPass(list []Pass) []PassGroup {
	buckets := map[string][]Pass{}
	for _, v := range list {
		buckets[strings.TrimSpace(v.Group)] = append(buckets[strings.TrimSpace(v.Group)], v)
	}
	keys := make([]string, 0, len(buckets))
	for k := range buckets {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i] == "" {
			return false
		}
		if keys[j] == "" {
			return true
		}
		return keys[i] < keys[j]
	})
	out := make([]PassGroup, 0, len(keys))
	for _, k := range keys {
		out = append(out, PassGroup{Group: k, Pass: buckets[k]})
	}
	return out
}

// GroupNames 已用过的归属名（去重，供界面快捷选择）
func GroupNames(list []Pass) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, v := range list {
		g := strings.TrimSpace(v.Group)
		if g == "" || seen[g] {
			continue
		}
		seen[g] = true
		out = append(out, g)
	}
	sort.Strings(out)
	return out
}

/* ---------- 强密码生成 ---------- */

const (
	pwUpper = "ABCDEFGHJKLMNPQRSTUVWXYZ"
	pwLower = "abcdefghijkmnpqrstuvwxyz"
	pwDigit = "23456789"
	pwSym   = "!#$%+-_=@"
)

// GenStrongPassword 生成强密码（字符集与 App 一致：避开易混字符）
func GenStrongPassword(length int) (string, error) {
	if length <= 0 {
		length = 16
	}
	if length < 4 {
		length = 4
	}
	all := pwUpper + pwLower + pwDigit + pwSym
	out := make([]byte, 0, length)
	for _, set := range []string{pwUpper, pwLower, pwDigit, pwSym} {
		c, err := pickChar(set)
		if err != nil {
			return "", err
		}
		out = append(out, c)
	}
	for len(out) < length {
		c, err := pickChar(all)
		if err != nil {
			return "", err
		}
		out = append(out, c)
	}
	// 洗牌
	for i := len(out) - 1; i > 0; i-- {
		j, err := randInt(i + 1)
		if err != nil {
			return "", err
		}
		out[i], out[j] = out[j], out[i]
	}
	return string(out), nil
}

func pickChar(set string) (byte, error) {
	i, err := randInt(len(set))
	if err != nil {
		return 0, err
	}
	return set[i], nil
}

func randInt(n int) (int, error) {
	if n <= 0 {
		return 0, errors.New("取值范围必须大于 0")
	}
	v, err := rand.Int(rand.Reader, big.NewInt(int64(n)))
	if err != nil {
		return 0, fmt.Errorf("随机数生成失败：%w", err)
	}
	return int(v.Int64()), nil
}

/* ---------- 小工具 ---------- */

// Normalize 补齐一条密码记录的默认字段并整理历史，返回是否改动
func (p *Pass) Normalize() bool {
	changed := normalizePass(p)
	now := Now()
	if p.ID == "" {
		p.ID = NewID()
		changed = true
	}
	if p.Source == "" {
		p.Source = SourceManual
		changed = true
	}
	if p.CreatedAt == 0 {
		p.CreatedAt = now
		changed = true
	}
	if p.UpdatedAt == 0 {
		p.UpdatedAt = now
		changed = true
	}
	return changed
}

func orNow(v, fallback int64) int64 {
	if v == 0 {
		return fallback
	}
	return v
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
