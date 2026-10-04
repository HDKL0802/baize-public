package agentsvc

import (
	"context"
	"strings"
	"testing"

	"baize/internal/config"
)

// 模型通道管理：加/改/删/探测，以及"key 留空不改动"和"远端地址必须先放行"两条约定
func TestProviderManagement(t *testing.T) {
	f := newFakeLLM(t, func(map[string]any) map[string]any { return sayBody("能通") })
	s := newService(t, nil, nil)

	base := config.Provider{
		Name: "local-fake", Protocol: "openai", BaseURL: f.srv.URL + "/v1",
		Model: "fake-model", APIKey: "sk-first",
	}
	list, err := s.ProviderSave(base, false)
	if err != nil {
		t.Fatalf("保存本地通道失败：%v", err)
	}
	if len(list) != 1 || !list[0].Usable || !list[0].Local || !list[0].HasAPIKey {
		t.Fatalf("通道状态不对：%+v", list)
	}
	// 配置要真落盘
	cfg, err := config.Load(s.DataDir())
	if err != nil || len(cfg.Providers) != 1 || cfg.Providers[0].APIKey != "sk-first" {
		t.Fatalf("配置没落盘或 key 丢了：%v %+v", err, cfg.Providers)
	}

	// key 留空 = 只改其他字段，不能把已有 key 清掉
	updated := base
	updated.APIKey = ""
	updated.Model = "fake-model-2"
	list, err = s.ProviderSave(updated, false)
	if err != nil {
		t.Fatalf("更新通道失败：%v", err)
	}
	if len(list) != 1 || list[0].Model != "fake-model-2" || !list[0].HasAPIKey {
		t.Fatalf("更新后应保留 key：%+v", list)
	}

	// 探测：走真实调用路径（这里打到本地假模型，不花任何额度）
	res, err := s.ProviderTest(context.Background(), "local-fake")
	if err != nil {
		t.Fatalf("探测失败：%v", err)
	}
	if res.Reply != "能通" || res.TotalTokens == 0 {
		t.Fatalf("探测结果不对：%+v", res)
	}
	if _, err := s.ProviderTest(context.Background(), "nope"); err == nil {
		t.Fatal("探测不存在的通道必须报错")
	}

	// 远端地址默认拒绝，要说清怎么办
	remote := config.Provider{Name: "cloud", Protocol: "openai", BaseURL: "https://api.deepseek.com/v1", Model: "deepseek-chat"}
	if _, err := s.ProviderSave(remote, false); err == nil {
		t.Fatal("远端地址未放行时必须报错")
	} else if !strings.Contains(err.Error(), "allow-remote") && !strings.Contains(err.Error(), "allowRemote") {
		t.Fatalf("错误信息要告诉用户怎么放行：%v", err)
	}
	list, err = s.ProviderSave(remote, true)
	if err != nil {
		t.Fatalf("显式放行后应该能保存：%v", err)
	}
	if len(list) != 2 || !s.Config().AllowRemote {
		t.Fatalf("放行状态没生效：%+v", list)
	}

	// 必填项校验
	bad := []config.Provider{
		{Name: "", BaseURL: "http://127.0.0.1:1/v1", Model: "m"},
		{Name: "x", BaseURL: "", Model: "m"},
		{Name: "x", BaseURL: "http://127.0.0.1:1/v1", Model: ""},
		{Name: "x", Protocol: "grpc", BaseURL: "http://127.0.0.1:1/v1", Model: "m"},
	}
	for _, b := range bad {
		if _, err := s.ProviderSave(b, true); err == nil {
			t.Fatalf("坏配置必须报错：%+v", b)
		}
	}

	// 删除
	list, err = s.ProviderRemove("cloud")
	if err != nil || len(list) != 1 {
		t.Fatalf("删除失败：%v %+v", err, list)
	}
	if _, err := s.ProviderRemove("cloud"); err == nil {
		t.Fatal("删不存在的通道必须报错")
	}
	if _, err := s.ProviderRemove(""); err == nil {
		t.Fatal("不给 name 必须报错")
	}
}
