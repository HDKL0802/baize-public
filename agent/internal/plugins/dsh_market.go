package plugins

import (
	_ "embed"
	"errors"
	"fmt"
	"strings"
)

// DSH 生态市场（内置）：桌面端开箱自带的 DSH 生态插件目录。
//
// 与官方内置源一样随二进制分发、默认启用、不写进 config.json，换数据目录也不会失效。
// 但**只提供货架信息**：DSH 生态插件是 npm / cordis 运行时形态，白泽装不了，
// 所以 readDshMarket 对包地址一律返回明确错误，绝不伪造一个「看起来能装」的 zip
// —— 用户最恨看起来能用其实不能用。
const (
	DshMarketSourceURL  = "builtin:dsh-market"
	DshMarketSourceName = "DSH 生态市场（内置）"
	// dshMarketPkgPrefix 包内约定的包地址前缀：builtin:dsh-market/<id>.zip
	dshMarketPkgPrefix = "builtin:dsh-market/"
)

// DshNotInstallableMsg 是请求 builtin:dsh-market/<id>.zip 时返回的明确错误（文案逐字固定）。
const DshNotInstallableMsg = "这是 DSH 生态插件（npm / cordis 运行时形态），白泽不能直接安装。请用 POST /api/agent/ecology/dsh/sync 把所需插件转换成白泽插件源后再安装。"

// dshMarketIndex 是生成器从 dsh-index.snapshot.json 转出来的紧凑白泽格式 index.json。
// 只 embed 这一份（约 4.9MB），绝不 embed 8.4MB 的原始快照（快照仅作来源凭证留在仓库里）。
//
//go:embed ecosystem/dsh-market.index.json
var dshMarketIndex []byte

// DshMarketIndexBytes 返回内嵌索引字节的副本（只读，供测试核对条数/元数据用）。
func DshMarketIndexBytes() []byte { return append([]byte(nil), dshMarketIndex...) }

// isDshMarketRef 判断 ref 是不是 DSH 内置市场的索引地址或包地址。
// 用「等于索引地址 或 带包前缀」而不是裸 HasPrefix("builtin:dsh-market")，
// 免得把 builtin:dsh-market-whatever 之类的陌生地址也算进来。
func isDshMarketRef(ref string) bool {
	r := strings.TrimSpace(ref)
	return r == DshMarketSourceURL || strings.HasPrefix(r, dshMarketPkgPrefix)
}

// readDshMarket 读一个 DSH 内置市场地址，返回 (内容, 最终地址, 错误)：
//   - 索引地址 builtin:dsh-market → 内嵌 index.json 的字节；
//   - 包地址 builtin:dsh-market/<id>.zip → 明确错误，绝不返回假 zip 字节；
//   - 其它不认识的 builtin:dsh-market 地址 → 也明确报错。
func readDshMarket(ref string) ([]byte, string, error) {
	r := strings.TrimSpace(ref)
	switch {
	case r == DshMarketSourceURL || r == strings.TrimSuffix(DshMarketSourceURL, "/"):
		return dshMarketIndex, r, nil
	case strings.HasPrefix(r, dshMarketPkgPrefix):
		return nil, r, errors.New(DshNotInstallableMsg)
	default:
		return nil, r, fmt.Errorf("不认识的 DSH 内置市场地址：%s（可用的是 %s）", r, DshMarketSourceURL)
	}
}
