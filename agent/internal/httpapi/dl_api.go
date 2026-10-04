package httpapi

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// registerDL 注册桌面端升级包的下载路由。
//
// 为什么放后端：桌面端要"自己给自己升级"，得有个稳定的分发点。NAS 本来就是唯一
// 中枢，把包放它的 <dataDir>/dl/ 下最省事（`tools/release-desktop.ps1` 负责上传）。
//
// 两道闸门：
//  1. 走 s.api —— 要配对令牌，跟其它接口一个口径（桌面端是从本机代理带着令牌来取的）；
//  2. 文件名白名单式校验 —— 只允许 dl 目录下的普通文件，杜绝 ../ 之类的路径穿越。
func (s *Server) registerDL(mux *http.ServeMux) {
	if s.agent == nil {
		return
	}
	dir := filepath.Join(s.agent.DataDir(), "dl")

	mux.HandleFunc("GET /dl/{name}", s.api(func(w http.ResponseWriter, r *http.Request) {
		name := r.PathValue("name")
		if name == "" || strings.HasPrefix(name, ".") ||
			strings.ContainsAny(name, `/\`) || strings.Contains(name, "..") {
			writeErr(w, http.StatusBadRequest, "非法文件名")
			return
		}
		full := filepath.Join(dir, name)
		if filepath.Dir(full) != filepath.Clean(dir) { // 再保险一次
			writeErr(w, http.StatusBadRequest, "非法路径")
			return
		}
		fi, err := os.Stat(full)
		if err != nil || fi.IsDir() {
			writeErr(w, http.StatusNotFound, "没有这个文件："+name)
			return
		}
		http.ServeFile(w, r, full)
	}))
}
