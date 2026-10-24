package llm

// Preset 预置的模型通道模板（界面「从模板新建」用）。
//
// 这里放的是**广为人知、且能公开核实的**接入点与示例模型名；模型名只是示例，按需改。
// 口径（2026-10-24 定）：
//   - 同一厂商**国内站 / 国际站**全部分开两条（Key 通常不通用）；
//   - 同一厂商**按量付费 / 套餐（Coding Plan、Token Plan、Step Plan、Agent Plan）**也全部分开
//     （例：火山方舟 = API 按量付费 + Coding Plan 两条）；
//   - 刻意不收录 OpenAI / Anthropic / Google Gemini / xAI Grok 四家（国内不可用）；
//   - 不替任何服务宣称"免费/不限量"——成本一栏只写能核实的事实口径。
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

		/* ---- 云端·国内（按量付费 / 套餐 / 国内国际站全部分开） ---- */
		// DeepSeek（官方直连，只有一条）
		{ID: "deepseek", Name: "DeepSeek（国内）", Protocol: "openai", BaseURL: "https://api.deepseek.com/v1",
			Model: "deepseek-v4-flash", Region: "cn", Cost: "按量计费（便宜）", Privacy: "公网云端", NeedsKey: true,
			DocURL: "https://platform.deepseek.com", Note: "deepseek-v4-flash = 轻量主力；deepseek-v4-pro = 重活。"},
		// 火山方舟（字节）：分两档 —— API 按量付费 / Coding Plan（套餐），端点不同
		{ID: "volcark-api", Name: "火山方舟 · API 按量付费", Protocol: "openai",
			BaseURL: "https://ark.cn-beijing.volces.com/api/v3", Model: "",
			Region: "cn", Cost: "按量付费", Privacy: "公网云端", NeedsKey: true,
			DocURL: "https://www.volcengine.com/product/ark",
			Note: "模型名以方舟控制台为准（豆包 doubao-seed 系列等）。⚠️ Coding Plan 用户误用本端点会产生额外费用，套餐请走下面那条。"},
		{ID: "volcark-coding", Name: "火山方舟 · Coding Plan（套餐）", Protocol: "openai",
			BaseURL: "https://ark.cn-beijing.volces.com/api/coding/v3", Model: "",
			Region: "cn", Cost: "套餐（Coding Plan，月付）", Privacy: "公网云端", NeedsKey: true,
			DocURL: "https://www.volcengine.com/product/ark",
			Note: "Coding Plan 专属端点（兼容 OpenAI 协议）。Claude Code 类工具走 Anthropic 协议时 BaseURL 换成 /api/coding（protocol 选 anthropic）。"},
		// 智谱 GLM：按量付费 / Coding Plan（套餐）两条
		{ID: "zhipu", Name: "智谱 GLM · API 按量付费（国内）", Protocol: "openai",
			BaseURL: "https://open.bigmodel.cn/api/paas/v4", Model: "glm-5.2",
			Region: "cn", Cost: "按量计费", Privacy: "公网云端", NeedsKey: true,
			DocURL: "https://open.bigmodel.cn", Note: "GLM 的 OpenAI 兼容端点；模型名以控制台为准（glm-5.2 / glm-4.5 等）。"},
		{ID: "zhipu-coding", Name: "智谱 GLM · Coding Plan（套餐）", Protocol: "openai",
			BaseURL: "https://open.bigmodel.cn/api/coding/paas/v4", Model: "glm-5.3",
			Region: "cn", Cost: "套餐（GLM Coding Plan，月付）", Privacy: "公网云端", NeedsKey: true,
			DocURL: "https://open.bigmodel.cn",
			Note: "Coding Plan 套餐专属端点；Claude Code 类工具走 Anthropic 协议时 BaseURL 换成 /api/anthropic。"},
		// 智谱国际版（Z.ai，海外端点）
		{ID: "zhipu-intl", Name: "智谱 GLM · 国际版（Z.ai）", Protocol: "openai",
			BaseURL: "https://api.z.ai/api/paas/v4", Model: "glm-4.5",
			Region: "intl", Cost: "按量计费（海外定价）", Privacy: "公网云端", NeedsKey: true,
			DocURL: "https://z.ai", Note: "Z.ai = 智谱国际版；Key 与国内站不通用。"},
		// 月之暗面 Kimi：国内 / 国际两条
		{ID: "moonshot", Name: "Kimi / Moonshot（国内）", Protocol: "openai",
			BaseURL: "https://api.moonshot.cn/v1", Model: "kimi-k2",
			Region: "cn", Cost: "按量计费", Privacy: "公网云端", NeedsKey: true,
			DocURL: "https://platform.moonshot.cn", Note: ""},
		{ID: "moonshot-intl", Name: "Kimi / Moonshot（国际版）", Protocol: "openai",
			BaseURL: "https://api.moonshot.ai/v1", Model: "kimi-k2",
			Region: "intl", Cost: "按量计费（海外定价）", Privacy: "公网云端", NeedsKey: true,
			DocURL: "https://platform.moonshot.ai", Note: "国际版端点 .ai；Key 与国内站不通用。"},
		// 阿里百炼（通义千问）：国内按量 / 国际按量 / Coding Plan（百炼 Token Plan）三条
		{ID: "dashscope", Name: "阿里百炼 · API 按量付费（国内）", Protocol: "openai",
			BaseURL: "https://dashscope.aliyuncs.com/compatible-mode/v1", Model: "qwen-plus",
			Region: "cn", Cost: "按量计费", Privacy: "公网云端", NeedsKey: true,
			DocURL: "https://help.aliyun.com/zh/model-studio/", Note: "OpenAI 兼容模式路径 /compatible-mode/v1。"},
		{ID: "dashscope-intl", Name: "阿里百炼 · 国际（模型方舟/新加坡）", Protocol: "openai",
			BaseURL: "https://dashscope-intl.aliyuncs.com/compatible-mode/v1", Model: "qwen-plus",
			Region: "intl", Cost: "按量计费（海外定价）", Privacy: "公网云端", NeedsKey: true,
			DocURL: "https://www.alibabacloud.com/product/model-studio", Note: "国际站（新加坡）；Key 与国内站不通用。"},
		{ID: "dashscope-coding", Name: "阿里百炼 · Coding Plan（套餐）", Protocol: "openai",
			BaseURL: "https://coding.dashscope.aliyuncs.com/v1", Model: "",
			Region: "cn", Cost: "套餐（Coding/Token Plan，月付）", Privacy: "公网云端", NeedsKey: true,
			DocURL: "https://help.aliyun.com/zh/model-studio/",
			Note: "百炼 Coding Plan 专属端点；套餐 Key（sk-sp- 开头）与按量 Key（sk- 开头）不通用。"},
		// 小米 MiMo（新厂商）
		{ID: "mimo", Name: "小米 MiMo（国内）", Protocol: "openai",
			BaseURL: "https://api.xiaomimimo.com/v1", Model: "",
			Region: "cn", Cost: "按量计费", Privacy: "公网云端", NeedsKey: true,
			DocURL: "https://mimo.mi.com",
			Note: "模型名以控制台为准（MiMo-V2.6-Pro / V2.6-Flash / V2-Omni 多模态）。"},
		// MiniMax：国内 / 国际两条（双版本都加）
		{ID: "minimax", Name: "MiniMax（国内）", Protocol: "openai",
			BaseURL: "https://api.minimaxi.com/v1", Model: "",
			Region: "cn", Cost: "按量计费", Privacy: "公网云端", NeedsKey: true,
			DocURL: "https://platform.minimaxi.com", Note: "国内版；模型名以控制台为准（MiniMax-M3 / M2.7 / abab 系列）。"},
		{ID: "minimax-intl", Name: "MiniMax（国际）", Protocol: "openai",
			BaseURL: "https://api.minimax.io/v1", Model: "",
			Region: "intl", Cost: "按量计费（海外定价）", Privacy: "公网云端", NeedsKey: true,
			DocURL: "https://www.minimax.io", Note: "国际版；Key 与国内版不通用。"},
		// 阶跃星辰 StepFun（≠MiniMax，独立厂商）
		{ID: "stepfun", Name: "阶跃星辰 StepFun（国内）", Protocol: "openai",
			BaseURL: "https://api.stepfun.com/v1", Model: "",
			Region: "cn", Cost: "按量计费", Privacy: "公网云端", NeedsKey: true,
			DocURL: "https://platform.stepfun.com", Note: "模型名以控制台为准（Step-3.5-Flash / Step-2-Mini 等）。"},
		// 百度智能云千帆
		{ID: "baidu-qianfan", Name: "百度智能云千帆（国内）", Protocol: "openai",
			BaseURL: "https://qianfan.baidubce.com/v2", Model: "",
			Region: "cn", Cost: "按量计费", Privacy: "公网云端", NeedsKey: true,
			DocURL: "https://cloud.baidu.com/product/qianfan", Note: "ERNIE 文心系列；OpenAI 兼容路径 /v2。"},
		// 腾讯混元（国内）
		{ID: "hunyuan", Name: "腾讯混元（国内）", Protocol: "openai",
			BaseURL: "https://api.hunyuan.cloud.tencent.com/v1", Model: "hunyuan-turbo",
			Region: "cn", Cost: "按量计费", Privacy: "公网云端", NeedsKey: true,
			DocURL: "https://cloud.tencent.com/product/hunyuan", Note: ""},
		// 腾讯云国际版（海外端点）
		{ID: "hunyuan-intl", Name: "腾讯混元 · 国际版", Protocol: "openai",
			BaseURL: "https://api.hunyuan.cloud.tencent.com/v1", Model: "hunyuan-turbo",
			Region: "intl", Cost: "按量计费（海外定价）", Privacy: "公网云端", NeedsKey: true,
			DocURL: "https://intl.cloud.tencent.com",
			Note: "国际账号走 intl 控制台；端点同但 Key 体系独立。"},
		// 硅基流动
		{ID: "siliconflow", Name: "硅基流动 SiliconFlow（国内）", Protocol: "openai",
			BaseURL: "https://api.siliconflow.cn/v1", Model: "deepseek-ai/DeepSeek-V3",
			Region: "cn", Cost: "按量计费", Privacy: "公网云端", NeedsKey: true,
			DocURL: "https://siliconflow.cn", Note: "聚合站：模型名用带斜杠的全名。"},
		// 魔搭社区 ModelScope（开源模型托管）
		{ID: "modelscope", Name: "魔搭社区 ModelScope（国内）", Protocol: "openai",
			BaseURL: "https://modelscope.cn", Model: "",
			Region: "cn", Cost: "按量计费 / 免费（开源模型）", Privacy: "公网云端", NeedsKey: true,
			DocURL: "https://modelscope.cn",
			Note: "开源模型聚合托管；具体模型接入端点以 ModelScope 控制台为准。"},
		// UCloud 优刻得
		{ID: "ucloud", Name: "UCloud 优刻得（国内）", Protocol: "openai",
			BaseURL: "https://api.ucloud.cn", Model: "",
			Region: "cn", Cost: "按量计费", Privacy: "公网云端", NeedsKey: true,
			DocURL: "https://www.ucloud.cn",
			Note: "AI 算力/模型托管；具体模型接入端点以优刻得控制台为准。"},

		/* ---- 云端·国际 / 海外可用平台（避开 OpenAI/Anthropic/Gemini/Grok 四家） ---- */
		{ID: "openrouter", Name: "OpenRouter（国际聚合）", Protocol: "openai",
			BaseURL: "https://openrouter.ai/api/v1", Model: "",
			Region: "intl", Cost: "按量计费（可挑便宜模型）", Privacy: "公网云端", NeedsKey: true,
			DocURL: "https://openrouter.ai",
			Note: "聚合站：模型名用 provider/model 全名；选模型时避开 OpenAI/Anthropic/Gemini/Grok 系列，其余可用。"},
		{ID: "aws", Name: "AWS 亚马逊（Bedrock/Nova）", Protocol: "openai",
			BaseURL: "", Model: "",
			Region: "intl", Cost: "按量计费（海外定价）", Privacy: "公网云端", NeedsKey: true,
			DocURL: "https://aws.amazon.com/bedrock/",
			Note: "Bedrock 托管模型 / Amazon Nova 自研；端点按 AWS 区域填（OpenAI 兼容模式）。"},
		// BytePlus 字节海外版（ModelArk 模型方舟，与火山方舟同源但独立 Key 体系）
		{ID: "byteplus-modelark", Name: "BytePlus · 模型方舟 ModelArk（字节海外）", Protocol: "openai",
			BaseURL: "https://ark-intl.bytepluses.com/api/v3", Model: "",
			Region: "intl", Cost: "按量计费（海外定价）", Privacy: "公网云端", NeedsKey: true,
			DocURL: "https://www.byteplus.com",
			Note: "字节海外版 ModelArk，与火山方舟（国内）Key 不通用。"},

		/* ---- Agnes AI（双版本：国内 .cn / 国际 .com；默认主力 = 国内 agnes-3.0-flash） ---- */
		{ID: "agnes", Name: "Agnes AI · 国内（默认主力）", Protocol: "openai", BaseURL: "https://api.agnes-ai.cn/v1",
			Model: "agnes-3.0-flash", Region: "cn", Cost: "以 agnes-ai.cn 为准（主力选它，便宜够用）",
			Privacy: "公网云端", NeedsKey: true,
			DocURL: "https://wiki.agnes-ai.cn",
			Note: "主力编码模型 = agnes-3.0-flash（Sapiens AI，新加坡）。平台另有文生图/图生图/视频模型（多模态 Provider 另配）。"},
		{ID: "agnes-intl", Name: "Agnes AI · 国际", Protocol: "openai", BaseURL: "https://apihub.agnes-ai.com/v1",
			Model: "agnes-3.0-flash", Region: "intl", Cost: "以 agnes-ai.com 为准", Privacy: "公网云端", NeedsKey: true,
			DocURL: "https://wiki.agnes-ai.com",
			Note:   "国际站；一般走国内那条即可，这条留给国际站。"},
	}
}
