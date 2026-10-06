package httpapi

import (
	"errors"
	"net/http"

	"baize/internal/observe"
)

// registerObservability 注册「可观测性」观测面接口。
//
// 它是一堆现读现算的汇总（runs.db 的运行统计 + 进程内的工具/通道计数 + 日志环 + 健康检查），
// 所以**一次请求就把界面要的都带走**，前端不用打十几个接口再自己拼。
func (s *Server) registerObservability(mux *http.ServeMux) {
	if s.agent == nil {
		return
	}
	mux.HandleFunc("GET /api/agent/observability", s.api(func(w http.ResponseWriter, r *http.Request) {
		rep, err := s.agent.Observability(r.URL.Query().Get("window"))
		if err != nil {
			// 档位写错是调用方的问题（400）；其余是真出错了（500）—— 别混成一种
			if errors.Is(err, observe.ErrBadWindow) {
				writeErr(w, http.StatusBadRequest, err.Error())
				return
			}
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, rep)
	}))
}
