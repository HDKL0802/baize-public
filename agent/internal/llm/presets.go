package llm

// Preset 预置的模型通道模板（界面「从模板新建」用）。
//
// 这里放的是**广为人知、且能公开核实的**接入点与示例模型名；模型名只是示例，按需改。
// 刻意不收录那些「端点/协议说法不一」的服务（见 Agnes 那条的 Note），
// 也不替任何服务宣称"免费/不限量"——成本一栏只写能核实的事实口径。
type Preset struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Protocol string `json:"protocol"` // openai | anthropic
	BaseURL  string `json:"baseUrl"`
	Model    string `json:"model"`  // 示例模型名（可留空 = 由你填）
	Region   string `json:"region"` // local | cn | intl
	Cost     string `json:"cost"`   // 成本标注（人话）
	Privacy  string `json:"privacy"`
	NeedsKey bool   `json:"needsKey"`
	DocURL   string `json:"docUrl,omitempty"`
	Note     string `json:"note,omitempty"`
}

// Presets 预置模板清单（顺序即界面显示顺序）
func Presets() []Preset {
	return []Preset{
		/* ---- 本地（默认基调：本地优先，不悄悄上云） ---- */
		{ID: "ollama", Name: "Ollama（本地）", Protocol: "openai", BaseURL: "http://127.0.0.1:11434/v1",
			Model: "qwen2.5:7b", Region: "local", Cost: "免费（吃本机算力）", Privacy: "本地，不出机器",
			DocURL: "https://ollama.com", Note: "模型名用 `ollama list` 里那个（如 qwen2.5:7b）。"},
		{ID: "lmstudio", Name: "LM Studio（本地）", Protocol: "openai", BaseURL: "http://127.0.0.1:1234/v1",
			Model: "", Region: "local", Cost: "免费（吃本机算力）", Privacy: "本地，不出机器",
			DocURL: "https://lmstudio.ai", Note: "在 LM Studio 里启动本地服务器；模型名填你加载的那个。"},
		{ID: "vllm", Name: "vLLM（本地）", Protocol: "openai", BaseURL: "http://127.0.0.1:8000/v1",
			Model: "", Region: "local", Cost: "免费（吃本机算力）", Privacy: "本地，不出机器",
			DocURL: "https://docs.vllm.ai", Note: "vLLM 默认 OpenAI 兼容端口 8000/v1；模型名填你起的服务名。"},
		{ID: "llamacpp", Name: "llama.cpp server（本地）", Protocol: "openai", BaseURL: "http://127.0.0.1:8080/v1",
			Model: "", Region: "local", Cost: "免费（吃本机算力）", Privacy: "本地，不出机器",
			DocURL: "https://github.com/ggml-org/llama.cpp", Note: "llama.cpp 的 `llama-server` 自带 OpenAI 兼容接口。"},

		/* ---- 云端·国内 ---- */
		{ID: "deepseek", Name: "DeepSeek（国内）", Protocol: "openai", BaseURL: "https://api.deepseek.com/v1",
			Model: "deepseek-chat", Region: "cn", Cost: "按量计费（便宜）", Privacy: "公网云端", NeedsKey: true,
			DocURL: "https://platform.deepseek.com", Note: "deepseek-chat = 通用对话；deepseek-reasoner = 推理。"},
		{ID: "dashscope", Name: "阿里百炼 / 通义（国内）", Protocol: "openai",
			BaseURL: "https://dashscope.aliyuncs.com/compatible-mode/v1", Model: "qwen-plus",
			Region: "cn", Cost: "按量计费", Privacy: "公网云端", NeedsKey: true,
			DocURL: "https://help.aliyun.com/zh/model-studio/", Note: "OpenAI 兼容模式路径 /compatible-mode/v1。"},
		{ID: "dashscope-intl", Name: "阿里百炼 / 通义（国际·新加坡）", Protocol: "openai",
			BaseURL: "https://dashscope-intl.aliyuncs.com/compatible-mode/v1", Model: "qwen-plus",
			Region: "intl", Cost: "按量计费", Privacy: "公网云端", NeedsKey: true,
			DocURL: "https://help.aliyun.com/zh/model-studio/", Note: "国际站（新加坡）；Key 与国内站不通用。"},
		{ID: "zhipu", Name: "智谱 GLM（国内）", Protocol: "openai",
			BaseURL: "https://open.bigmodel.cn/api/paas/v4", Model: "glm-4-plus",
			Region: "cn", Cost: "按量计费", Privacy: "公网云端", NeedsKey: true,
			DocURL: "https://open.bigmodel.cn", Note: "GLM 的 OpenAI 兼容端点。"},
		{ID: "moonshot", Name: "Moonshot / Kimi（国内）", Protocol: "openai",
			BaseURL: "https://api.moonshot.cn/v1", Model: "moonshot-v1-8k",
			Region: "cn", Cost: "按量计费", Privacy: "公网云端", NeedsKey: true,
			DocURL: "https://platform.moonshot.cn", Note: ""},
		{ID: "siliconflow", Name: "硅基流动 SiliconFlow（国内）", Protocol: "openai",
			BaseURL: "https://api.siliconflow.cn/v1", Model: "deepseek-ai/DeepSeek-V3",
			Region: "cn", Cost: "按量计费", Privacy: "公网云端", NeedsKey: true,
			DocURL: "https://siliconflow.cn", Note: "聚合站：模型名用带斜杠的全名。"},
		{ID: "hunyuan", Name: "腾讯混元（国内）", Protocol: "openai",
			BaseURL: "https://api.hunyuan.cloud.tencent.com/v1", Model: "hunyuan-turbo",
			Region: "cn", Cost: "按量计费", Privacy: "公网云端", NeedsKey: true,
			DocURL: "https://cloud.tencent.com/product/hunyuan", Note: ""},

		/* ---- 云端·国际 ---- */
		{ID: "openai", Name: "OpenAI（国际）", Protocol: "openai", BaseURL: "https://api.openai.com/v1",
			Model: "gpt-4o-mini", Region: "intl", Cost: "按量计费", Privacy: "公网云端", NeedsKey: true,
			DocURL: "https://platform.openai.com", Note: "国内直连通常不通，需自备网络。"},
		{ID: "anthropic", Name: "Anthropic Claude（国际）", Protocol: "anthropic",
			BaseURL: "https://api.anthropic.com", Model: "claude-3-5-sonnet-latest",
			Region: "intl", Cost: "按量计费", Privacy: "公网云端", NeedsKey: true,
			DocURL: "https://docs.anthropic.com", Note: "走 anthropic 协议（不是 openai 兼容）。"},
		{ID: "openrouter", Name: "OpenRouter（国际聚合）", Protocol: "openai",
			BaseURL: "https://openrouter.ai/api/v1", Model: "openai/gpt-4o-mini",
			Region: "intl", Cost: "按量计费（可挑便宜模型）", Privacy: "公网云端", NeedsKey: true,
			DocURL: "https://openrouter.ai", Note: "聚合站：模型名用 provider/model 全名。"},
		{ID: "gemini", Name: "Google Gemini（OpenAI 兼容）", Protocol: "openai",
			BaseURL: "https://generativelanguage.googleapis.com/v1beta/openai/", Model: "gemini-1.5-flash",
			Region: "intl", Cost: "有免费额度 / 按量计费", Privacy: "公网云端", NeedsKey: true,
			DocURL: "https://ai.google.dev", Note: "用 Google 的 OpenAI 兼容入口。"},

		/* ---- 需自行确认端点（不替它编地址） ---- */
		{ID: "agnes", Name: "Agnes AI（端点自行确认）", Protocol: "openai", BaseURL: "",
			Model: "", Region: "cn", Cost: "自称免费（以 agnes-ai.com 为准）", Privacy: "公网云端", NeedsKey: true,
			DocURL: "https://agnes-ai.com",
			Note:   "⚠️ Agnes AI 的接口**与 OpenAI 不完全兼容**（公开资料如此），直连可能不通；端点与模型名请以 agnes-ai.com 控制台为准自行填入，必要时先做一层协议转换。"},
	}
}
