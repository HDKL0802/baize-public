package llm

import (
	"strings"
	"testing"
)

// 模型接口对函数名只接受 [a-zA-Z0-9_-]，带点号会被 DeepSeek/OpenAI 直接 400 拒绝。
// 这条用例把规矩钉住：不合规的名字必须在本地就被拦下并说清原因。
func TestCheckToolNames(t *testing.T) {
	ok := []ToolSpec{{Name: "fs_read"}, {Name: "mcp_demo_echo"}, {Name: "a-b_c"}, {Name: strings.Repeat("x", 64)}}
	if err := CheckToolNames(ok); err != nil {
		t.Fatalf("合规的工具名不该报错：%v", err)
	}
	bad := []ToolSpec{{Name: "fs.read"}, {Name: "skill.load"}, {Name: "带中文"}, {Name: ""}, {Name: strings.Repeat("x", 65)}}
	for _, spec := range bad {
		if err := CheckToolNames([]ToolSpec{spec}); err == nil {
			t.Fatalf("不合规的工具名 %q 必须报错", spec.Name)
		} else if !strings.Contains(err.Error(), spec.Name) || !strings.Contains(err.Error(), "a-zA-Z0-9_-") {
			t.Fatalf("错误信息要说清是哪个工具名、该怎么改：%v", err)
		}
	}
	if !ValidToolName("fs_write") || ValidToolName("fs.write") {
		t.Fatal("ValidToolName 判定不对")
	}
}
