package tools

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// RunToolBatch 一次调用按顺序执行多个**只读**工具，把结果一起带回来。
//
// 为什么要它：当后续步骤在执行前就完全确定（例如「先搜网页 → 再抓三个链接的正文 → 一起汇总」），
// 逐步让模型再发下一轮工具调用要多花好几轮往返；一次批量做完能省掉这些往返。
//
// 安全边界（很重要）：
//   - **批内不允许出现危险工具**（fs_delete / memory_forget / note_forget / shell…）——
//     审批闸门是**按工具名**判断的，只看到 run_tool_batch 这个名字；允许批内塞危险工具
//     等于给模型开了一条绕过人工审批的后门。这里直接拒绝并说明原因。
//   - **批内不允许出现会改现场的工具**（Mutating）——变更前快照同样是按工具名打的，
//     批内执行会跳过快照、回滚变得不可靠。一并拒绝。
//   - 不允许嵌套 run_tool_batch（防止递归放大）。
type RunToolBatch struct{ reg *Registry }

// NewRunToolBatch 创建 run_tool_batch（需要注册中心本体才能调用别的工具）
func NewRunToolBatch(reg *Registry) *RunToolBatch { return &RunToolBatch{reg: reg} }

// 一次批量的调用数上限：防止模型一口气塞几百个把那一步卡死
const runToolBatchMaxCalls = 20

// Name 工具名
func (t *RunToolBatch) Name() string { return "run_tool_batch" }

// Description 说明
func (t *RunToolBatch) Description() string {
	return "一次按顺序执行多个**只读**工具（如 web_fetch / file_search / memory / note 的查询动作），" +
		"把每个的结果一起返回；适合后续步骤已经确定的场景，能省掉多轮往返。" +
		"危险工具与会改现场的工具不允许放进批量（避免绕过审批与快照）。"
}

// Schema 参数说明
func (t *RunToolBatch) Schema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"calls": map[string]any{
				"type":        "array",
				"description": "要依次执行的调用，每项形如 {\"tool\":\"web_fetch\",\"args\":{\"url\":\"...\"}}",
				"items": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"tool": map[string]any{"type": "string", "description": "工具名"},
						"args": map[string]any{"type": "object", "description": "该工具的参数"},
					},
					"required": []string{"tool"},
				},
			},
			"stopOnError": map[string]any{"type": "boolean", "description": "遇到第一个失败就停，默认 false（继续跑完）"},
		},
		"required": []string{"calls"},
	}
}

// BatchCallResult 批内一次调用的结果
type BatchCallResult struct {
	Tool    string `json:"tool"`
	OK      bool   `json:"ok"`
	Result  any    `json:"result,omitempty"`
	Error   string `json:"error,omitempty"`
	Blocked string `json:"blocked,omitempty"` // 被安全策略挡下的原因（不是执行失败）
}

// Run 执行
func (t *RunToolBatch) Run(ctx context.Context, args map[string]any) (any, error) {
	if t.reg == nil {
		return nil, errors.New("工具注册中心不可用")
	}
	calls := argCallList(args["calls"])
	if len(calls) == 0 {
		return nil, errors.New("calls 不能为空（每项形如 {\"tool\":\"...\",\"args\":{...}}）")
	}
	truncated := false
	if len(calls) > runToolBatchMaxCalls {
		calls = calls[:runToolBatchMaxCalls]
		truncated = true
	}
	stopOnError := ArgBool(args, "stopOnError")

	results := make([]BatchCallResult, 0, len(calls))
	okCount, blockedCount := 0, 0
	for _, c := range calls {
		name := strings.TrimSpace(fmt.Sprint(c["tool"]))
		if name == "" {
			results = append(results, BatchCallResult{Tool: "", OK: false, Error: "缺少 tool"})
			if stopOnError {
				break
			}
			continue
		}
		sub, _ := c["args"].(map[string]any)

		// 安全闸门：危险 / 会改现场 / 嵌套批量，一律拒绝
		if reason := t.blockReason(name); reason != "" {
			blockedCount++
			results = append(results, BatchCallResult{Tool: name, OK: false, Blocked: reason})
			if stopOnError {
				break
			}
			continue
		}
		if _, ok := t.reg.Get(name); !ok {
			results = append(results, BatchCallResult{Tool: name, OK: false, Error: "未知工具：" + name})
			if stopOnError {
				break
			}
			continue
		}
		res, err := t.reg.Call(ctx, name, sub)
		if err != nil {
			results = append(results, BatchCallResult{Tool: name, OK: false, Error: err.Error()})
			if stopOnError {
				break
			}
			continue
		}
		okCount++
		results = append(results, BatchCallResult{Tool: name, OK: true, Result: res})
	}

	out := map[string]any{
		"calls": len(results), "okCount": okCount, "results": results,
	}
	if blockedCount > 0 {
		out["blockedCount"] = blockedCount
		out["note"] = "有调用被安全策略挡下：危险工具与会改现场的工具不能放进批量，" +
			"它们必须单独调用走人工审批与变更前快照"
	}
	if truncated {
		out["truncated"] = true
		out["maxCalls"] = runToolBatchMaxCalls
	}
	return out, nil
}

// blockReason 判断某个工具能否放进批量
func (t *RunToolBatch) blockReason(name string) string {
	if name == t.Name() {
		return "不允许嵌套 run_tool_batch"
	}
	// agent_delegate 会再派一层活（可能自己进审批），批内调用会让链路与审批难以追踪
	if name == "agent_delegate" {
		return "派发子 Agent 请单独调用（批内调用会让链路与审批难以追踪）"
	}
	if t.reg.IsDangerous(name) {
		return "危险工具必须单独调用并走人工审批（批量调用会让审批闸门看不到它）"
	}
	if t.reg.IsMutating(name) {
		return "会改动工作目录/数据的工具必须单独调用（批量会跳过变更前快照）"
	}
	return ""
}

// argCallList 把 calls 参数收敛成 []map[string]any（容忍字符串形式的 args）
func argCallList(v any) []map[string]any {
	arr, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]map[string]any, 0, len(arr))
	for _, it := range arr {
		m, ok := it.(map[string]any)
		if !ok {
			continue
		}
		out = append(out, m)
	}
	return out
}

// RegisterRunToolBatch 注册 run_tool_batch
func RegisterRunToolBatch(r *Registry) {
	if r == nil {
		return
	}
	r.Register(NewRunToolBatch(r))
}
