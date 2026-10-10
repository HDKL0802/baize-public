package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"baize/shared/proto"
)

// 本文件是**桌面控制**的权限闸门：决定这台电脑允许白泽的智能体做什么。
//
// 为什么闸门放在**设备侧**而不是只靠后端：后端只是"派活方"，一旦它配错了、
// 或者配对令牌泄漏，受害的是这台电脑。所以能不给的能力就不要给出去，
// 真正的判定发生在**本机**（越权直接拒绝，并且说清为什么）。
//
// 五档（数字越大能力越强）：
//
//	0 关闭          连读都不行（只剩 ping 探活，否则后端会以为设备掉线）
//	1 只读          基础信息：本机信息、当前前台窗口
//	2 只读指定目录  在用户圈定的目录里读文件信息
//	3 只读指定盘    在用户勾选的整个盘里读文件信息（**高风险，要过免责**）
//	4 完全访问      不再限定范围；删除等危险动作仍然要过后端人工审批（**高风险，要过免责**）
const (
	PermOff        = 0
	PermReadOnly   = 1
	PermDirScoped  = 2
	PermDiskScoped = 3
	PermFullAccess = 4
)

// permMax 最大等级（界面/接口校验用）
const permMax = PermFullAccess

var errNeedDisclaimer = errors.New("选择这一档（指定盘 / 完全访问）需要先确认风险免责声明")
var errBadDataDir = errors.New("数据目录不合法（要一个能创建/写入的目录路径）")

func errBadPerm(perm int) error {
	return fmt.Errorf("权限等级不合法：%d（可用 0=%d 关闭 / 1 只读 / 2 只读指定目录 / 3 只读指定盘 / 4 完全访问）",
		perm, PermOff)
}

// validPerm 等级是否合法
func validPerm(p int) bool { return p >= PermOff && p <= permMax }

// needsDisclaimer 这一档是否必须先确认风险免责（用户明确要求：最大的两档要弹提示）
func needsDisclaimer(p int) bool { return p >= PermDiskScoped }

// permNeedsScopes 这一档是否需要用户圈定范围
func permNeedsScopes(p int) bool { return p == PermDirScoped || p == PermDiskScoped }

// PermTitle 等级的中文名（界面与日志共用）
func PermTitle(p int) string {
	switch p {
	case PermOff:
		return "关闭（什么都不能做）"
	case PermReadOnly:
		return "只读（本机信息 / 当前窗口）"
	case PermDirScoped:
		return "只读指定目录"
	case PermDiskScoped:
		return "只读指定盘"
	case PermFullAccess:
		return "完全访问"
	}
	return "未知"
}

// cleanScopes 归一范围清单：去空、去重、转绝对路径；目录与盘都按同一函数处理。
// 盘（D:）会被补成 D:\，方便后面做前缀比较。
func cleanScopes(in []string) []string {
	out := make([]string, 0, len(in))
	seen := map[string]bool{}
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		s = normalizeScope(s)
		if s == "" {
			continue
		}
		key := strings.ToLower(s)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, s)
	}
	return out
}

// normalizeScope 把范围项归一成可比较的绝对路径（盘补上 \）
func normalizeScope(s string) string {
	s = strings.ReplaceAll(s, "/", `\`)
	// 纯盘符：D: 或 D:\ 都归一成 D:\
	if len(s) == 2 && s[1] == ':' {
		return s + `\`
	}
	if abs, err := filepath.Abs(s); err == nil {
		return filepath.Clean(abs)
	}
	return filepath.Clean(s)
}

// currentPerm 当前权限等级与范围（快照）
func currentPerm() (int, []string) {
	cfgMu.RLock()
	defer cfgMu.RUnlock()
	return cfg.GuiPerm, append([]string{}, cfg.GuiScopes...)
}

// permNow 当前权限等级（上报 hello 用）
func permNow() int {
	p, _ := currentPerm()
	return p
}

// disclaimerAcked 用户是否已确认过风险免责声明
func disclaimerAcked() bool {
	cfgMu.RLock()
	defer cfgMu.RUnlock()
	return cfg.DisclaimerAck
}

// permLevelViews 各等级说明（给界面渲染，避免前端硬编码这套文案）。
// needScope = 需要用户圈定范围；risky = 需要先确认风险免责。
func permLevelViews() []map[string]any {
	return []map[string]any{
		{"level": PermOff, "title": "关闭", "risky": false, "needScope": false,
			"desc": "什么都不能做（连读都不行），只剩探活心跳。"},
		{"level": PermReadOnly, "title": "只读", "risky": false, "needScope": false,
			"desc": "只能读本机信息与当前前台窗口，不碰任何文件。"},
		{"level": PermDirScoped, "title": "只读指定目录", "risky": false, "needScope": true,
			"desc": "只在你自己圈定的目录里读文件信息。"},
		{"level": PermDiskScoped, "title": "只读指定盘", "risky": true, "needScope": true,
			"desc": "在你勾选的整个盘里读文件信息（范围大得多，选它要先确认风险）。"},
		{"level": PermFullAccess, "title": "完全访问", "risky": true, "needScope": false,
			"desc": "不再限定范围；删除等危险动作仍会先弹人工审批，但风险由你自己承担。"},
	}
}

// scopesNow 当前范围清单（上报 hello 用）
func scopesNow() []string {
	_, s := currentPerm()
	return s
}

// shellEnabled 是否允许在本机执行命令（电脑控制第 2 档）
func shellEnabled() bool {
	cfgMu.RLock()
	defer cfgMu.RUnlock()
	return cfg.GuiShell && len(cfg.GuiShellAllowCmds) > 0
}

// shellAllowList 当前允许的命令名白名单（副本）
func shellAllowList() []string {
	cfgMu.RLock()
	defer cfgMu.RUnlock()
	return append([]string{}, cfg.GuiShellAllowCmds...)
}

// permCaps 按当前权限算出该上报给后端的能力标记。
//
// 这里直接决定后端能派什么活 —— 权限不够的能力**根本不上报**，
// 后端也就不会派这类任务过来（比"派过来再拒绝"更干净）。
func permCaps() []string {
	perm, _ := currentPerm()
	var caps []string
	switch {
	case perm <= PermOff:
		return []string{}
	case perm == PermReadOnly:
		caps = []string{proto.CapWindow}
	case perm == PermFullAccess:
		caps = []string{proto.CapWindow, proto.CapFs, proto.CapFsDelete}
	default:
		caps = []string{proto.CapWindow, proto.CapFs}
	}
	// 命令执行是独立开关（与文件权限档位不是一条轴）：开了才上报
	if shellEnabled() {
		caps = append(caps, proto.CapShell)
	}
	return caps
}

// permGate 桌面控制的总闸门：动作能不能执行、路径合不合法。
//
// 返回 nil 表示放行；返回错误表示拒绝（错误信息直接回给后端与用户看，所以要说清原因）。
// paths 只在 fs 类动作时需要（调用方先把 paths 参数抽出来再传进来，
// 这样本文件不依赖 Windows 专有的参数解析代码，非 Windows 也能编译）。
func permGate(action string, paths []string) error {
	perm, scopes := currentPerm()

	// ping 是探活，任何等级都放行：禁掉它只会让后端一直显示"设备掉线"，
	// 而它本身不泄露任何信息、也不改任何东西。
	if action == proto.ActionPing {
		return nil
	}
	// 执行命令是**独立开关**（不看文件权限档位，免得"想跑条命令还得先把整个盘放开"）：
	// 只认「允许执行命令」这个开关，命令本身逐条在校验（shellGate，要拿到命令文本）。
	if action == proto.ActionSysExec {
		if !shellEnabled() {
			return fmt.Errorf("本机没开「允许执行命令」（设置 → 桌面控制 → 允许执行命令）；这是高危能力，默认关闭")
		}
		return nil
	}
	switch perm {
	case PermOff:
		return fmt.Errorf("本机已把桌面控制设为「关闭」，不接受任何动作（含 %s）；要放开请在桌面端「设置 → 桌面控制」里改", action)
	case PermReadOnly:
		if action == proto.ActionSysInfo || action == proto.ActionWindowNow {
			return nil
		}
		return fmt.Errorf("本机桌面控制是「只读」档，只允许 %s / %s，不允许 %s；想让它读文件请在「设置 → 桌面控制」里升到「只读指定目录」或更高",
			proto.ActionSysInfo, proto.ActionWindowNow, action)
	}

	switch action {
	case proto.ActionSysInfo, proto.ActionWindowNow:
		return nil
	case proto.ActionFsStat, proto.ActionFsList:
		if len(paths) == 0 {
			return errors.New("缺少参数 paths")
		}
		return checkScopes(paths, perm, scopes)
	case proto.ActionFsDelete:
		// 删除只在「完全访问」档才允许；而且**仍然**会过后端的人工审批闸门
		// （危险动作强制审批是项目铁律，设备侧这一档只是"允许它进入审批"）。
		if perm < PermFullAccess {
			return fmt.Errorf("本机桌面控制是「%s」档，只读，不允许删除；要允许删除请升到「完全访问」并确认风险提示", PermTitle(perm))
		}
		return nil
	default:
		return fmt.Errorf("本机不支持的动作：%s", action)
	}
}

// checkScopes 校验一批路径是否都落在允许范围内（level 2 看目录、level 3 看盘）
func checkScopes(paths []string, perm int, scopes []string) error {
	if perm >= PermFullAccess {
		return nil
	}
	if len(scopes) == 0 {
		return fmt.Errorf("本机还没圈定允许读取的范围（当前是「%s」档）；请在桌面端「设置 → 桌面控制」里选好目录或盘", PermTitle(perm))
	}
	for _, p := range paths {
		abs, err := filepath.Abs(p)
		if err != nil {
			return fmt.Errorf("路径不合法：%s", p)
		}
		abs = filepath.Clean(abs)
		if !pathAllowed(abs, perm, scopes) {
			return fmt.Errorf("路径 %s 不在本机允许的范围内（%s：%s）；要放开请到桌面端「设置 → 桌面控制」里加范围",
				abs, PermTitle(perm), strings.Join(scopes, "、"))
		}
	}
	return nil
}

// pathAllowed 单个路径是否在范围内
func pathAllowed(abs string, perm int, scopes []string) bool {
	vol := volumeOf(abs)
	target := strings.ToLower(abs)
	for _, s := range scopes {
		// 限定盘：只看卷是否相同（D:\ 下任意路径都算）
		if perm == PermDiskScoped && volumeOf(s) == vol {
			return true
		}
		// 目录（也兼容"范围给的是盘"的情况）：前缀匹配，且必须是完整一段
		scope := strings.ToLower(filepath.Clean(s))
		if target == scope || strings.HasPrefix(target, strings.TrimRight(scope, `\`)+`\`) {
			return true
		}
	}
	return false
}

// volumeOf 取路径所在的卷（Windows：D:；其它平台取根）
func volumeOf(p string) string {
	p = filepath.Clean(p)
	if len(p) >= 2 && p[1] == ':' {
		return strings.ToLower(p[:2])
	}
	vol := filepath.VolumeName(p)
	if vol != "" {
		return strings.ToLower(vol)
	}
	return "/"
}

// normalizeCmdName 命令名归一：小写、去引号、带路径取 basename、去 .exe/.cmd/.bat。
// 与后端 shell 白名单同一口径（`/bin/echo` → `echo`、`DIR.EXE` → `dir`）。
func normalizeCmdName(s string) string {
	s = strings.TrimSpace(strings.Trim(strings.TrimSpace(s), `"'`))
	if s == "" {
		return ""
	}
	s = strings.ReplaceAll(s, "/", `\`)
	if i := strings.LastIndex(s, `\`); i >= 0 {
		s = s[i+1:]
	}
	s = strings.ToLower(s)
	for _, ext := range []string{".exe", ".cmd", ".bat", ".com"} {
		if strings.HasSuffix(s, ext) {
			s = strings.TrimSuffix(s, ext)
			break
		}
	}
	return s
}

// listVolumes 列出本机可选的盘（给界面下拉用）。取不到就返回空，不假装有。
func listVolumes() []string {
	out := []string{}
	// Windows：A: 到 Z: 里能 Stat 到的就算存在
	for c := 'A'; c <= 'Z'; c++ {
		root := string(c) + `:\`
		if fi, err := os.Stat(root); err == nil && fi.IsDir() {
			out = append(out, root)
		}
	}
	return out
}
