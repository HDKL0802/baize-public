package core

import (
	"os"
	"runtime"
	"sort"
	"strings"
	"time"
)

// 本文件让手机内核（bzcore）能以"跨端设备"的身份接后端指令。
//
// 安全边界：**只提供只读动作**。跨端下发不允许直接改手机上的待办/密码本，
// 也不允许读密码明文（密码本相关内容一律不出本机）。写操作要留在手机上由人自己操作，
// 或者以后单独走"必须审批"的动作通道再做。
//
// 动作名与 shared/proto 里的常量保持一致（todo.list / todo.stats / sys.info / ping）。

// DeviceActionList 本端支持的设备动作清单（注册时用作能力探测依据）
func DeviceActionList() []string {
	return []string{"ping", "sys.info", "todo.list", "todo.stats"}
}

// DeviceCaps 本端的设备能力标记（hello.caps）
func DeviceCaps() []string {
	return []string{"todo"}
}

// RunDeviceAction 执行一条来自后端的设备指令。
// 返回 (结果, 失败原因)：原因非空表示执行失败，结果一律回给后端落库。
func (s *Service) RunDeviceAction(action string, args map[string]any) (map[string]any, string) {
	action = strings.TrimSpace(action)
	switch action {
	case "ping":
		return map[string]any{"pong": true, "at": time.Now().UnixMilli(), "service": "bzcore"}, ""

	case "sys.info":
		host, _ := os.Hostname()
		snap, err := s.Snapshot()
		if err != nil {
			return nil, "读取本机状态失败：" + err.Error()
		}
		return map[string]any{
			"os": runtime.GOOS, "arch": runtime.GOARCH,
			"hostname": host, "version": Version,
			"dataDir": snap.DataDir,
			"todos":   len(snap.Todos),
			"vault":   len(snap.Vault), "vaultLocked": snap.VaultLocked,
			"now": time.Now().UnixMilli(),
		}, ""

	case "todo.list":
		return s.deviceTodoList(args)

	case "todo.stats":
		return s.deviceTodoStats(), ""
	}
	return nil, "手机内核不支持这个动作：" + action + "（支持：" + strings.Join(DeviceActionList(), ", ") + "）"
}

// deviceTodoList 列出待办（只读）。参数：owner(all|user|agent)、status(all|todo|doing|done)、keyword、limit(默认 20，上限 100)
func (s *Service) deviceTodoList(args map[string]any) (map[string]any, string) {
	snap, err := s.Snapshot()
	if err != nil {
		return nil, "读取待办失败：" + err.Error()
	}
	owner := strings.ToLower(strings.TrimSpace(argStr(args, "owner")))
	status := strings.ToLower(strings.TrimSpace(argStr(args, "status")))
	keyword := strings.ToLower(strings.TrimSpace(argStr(args, "keyword")))
	limit := argInt(args, "limit", 20)
	if limit <= 0 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}

	hits := make([]Task, 0, len(snap.Todos))
	for _, t := range snap.Todos {
		if owner == "user" || owner == "agent" {
			if t.Owner != owner {
				continue
			}
		}
		if status != "" && status != "all" && t.Status != status {
			continue
		}
		if keyword != "" {
			hay := strings.ToLower(t.Title + " " + t.Category + " " + t.Note)
			if !strings.Contains(hay, keyword) {
				continue
			}
		}
		hits = append(hits, t)
	}
	// 先按截止时间（空的排最后），再按创建时间
	sort.SliceStable(hits, func(i, j int) bool {
		di, dj := hits[i].Due, hits[j].Due
		if di == "" && dj != "" {
			return false
		}
		if di != "" && dj == "" {
			return true
		}
		if di != dj {
			return di < dj
		}
		return hits[i].CreatedAt < hits[j].CreatedAt
	})

	total := len(hits)
	truncated := false
	if len(hits) > limit {
		hits = hits[:limit]
		truncated = true
	}
	out := make([]map[string]any, 0, len(hits))
	for _, t := range hits {
		out = append(out, map[string]any{
			"id": t.ID, "title": t.Title, "category": t.Category,
			"priority": t.Priority, "form": t.Form, "due": t.Due,
			"owner": t.Owner, "status": t.Status, "estimate": t.Estimate,
			"weekly": t.Weekly, "auth": t.Auth,
		})
	}
	return map[string]any{
		"count": len(out), "matched": total, "truncated": truncated,
		"filter": map[string]any{"owner": orDefault(owner, "all"), "status": orDefault(status, "all"), "keyword": keyword},
		"todos":  out,
	}, ""
}

// deviceTodoStats 待办统计（只读）
func (s *Service) deviceTodoStats() map[string]any {
	snap, _ := s.Snapshot()
	now := time.Now().Format("2006-01-02T15:04")
	var total, user, agent, done, doing, todo, overdue int
	for _, t := range snap.Todos {
		total++
		switch t.Owner {
		case OwnerAgent:
			agent++
		default:
			user++
		}
		switch t.Status {
		case StatusDone:
			done++
		case StatusDoing:
			doing++
		default:
			todo++
		}
		if t.Status != StatusDone && t.Due != "" && t.Due < now {
			overdue++
		}
	}
	return map[string]any{
		"total": total, "user": user, "agent": agent,
		"done": done, "doing": doing, "todo": todo, "cancelled": total - done - doing - todo,
		"overdue": overdue,
		"sched":   snap.Sched, "leisure": snap.Leisure,
		"domains": snap.Domains,
		"agentStat": map[string]any{
			"total": snap.AgentStats.Total, "pendingAuth": snap.AgentStats.Pending, "done": snap.AgentStats.Done,
		},
	}
}

/* ---------- 参数取值小工具（只认字符串/数字，取不到就用默认值） ---------- */

// argStr 只认字符串：类型不对（数字/对象/数组）就当"没传"，交给默认值处理。
// 后端可能从 JSON 里传来各种类型，宁可忽略也不要拿一个奇怪的字符串去过滤。
func argStr(args map[string]any, key string) string {
	if args == nil {
		return ""
	}
	if s, ok := args[key].(string); ok {
		return s
	}
	return ""
}

func argInt(args map[string]any, key string, def int) int {
	switch v := args[key].(type) {
	case nil:
		return def
	case int:
		return v
	case int64:
		return int(v)
	case float64:
		return int(v)
	case string:
		n := 0
		neg := false
		for i, r := range strings.TrimSpace(v) {
			if i == 0 && r == '-' {
				neg = true
				continue
			}
			if r < '0' || r > '9' {
				return def
			}
			n = n*10 + int(r-'0')
		}
		if neg {
			return -n
		}
		return n
	}
	return def
}

func orDefault(s, def string) string {
	if strings.TrimSpace(s) == "" {
		return def
	}
	return s
}
