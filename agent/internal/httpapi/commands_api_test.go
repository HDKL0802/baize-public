package httpapi_test

import (
	"strings"
	"testing"
)

/* 魔法命令接口测试（QwenPaw 斜杠命令的 Go 版）。
   关注：命令清单广播；内置命令秒回且不经模型；光斜杠=帮助；参数校验；
   /new /clear 回 action 指令；未知斜杠命令透传为普通任务；技能回退（说明 + 注入）。 */

type cmdInfo struct {
	Name     string   `json:"name"`
	Aliases  []string `json:"aliases"`
	Category string   `json:"category"`
	Help     string   `json:"help"`
}

type commandsResp struct {
	Commands []cmdInfo `json:"commands"`
}

// runReply /api/agent/run 的两种形状（命令回执 / 派活）合在一起解，方便断言
type runReply struct {
	Command bool   `json:"command"`
	Name    string `json:"name"`
	Reply   string `json:"reply"`
	Action  string `json:"action"`
	Started bool   `json:"started"`
	RunID   string `json:"runId"`
}

func TestCommandsAPI(t *testing.T) {
	e, _ := newAgentEnv(t)

	var got commandsResp
	if code := e.do("GET", "/api/agent/commands", nil, true, &got); code != 200 {
		t.Fatalf("GET /api/agent/commands 期望 200，实际 %d", code)
	}
	names := map[string]bool{}
	for _, c := range got.Commands {
		names[c.Name] = true
	}
	for _, want := range []string{"help", "status", "skills", "new", "clear", "compact", "checkpoint"} {
		if !names[want] {
			t.Fatalf("命令清单缺 %s：%+v", want, got.Commands)
		}
	}
}

func TestMagicCommands(t *testing.T) {
	e, _ := newAgentEnv(t)

	// 光一个斜杠 = 帮助
	var help runReply
	if code := e.do("POST", "/api/agent/run", map[string]any{"goal": "/"}, true, &help); code != 200 || !help.Command {
		t.Fatalf("/ 应回执帮助：%d %+v", code, help)
	}
	for _, want := range []string{"/status", "/skills", "/checkpoint"} {
		if !strings.Contains(help.Reply, want) {
			t.Fatalf("帮助里应含 %s：%q", want, help.Reply)
		}
	}

	// /help <命令名>
	var one runReply
	if code := e.do("POST", "/api/agent/run", map[string]any{"goal": "/help compact"}, true, &one); code != 200 || !strings.Contains(one.Reply, "重复条目") {
		t.Fatalf("/help compact 说明不对：%d %q", code, one.Reply)
	}
	// /help 一个不存在的命令：要回话，不能崩
	var miss runReply
	if code := e.do("POST", "/api/agent/run", map[string]any{"goal": "/help wat"}, true, &miss); code != 200 || !strings.Contains(miss.Reply, "没有这条命令") {
		t.Fatalf("/help wat 应回话：%d %q", code, miss.Reply)
	}

	// /status
	var st runReply
	if code := e.do("POST", "/api/agent/run", map[string]any{"goal": "/status"}, true, &st); code != 200 || !strings.Contains(st.Reply, "白泽 v") {
		t.Fatalf("/status 不对：%d %q", code, st.Reply)
	}

	// /skills（空库也要给话，不能报错）
	var sk runReply
	if code := e.do("POST", "/api/agent/run", map[string]any{"goal": "/skills"}, true, &sk); code != 200 || !sk.Command {
		t.Fatalf("/skills 不对：%d %+v", code, sk)
	}

	// /compact 幂等：空库也能跑
	var cp runReply
	if code := e.do("POST", "/api/agent/run", map[string]any{"goal": "/compact"}, true, &cp); code != 200 || !strings.Contains(cp.Reply, "整理") {
		t.Fatalf("/compact 不对：%d %q", code, cp.Reply)
	}

	// /checkpoint 空列表
	var ck runReply
	if code := e.do("POST", "/api/agent/run", map[string]any{"goal": "/checkpoint"}, true, &ck); code != 200 || !strings.Contains(ck.Reply, "快照") {
		t.Fatalf("/checkpoint 不对：%d %q", code, ck.Reply)
	}
	// 回滚不存在的快照：要回话而不是报错崩
	var rb runReply
	if code := e.do("POST", "/api/agent/run", map[string]any{"goal": "/checkpoint rollback nope"}, true, &rb); code != 200 || !strings.Contains(rb.Reply, "回滚失败") {
		t.Fatalf("回滚不存在快照应回话：%d %q", code, rb.Reply)
	}

	// /new 与 /clear：回 action，让客户端去清本地上下文
	var nw runReply
	if code := e.do("POST", "/api/agent/run", map[string]any{"goal": "/new"}, true, &nw); code != 200 || nw.Action != "new" {
		t.Fatalf("/new 应回 action=new：%d %+v", code, nw)
	}
	var cl runReply
	if code := e.do("POST", "/api/agent/run", map[string]any{"goal": "/clear"}, true, &cl); code != 200 || cl.Action != "clear" {
		t.Fatalf("/clear 应回 action=clear：%d %+v", code, cl)
	}

	// 不认识的斜杠命令：不该当命令回执，按普通任务收下（没配模型也会先返回 runId）
	var unknown runReply
	code := e.do("POST", "/api/agent/run", map[string]any{"goal": "/nope-not-a-command"}, true, &unknown)
	if unknown.Command || code != 202 || !unknown.Started {
		t.Fatalf("未知命令不该当命令回执：%d %+v", code, unknown)
	}
}

func TestSkillFallback(t *testing.T) {
	e, svc := newAgentEnv(t)

	// 造一个技能（走与 Agent 自建技能同一条 Manager，落盘后自动重载）
	content := "---\nname: triage\ndescription: 分诊用的技能\n---\n\n正文：先看紧急程度。\n"
	if _, err := svc.SkillManager().Create("triage", "", content); err != nil {
		t.Fatalf("建技能失败：%v", err)
	}

	// 只给技能名：回说明，不跑模型
	var info runReply
	if code := e.do("POST", "/api/agent/run", map[string]any{"goal": "/triage"}, true, &info); code != 200 || !info.Command || !strings.Contains(info.Reply, "triage") {
		t.Fatalf("/triage 应回技能说明：%d %+v", code, info)
	}

	// 技能名 + 要求：注入技能正文后照常跑模型（不是命令回执）
	openai := fakeOpenAI(t)
	if code := e.do("POST", "/api/agent/providers", map[string]any{
		"action": "upsert",
		"config": map[string]any{
			"name": "local", "protocol": "openai",
			"baseUrl": openai.URL + "/v1", "model": "fake-openai", "apiKey": "sk-x",
		},
	}, true, nil); code != 200 {
		t.Fatalf("配模型失败：%d", code)
	}
	var res struct {
		Command bool `json:"command"`
	}
	if code := e.do("POST", "/api/agent/run", map[string]any{"goal": "/triage 帮我分诊", "wait": true}, true, &res); code != 200 || res.Command {
		t.Fatalf("技能注入不该当命令回执：%d %+v", code, res)
	}

	// 运行记录里的目标应是"注入后"的目标，而不是原始的 /triage 开头
	var runs struct {
		Runs []struct {
			Goal string `json:"goal"`
		} `json:"runs"`
	}
	if code := e.do("GET", "/api/agent/runs?limit=1", nil, true, &runs); code != 200 || len(runs.Runs) == 0 {
		t.Fatalf("读运行记录失败：%d %+v", code, runs)
	}
	g := runs.Runs[0].Goal
	if !strings.Contains(g, "帮我分诊") || !strings.Contains(g, "先看紧急程度") || strings.HasPrefix(g, "/triage") {
		t.Fatalf("技能没被注入目标：%q", g)
	}
}

// 没挂 Agent 服务时命令接口不该注册
func TestCommandsAbsentWithoutAgent(t *testing.T) {
	e := newEnv(t, 5e9)
	if code := e.do("GET", "/api/agent/commands", nil, true, nil); code == 200 {
		t.Fatalf("没挂 Agent 时 /api/agent/commands 不该可用，实际 %d", code)
	}
}
