package core

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// 本文件是「正本在后端」这一侧的实现：手机内核在跨端模式下不再持有正本，
// 读写都交给 NAS 上的白泽知识库（后端 /api/kb/state 与 /api/kb/op）。
//
// 两条约定：
//  1. 连不上后端就明确报错，绝不伪装成功（写操作尤其不能悄悄丢）；
//  2. 密码本不做本地缓存 —— 用户要求「手机上不留文件」，密码这一类更不该在手机上多留一份。

// Remote 后端知识库客户端
type Remote struct {
	server string
	token  string
	hc     *http.Client
}

// NewRemote 创建客户端（server 形如 http://192.168.1.100:8787）
func NewRemote(server, token string) *Remote {
	return &Remote{
		server: strings.TrimRight(strings.TrimSpace(server), "/"),
		token:  strings.TrimSpace(token),
		hc:     &http.Client{Timeout: 15 * time.Second},
	}
}

// Server 后端地址
func (r *Remote) Server() string { return r.server }

// do 发一次请求，把后端的 {ok,data} / {error} 解开
func (r *Remote) do(method, path string, body any) (json.RawMessage, error) {
	if r.server == "" {
		return nil, errors.New("没有配置后端地址")
	}
	var rdr io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("组装请求失败：%w", err)
		}
		rdr = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, r.server+path, rdr)
	if err != nil {
		return nil, fmt.Errorf("组装请求失败：%w", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json; charset=utf-8")
	}
	if r.token != "" {
		req.Header.Set("X-Baize-Token", r.token)
	}
	resp, err := r.hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("连不上后端知识库（%s）：%w", r.server, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, fmt.Errorf("读取后端响应失败：%w", err)
	}
	switch {
	case resp.StatusCode == http.StatusUnauthorized:
		return nil, errors.New("后端拒绝了请求：配对令牌不对（去「设置 → 跨端」重填后端令牌）")
	case resp.StatusCode == http.StatusNotFound:
		return nil, fmt.Errorf("后端没有知识库接口（%s/api/kb/state），可能后端版本太旧，升级后端即可", r.server)
	case resp.StatusCode >= 400:
		var e struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(raw, &e) == nil && e.Error != "" {
			return nil, errors.New(e.Error)
		}
		return nil, fmt.Errorf("后端返回 HTTP %d", resp.StatusCode)
	}
	return raw, nil
}

// State 取后端知识库快照
func (r *Remote) State() (Snapshot, error) {
	raw, err := r.do(http.MethodGet, "/api/kb/state", nil)
	if err != nil {
		return Snapshot{}, err
	}
	snap := Snapshot{}
	if err := json.Unmarshal(raw, &snap); err != nil {
		return Snapshot{}, fmt.Errorf("后端知识库快照解析失败：%w", err)
	}
	return snap, nil
}

// Do 在后端执行一条操作，返回操作结果（与本地 Do 的形状一致）
func (r *Remote) Do(op string, args json.RawMessage) (any, error) {
	raw, err := r.do(http.MethodPost, "/api/kb/op", map[string]any{"op": op, "args": args})
	if err != nil {
		return nil, err
	}
	var res struct {
		OK   bool            `json:"ok"`
		Data json.RawMessage `json:"data"`
		Err  string          `json:"error"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		return nil, fmt.Errorf("后端返回的内容看不懂：%w", err)
	}
	if !res.OK {
		if res.Err == "" {
			res.Err = "后端执行失败（没有给出原因）"
		}
		return nil, errors.New(res.Err)
	}
	if len(res.Data) == 0 {
		return nil, nil
	}
	var out any
	if err := json.Unmarshal(res.Data, &out); err != nil {
		return nil, nil
	}
	return out, nil
}

// Agent 把一次请求转给后端（App 的白泽页与附件读写都走这条路）。
// path 形如 /api/agent/run 或 /api/kb/files，method 与 body 原样转发。
// 附件走 base64，所以这里的读取上限比其它接口宽（16MB 的文件 → base64 约 21MB）。
func (r *Remote) Agent(method, path string, body []byte, contentType string) ([]byte, int, error) {
	if r.server == "" {
		return nil, 0, errors.New("没有配置后端地址")
	}
	var rdr io.Reader
	if len(body) > 0 {
		rdr = bytes.NewReader(body)
	}
	req, err := http.NewRequest(method, r.server+path, rdr)
	if err != nil {
		return nil, 0, err
	}
	if len(body) > 0 {
		if contentType == "" {
			contentType = "application/json; charset=utf-8"
		}
		req.Header.Set("Content-Type", contentType)
	}
	if r.token != "" {
		req.Header.Set("X-Baize-Token", r.token)
	}
	resp, err := r.hc.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("连不上白泽后端（%s）：%w", r.server, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 40<<20))
	if err != nil {
		return nil, 0, err
	}
	return raw, resp.StatusCode, nil
}
