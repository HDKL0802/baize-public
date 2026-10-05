package slash

import (
	"context"
	"errors"
	"testing"
)

/* 魔法命令注册表的单元测试。
   关注：解析（大小写/别名/光斜杠/参数）、回退、同名更新、坏定义拦截、错误透传。 */

func replyWith(s string) Handler {
	return func(context.Context, string, string) (Result, error) { return Result{Reply: s}, nil }
}

func TestResolve(t *testing.T) {
	r := New()
	r.Register(Spec{Name: "Help", Aliases: []string{"h", "?"}, Help: "帮助", Handler: replyWith("ok")})

	cases := []struct {
		in   string
		name string
		args string
		ok   bool
	}{
		{"/help", "help", "", true},
		{"/HELP", "help", "", true},
		{"/h", "help", "", true},
		{"/?", "help", "", true},
		{"/", "help", "", true}, // 光一个斜杠 = 要看帮助
		{"/help me", "help", "me", true},
		{"  /help  ", "help", "", true},
		{"/nope", "", "", false},
		{"hello", "", "", false},
	}
	for _, c := range cases {
		spec, args, ok := r.Resolve(c.in)
		if ok != c.ok {
			t.Fatalf("%q ok=%v 期望 %v", c.in, ok, c.ok)
		}
		if !ok {
			continue
		}
		if spec.Name != c.name || args != c.args {
			t.Fatalf("%q => %q/%q 期望 %q/%q", c.in, spec.Name, args, c.name, c.args)
		}
	}
}

func TestDispatchAndFallback(t *testing.T) {
	r := New()
	r.Register(Spec{Name: "help", Handler: replyWith("builtin")})
	r.RegisterFallback(func(_ context.Context, raw string) (Result, bool) {
		if raw == "/skill now" {
			return Result{Goal: "injected"}, true
		}
		return Result{}, false
	})

	if res, ok, _ := r.Dispatch(context.Background(), "/help"); !ok || res.Reply != "builtin" {
		t.Fatalf("内置命令应命中：%+v %v", res, ok)
	}
	// 内置优先，即便回退也认识这个名字
	if res, ok, _ := r.Dispatch(context.Background(), "/help extra"); !ok || res.Reply != "builtin" {
		t.Fatalf("内置应优先于回退：%+v %v", res, ok)
	}
	if res, ok, _ := r.Dispatch(context.Background(), "/skill now"); !ok || res.Goal != "injected" {
		t.Fatalf("回退应命中：%+v %v", res, ok)
	}
	if _, ok, _ := r.Dispatch(context.Background(), "/none"); ok {
		t.Fatal("回退也不认识时不该报 handled")
	}
	if _, ok, _ := r.Dispatch(context.Background(), "普通文本"); ok {
		t.Fatal("不以斜杠开头不该命中")
	}
}

func TestRegisterUpdate(t *testing.T) {
	r := New()
	r.Register(Spec{Name: "x", Aliases: []string{"old"}, Handler: replyWith("1")})
	r.Register(Spec{Name: "x", Aliases: []string{"new"}, Handler: replyWith("2")})

	if _, _, ok := r.Resolve("/old"); ok {
		t.Fatal("更新后旧别名应失效")
	}
	if _, _, ok := r.Resolve("/new"); !ok {
		t.Fatal("更新后新别名应生效")
	}
	if adv := r.Advertise(); len(adv) != 1 {
		t.Fatalf("同名重复注册应只留一条，实际 %d：%+v", len(adv), adv)
	}
	if res, _, _ := r.Dispatch(context.Background(), "/x"); res.Reply != "2" {
		t.Fatalf("更新后应走新处理器：%+v", res)
	}
}

func TestInvalidRegistrationIgnored(t *testing.T) {
	r := New()
	r.Register(Spec{Name: "", Handler: replyWith("x")}) // 没名字
	r.Register(Spec{Name: "y"})                          // 没处理器
	if adv := r.Advertise(); len(adv) != 0 {
		t.Fatalf("坏定义不该进表：%+v", adv)
	}
}

func TestHandlerErrorPropagates(t *testing.T) {
	r := New()
	r.Register(Spec{Name: "boom", Handler: func(context.Context, string, string) (Result, error) {
		return Result{}, errors.New("炸了")
	}})
	_, ok, err := r.Dispatch(context.Background(), "/boom")
	if !ok || err == nil || err.Error() != "炸了" {
		t.Fatalf("处理器错误应透传：ok=%v err=%v", ok, err)
	}
}

func TestAdvertiseSorted(t *testing.T) {
	r := New()
	r.Register(Spec{Name: "b", Category: "B", Help: "bb", Handler: replyWith("")})
	r.Register(Spec{Name: "a", Category: "A", Help: "aa", Handler: replyWith("")})
	adv := r.Advertise()
	if len(adv) != 2 || adv[0].Name != "a" || adv[1].Name != "b" {
		t.Fatalf("应按分类/名字排序：%+v", adv)
	}
}
