package httpapi

import (
	"embed"
	"io/fs"
	"net/http"
	"strings"
)

// WebUI 分「电脑版 / 手机版」两套，按 UA 分流（见 panelVariant）：
//   - 电脑版 = 老的 panel.html（单个自包含文件，注入令牌后直接发）；
//   - 手机版 = webui/ 下这一套（从仓库根的 todo-app/ 移植过来的手机前端）。
//
// 手机版为什么是整套静态资源：它就是手机 App 的前端，天生多文件分模块；
// 而 go:embed 只能嵌**本包目录以内**的东西，所以这儿放了一份 webui/。
// 与手机端的差异只有一处、写在 webui/js/webui-bridge.js 里：把「本机内核桥」
// 换成「直接打后端」，因为浏览器里没有 libbzcore。
//
//go:embed webui
var webuiFS embed.FS

// webuiSub 是 webui/ 这一层（/js、/css 这些资源路由从它取文件）
var webuiSub = mustSubFS(webuiFS, "webui")

// mobileIndexHTML 手机版首页：含 __BAIZE_TOKEN_ATTR__ 占位符，发的时候替换成真令牌
var mobileIndexHTML = mustReadFS(webuiSub, "index.html")

// mobileTokenPlaceholder 手机版首页里令牌的占位符。
// 特意跟电脑版的 __BAIZE_TOKEN__ 分开：电脑版那处是 JS 字符串字面量，手机版这处在 HTML 属性里。
// 若两处共用同一个字面量，替换会把 JS 里的变量名一起换掉 —— 实测直接搞成 `window. = "";` 的语法错误。
const mobileTokenPlaceholder = "__BAIZE_TOKEN_ATTR__"

// iconPNG 应用图标（就是桌面端 desktop/ui/icon.png 那张）：两套页面共用，
// 既做 favicon，也做电脑版侧栏的品牌标 —— 替掉原来那个「蓝底黑字泽字」的方标。
var iconPNG = mustReadFS(webuiSub, "icon.png")

func mustSubFS(f embed.FS, dir string) fs.FS {
	sub, err := fs.Sub(f, dir)
	if err != nil {
		panic("httpapi: embed 子目录 " + dir + " 取不到：" + err.Error())
	}
	return sub
}

func mustReadFS(f fs.FS, name string) []byte {
	b, err := fs.ReadFile(f, name)
	if err != nil {
		panic("httpapi: embed 文件 " + name + " 读不到：" + err.Error())
	}
	return b
}

// registerWebUI 注册手机版控制台的静态资源与图标。
//
// 首页本身不带前缀（走 GET /，见 handlePanel），资源才带前缀 —— 这样手机浏览器
// 打开 http://<后端>/ 时，页面里 `css/app.css`、`js/store.js` 这些相对路径才会落在
// /css/... 与 /js/... 上。这些资源不含任何密钥（令牌只注入在首页里），所以不设令牌闸门。
func (s *Server) registerWebUI(mux *http.ServeMux) {
	mux.HandleFunc("GET /css/", s.serveWebUIAsset)
	mux.HandleFunc("GET /js/", s.serveWebUIAsset)
	mux.HandleFunc("GET /manifest.webmanifest", s.serveWebUIAsset)
	mux.HandleFunc("GET /icon.png", s.serveIcon)
	mux.HandleFunc("GET /favicon.ico", s.serveIcon)
}

// serveWebUIAsset 发一份手机版静态资源。
// 路径一律过 fs.ValidPath（挡掉 ..、绝对路径、目录请求），只认 embed 里真有那份。
func (s *Server) serveWebUIAsset(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(r.URL.Path, "/")
	if name == "" || !fs.ValidPath(name) {
		http.NotFound(w, r)
		return
	}
	data, err := fs.ReadFile(webuiSub, name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", webuiMIME(name))
	// 前端随后端版本一起发：别让浏览器留旧的（历史上被无头 Edge 的缓存坑过）
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(data)
}

// serveIcon 发应用图标（favicon + 两套页面的品牌标都用它）
func (s *Server) serveIcon(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(iconPNG)
}

func webuiMIME(name string) string {
	switch {
	case strings.HasSuffix(name, ".js"):
		return "text/javascript; charset=utf-8"
	case strings.HasSuffix(name, ".css"):
		return "text/css; charset=utf-8"
	case strings.HasSuffix(name, ".html"):
		return "text/html; charset=utf-8"
	case strings.HasSuffix(name, ".webmanifest"):
		return "application/manifest+json; charset=utf-8"
	case strings.HasSuffix(name, ".png"):
		return "image/png"
	case strings.HasSuffix(name, ".svg"):
		return "image/svg+xml"
	default:
		return "application/octet-stream"
	}
}

/* ---------- 电脑版 / 手机版分流 ---------- */

// panelVariant 决定这次发哪套页面。
//   - 显式 ?ver=mobile|desktop 最优先（手机想用电脑版、电脑想核手机版都能切，顺手也方便自查）；
//   - 否则按 UA：手机 / 平板给手机版，其余给电脑版。
func panelVariant(r *http.Request) string {
	switch strings.ToLower(strings.TrimSpace(r.URL.Query().Get("ver"))) {
	case "mobile", "m", "phone", "h5":
		return "mobile"
	case "desktop", "d", "pc":
		return "desktop"
	}
	if isMobileUA(r.UserAgent()) {
		return "mobile"
	}
	return "desktop"
}

// isMobileUA 认手机 / 平板。mobile 一词能兜住绝大多数移动浏览器；
// 其余几个是各平台特有的标识（iPad 报的 UA 也带 mobile）。
func isMobileUA(ua string) bool {
	ua = strings.ToLower(ua)
	for _, k := range []string{"android", "iphone", "ipod", "ipad", "mobile", "harmonyos", "windows phone", "micromessenger"} {
		if strings.Contains(ua, k) {
			return true
		}
	}
	return false
}
