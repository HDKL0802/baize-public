package agentsvc

// 协商里的「回复AI」：两位协作者各点一次确认后，后端把整段上下文
// （几版同名文档 + 协商聊天 + 通话里的文字留言）交给模型判定——
// 要么保留某一版，要么合成一份——然后直接落库定稿、清掉分叉。
//
// 口径（不许含糊）：
//   - 判定走**第一条可用模型通道**（纯文本活，与翻译同口径）；没有可用通道就
//     如实回「判定失败」，不假装合并过。
//   - 模型只输出 JSON 契约（decision=keep|merge），解析不出契约同样算失败。
//   - 合并内容以双方聊天记录里达成的共识为准，没谈拢的各版内容都保留、不臆造
//     （这条写进提示词，由模型执行；后端只校验契约、不校验语义）。

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"baize/internal/accounts"
	"baize/internal/calls"
	"baize/internal/conflicts"
	"baize/internal/kb"
	"baize/internal/llm"
)

// AIJudgeDecision 模型必须回的结构（keep = 保留 docId 那一版；merge = mergedContent 是新正本）
type AIJudgeDecision struct {
	Decision      string `json:"decision"`
	DocID         string `json:"docId"`
	MergedContent string `json:"mergedContent"`
	Reason        string `json:"reason"`
}

// aiJudgeContext 攒上下文时的截断上限（防止超长文档把提示词撑爆）
const (
	aiJudgeDocCap   = 6000 // 每版文档正文最多带这么多字（超出截断并标注）
	aiJudgeMsgCap   = 800  // 每条聊天/通话留言最多带这么多字
	aiJudgeMsgLimit = 200  // 最多带多少条留言
)

// ConflictAIJudge 执行一次「回复AI」判定（同步跑完；调用方在 goroutine 里调）。
// 结果写回协商会话（aiStatus/aiNote + 一条系统留言）；失败也如实写回，不留空。
func (s *Service) ConflictAIJudge(groupID, sid string) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	pr := accounts.GroupPrincipal(groupID)
	cs, err := s.ConflictsFor(pr)
	if err != nil {
		s.lg.Error("AI判定：取协商库失败", "group", groupID, "err", err)
		return
	}
	sess, err := cs.Session(sid)
	if err != nil {
		s.lg.Error("AI判定：取会话失败", "sid", sid, "err", err)
		return
	}
	if sess.Status != conflicts.StatusOpen {
		s.lg.Info("AI判定：会话已不是 open，跳过", "sid", sid, "status", sess.Status)
		return
	}
	vers, err := cs.Versions(sid)
	if err != nil || len(vers) < 2 {
		fail := "至少要两版同名文档才谈得上合并"
		if err != nil {
			fail = "取文档版本失败：" + err.Error()
		}
		s.finishAIFail(cs, sess, fail)
		return
	}

	// 攒上下文：几版文档 + 协商聊天 + 关联通话的留言
	docs, err := s.GroupDocs(pr)
	if err != nil {
		s.finishAIFail(cs, sess, "取共享文档库失败："+err.Error())
		return
	}
	prompt, err := s.buildAIJudgePrompt(ctx, docs, sess, vers, cs)
	if err != nil {
		s.finishAIFail(cs, sess, "组装判定上下文失败："+err.Error())
		return
	}

	// 先立旗：两边界面轮询能立刻看到「AI 正在判定」，而不是点了没反应
	if _, err := cs.SetAIStatus(sid, "running", ""); err != nil {
		s.lg.Warn("AI判定：写 running 状态失败", "sid", sid, "err", err)
	}

	provCfg, err := s.translateProvider() // 第一条可用通道（与翻译同口径）
	if err != nil {
		s.finishAIFail(cs, sess, "没有可用的模型通道："+err.Error())
		return
	}
	prov, err := llm.New(provCfg)
	if err != nil {
		s.finishAIFail(cs, sess, "模型通道建不起来："+err.Error())
		return
	}
	resp, err := prov.Chat(ctx, llm.Request{
		Messages:  []llm.Message{{Role: llm.RoleUser, Content: prompt}},
		MaxTokens: 8192, // 合并内容可能比原文还长一点，给足
	})
	if err != nil {
		s.finishAIFail(cs, sess, "模型没回话："+err.Error())
		return
	}
	d, err := parseAIJudgeReply(resp.Text)
	if err != nil {
		s.finishAIFail(cs, sess, "模型输出不是约定的 JSON 契约："+err.Error())
		return
	}

	updated, err := s.applyAIJudgeDecision(cs, docs, sess, vers, d)
	if err != nil {
		s.finishAIFail(cs, sess, "判定结果落库失败："+err.Error())
		return
	}
	note := "AI 判定：" + d.Reason
	if _, err := cs.SetAIStatus(sid, "done", note); err != nil {
		s.lg.Warn("AI判定：写 done 状态失败", "sid", sid, "err", err)
	}
	s.lg.Info("AI判定完成", "conflict", sid, "decision", d.Decision, "model", resp.Model)
	_ = updated
}

// finishAIFail 如实记失败（状态 + 一条系统留言），绝不静默
func (s *Service) finishAIFail(cs *conflicts.Store, sess conflicts.Session, why string) {
	if _, err := cs.SetAIStatus(sess.ID, "failed", why); err == nil {
		if _, err := cs.AddMessage(sess.ID, "ai", "白泽AI", "AI 判定失败："+why); err == nil {
			return
		}
	}
	s.lg.Error("AI判定失败", "conflict", sess.ID, "why", why)
}

// buildAIJudgePrompt 组装判定上下文（文档正文截断 + 聊天记录 + 关联通话留言）
func (s *Service) buildAIJudgePrompt(ctx context.Context, docs *kb.FileStore,
	sess conflicts.Session, vers []conflicts.DocVersion, cs *conflicts.Store) (string, error) {

	var b strings.Builder
	fmt.Fprintf(&b, "「白泽」正在替两位协作者处理同一份文档的分叉。文档名：%q。下面是各版本正文、聊天记录、以及他们通话里发的文字留言。\n", sess.Name)

	b.WriteString("\n【文档版本】\n")
	for i, v := range vers {
		who := strings.TrimSpace(v.OwnerName)
		if who == "" {
			who = "作者未知"
		}
		fmt.Fprintf(&b, "\n版本 %d（作者：%s，docId=%s）：\n", i+1, who, v.DocID)
		_, data, err := docs.Get(v.DocID)
		if err != nil {
			fmt.Fprintf(&b, "（这一版内容取不到：%v）\n", err)
			continue
		}
		text := capText(string(data), aiJudgeDocCap)
		b.WriteString(text + "\n")
	}

	msgs, err := cs.Messages(sess.ID, 0, aiJudgeMsgLimit)
	if err == nil && len(msgs) > 0 {
		b.WriteString("\n【协商聊天记录】（按时间顺序）\n")
		for _, m := range msgs {
			who := strings.TrimSpace(m.FromName)
			if who == "" {
				who = m.FromID
			}
			fmt.Fprintf(&b, "%s：%s\n", who, capText(m.Text, aiJudgeMsgCap))
		}
	}

	if cs != nil {
		// 关联通话的留言（自由通话没有 conflict_sid，取不到就只写协商聊天）
		if sCalls := s.callsStoreFor(sess.GroupID); sCalls != nil {
			if linked, err := sCalls.ByConflict(sess.GroupID, sess.ID); err == nil {
				for _, c := range linked {
					cmsgs, err := sCalls.Messages(c.ID, 0, aiJudgeMsgLimit)
					if err != nil || len(cmsgs) == 0 {
						continue
					}
					b.WriteString(fmt.Sprintf("\n【通话留言】（%s 与 %s，通话 %s）\n",
						c.AName, c.BName, c.ID))
					for _, m := range cmsgs {
						who := strings.TrimSpace(m.FromName)
						if who == "" {
							who = m.FromID
						}
						fmt.Fprintf(&b, "%s：%s\n", who, capText(m.Text, aiJudgeMsgCap))
					}
				}
			}
		}
	}

	b.WriteString(`
请你判断：保留其中某一版，还是把几版合并成一份更完整的。
合并时以双方聊天记录里**已达成共识**的内容为准；没谈拢的部分两边都保留，不要臆造任何新内容。

只输出一个 JSON 对象，不要输出任何其它文字（不要 Markdown 代码块）：
保留某一版：{"decision":"keep","docId":"<要保留的版本 docId>","reason":"<一句话理由>"}
合并成一份：{"decision":"merge","mergedContent":"<合并后的完整文档内容>","reason":"<一句话理由>"}
`)
	return b.String(), nil
}

// applyAIJudgeDecision 落库：keep = 定稿保留那一版；merge = 新内容存成新版本再定稿。
// 两种都会清掉同名分叉（只留定稿正本），并在协商聊天里写一条系统留言。
func (s *Service) applyAIJudgeDecision(cs *conflicts.Store, docs *kb.FileStore,
	sess conflicts.Session, vers []conflicts.DocVersion, d AIJudgeDecision) (conflicts.Session, error) {

	switch strings.ToLower(strings.TrimSpace(d.Decision)) {
	case "keep":
		d.DocID = strings.TrimSpace(d.DocID)
		if d.DocID == "" {
			return sess, errors.New("keep 判定没给要保留的 docId")
		}
		found := false
		for _, v := range vers {
			if v.DocID == d.DocID {
				found = true
				break
			}
		}
		if !found {
			return sess, fmt.Errorf("模型要保留的 docId %q 不在这次协商的版本里", d.DocID)
		}
		updated, err := cs.Resolve(sess.ID, d.DocID, "ai")
		if err != nil {
			return sess, err
		}
		s.PruneFork(sess.GroupID, sess.Name, d.DocID)
		if _, err := cs.AddMessage(sess.ID, "ai", "白泽AI",
			"AI 通读判定：保留 docId="+d.DocID+" 这一版。理由："+strings.TrimSpace(d.Reason)); err != nil {
			s.lg.Warn("AI判定：写系统留言失败", "conflict", sess.ID, "err", err)
		}
		return updated, nil
	case "merge":
		content := d.MergedContent
		if strings.TrimSpace(content) == "" {
			return sess, errors.New("merge 判定没给合并后的内容")
		}
		kind, mime := "file", ""
		if info, _, err := docs.Get(vers[0].DocID); err == nil {
			kind, mime = info.Kind, info.Mime
		}
		info, err := docs.PutBy(sess.Name, kind, mime, "ai", "白泽AI", []byte(content))
		if err != nil {
			return sess, fmt.Errorf("合并内容存进共享文档库失败：%w", err)
		}
		updated, err := cs.Resolve(sess.ID, info.ID, "ai")
		if err != nil {
			return sess, err
		}
		s.PruneFork(sess.GroupID, sess.Name, info.ID)
		if _, err := cs.AddMessage(sess.ID, "ai", "白泽AI",
			"AI 通读判定：几版已合并成新正本（docId="+info.ID+"）。理由："+strings.TrimSpace(d.Reason)); err != nil {
			s.lg.Warn("AI判定：写系统留言失败", "conflict", sess.ID, "err", err)
		}
		return updated, nil
	default:
		return sess, fmt.Errorf("模型回了不认识的 decision：%q（只认 keep / merge）", d.Decision)
	}
}

// PruneFork 定稿后把同名文档的其它版本删掉，只留正本（返回被删的 docId）。
// 删不动不算致命（文件可能已不在），如实回传。
func (s *Service) PruneFork(groupID, name, keepDocID string) []string {
	store, err := s.GroupDocs(accounts.GroupPrincipal(groupID))
	if err != nil {
		return nil
	}
	list, err := store.List(0)
	if err != nil {
		return nil
	}
	removed := []string{}
	for _, f := range list {
		if f.Name != name || f.ID == keepDocID {
			continue
		}
		if err := store.Remove(f.ID); err == nil {
			removed = append(removed, f.ID)
		}
	}
	return removed
}

// parseAIJudgeReply 从模型回话里抠出 JSON 契约。
// 容忍 ```json 围栏与前后杂文：取第一个 '{' 到最后一个 '}' 之间解析。
func parseAIJudgeReply(raw string) (AIJudgeDecision, error) {
	t := strings.TrimSpace(raw)
	if i := strings.Index(t, "{"); i >= 0 {
		if j := strings.LastIndex(t, "}"); j > i {
			t = t[i : j+1]
		}
	}
	var d AIJudgeDecision
	if err := json.Unmarshal([]byte(t), &d); err != nil {
		return AIJudgeDecision{}, fmt.Errorf("解不出 JSON（原文前 200 字：%s）", head200(raw))
	}
	return d, nil
}

// callsStoreFor 取组通话库（走分区缓存；取不到就 nil，调用方按"没有通话留言"处理）
func (s *Service) callsStoreFor(groupID string) *calls.Store {
	cs, err := s.CallsFor(accounts.GroupPrincipal(groupID))
	if err != nil {
		return nil
	}
	return cs
}

func capText(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + fmt.Sprintf("…（截断，原文共 %d 字）", len(r))
}

func head200(s string) string {
	r := []rune(s)
	if len(r) <= 200 {
		return s
	}
	return string(r[:200]) + "…"
}
