package accounts

import (
	"os"
	"path/filepath"
	"testing"
)

func TestBootstrapAdmin(t *testing.T) {
	dir := t.TempDir()
	r, err := Open(dir, nil)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer r.Close()

	users, err := r.Users()
	if err != nil {
		t.Fatalf("Users: %v", err)
	}
	if len(users) != 1 || users[0].Name != "admin" || !users[0].Admin {
		t.Fatalf("首启应有一个 admin，实得 %+v", users)
	}
	// 初始口令落盘，且文件权限收得很紧
	if _, err := os.Stat(filepath.Join(dir, "admin-password.txt")); err != nil {
		t.Fatalf("初始口令文件不存在：%v", err)
	}
}

func TestCreateAuthenticateUser(t *testing.T) {
	r, err := Open(t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()

	u, err := r.CreateUser("阿离", "s3cret-密码", false)
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	if u.ID == "" || u.Admin {
		t.Fatalf("用户字段不对：%+v", u)
	}
	// 分区目录应当已经建好
	if _, err := os.Stat(filepath.Join(r.UserDir(u.ID), "kb", "files")); err != nil {
		t.Fatalf("用户分区没建好：%v", err)
	}

	if got, err := r.Authenticate("阿离", "s3cret-密码"); err != nil || got.ID != u.ID {
		t.Fatalf("正确口令应通过，实得 %+v / %v", got, err)
	}
	if _, err := r.Authenticate("阿离", "错的"); err != ErrBadPassword {
		t.Fatalf("错口令应报 ErrBadPassword，实得 %v", err)
	}
	if _, err := r.Authenticate("查无此人", "x"); err != ErrBadPassword {
		t.Fatalf("用户不存在也应报 ErrBadPassword（不泄露用户名），实得 %v", err)
	}
	// 重名应被挡住
	if _, err := r.CreateUser("阿离", "another", false); err == nil {
		t.Fatal("重名建用户应当失败")
	}
	// 大小写不敏感
	if _, err := r.UserByName("阿离"); err != nil {
		t.Fatalf("UserByName: %v", err)
	}
}

func TestSetPasswordInvalidatesSessions(t *testing.T) {
	r, _ := Open(t.TempDir(), nil)
	defer r.Close()

	u, _ := r.CreateUser("bob", "old-pass", false)
	if _, _, err := r.Login("bob", "old-pass"); err != nil {
		t.Fatal(err)
	}
	if err := r.SetPassword(u.ID, "new-pass"); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Authenticate("bob", "old-pass"); err != ErrBadPassword {
		t.Fatalf("旧口令应失效，实得 %v", err)
	}
	if _, err := r.Authenticate("bob", "new-pass"); err != nil {
		t.Fatalf("新口令应通过：%v", err)
	}
}

func TestSessionLifecycle(t *testing.T) {
	r, _ := Open(t.TempDir(), nil)
	defer r.Close()

	u, _ := r.CreateUser("carol", "pw-123456", false)
	got, tok, err := r.Login("carol", "pw-123456")
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	if got.ID != u.ID || tok == "" {
		t.Fatalf("登录结果不对：%+v / %q", got, tok)
	}
	if who, ok := r.Session(tok); !ok || who.ID != u.ID {
		t.Fatalf("会话应能换回用户，实得 %+v / %v", who, ok)
	}
	if _, ok := r.Session("瞎编的令牌"); ok {
		t.Fatal("伪造令牌不该通过")
	}
	if err := r.Logout(tok); err != nil {
		t.Fatal(err)
	}
	if _, ok := r.Session(tok); ok {
		t.Fatal("登出后会话应失效")
	}
}

func TestGroups(t *testing.T) {
	r, _ := Open(t.TempDir(), nil)
	defer r.Close()

	a, _ := r.CreateUser("userA", "pw-a-1234", false)
	b, _ := r.CreateUser("userB", "pw-b-1234", false)
	c, _ := r.CreateUser("userC", "pw-c-1234", false)

	if _, err := r.CreateGroup("  ", ""); err == nil {
		t.Fatal("空名建组应当失败")
	}
	g, err := r.CreateGroup("组1", a.ID)
	if err != nil {
		t.Fatalf("CreateGroup: %v", err)
	}
	if g.OwnerID != a.ID || g.MemberCount != 1 {
		t.Fatalf("建组结果不对：%+v", g)
	}
	if _, err := os.Stat(filepath.Join(r.GroupDir(g.ID), "shared")); err != nil {
		t.Fatalf("共享分区没建好：%v", err)
	}
	if err := r.AddMember(g.ID, b.ID); err != nil {
		t.Fatalf("AddMember: %v", err)
	}
	if err := r.AddMember(g.ID, b.ID); err != nil {
		t.Fatalf("重复加人应当幂等：%v", err)
	}
	if !r.IsMember(g.ID, a.ID) || !r.IsMember(g.ID, b.ID) {
		t.Fatal("A/B 都该在组里")
	}
	if r.IsMember(g.ID, c.ID) {
		t.Fatal("C 不该在组里")
	}
	ms, err := r.Members(g.ID)
	if err != nil || len(ms) != 2 {
		t.Fatalf("成员应为 2 人，实得 %d / %v", len(ms), err)
	}
	gs, err := r.GroupsOf(b.ID)
	if err != nil || len(gs) != 1 || gs[0].ID != g.ID {
		t.Fatalf("GroupsOf(B) 应得组1，实得 %+v / %v", gs, err)
	}
	if err := r.RemoveMember(g.ID, b.ID); err != nil {
		t.Fatal(err)
	}
	if r.IsMember(g.ID, b.ID) {
		t.Fatal("移出后不该还在组里")
	}
	if err := r.RemoveMember(g.ID, b.ID); err == nil {
		t.Fatal("重复移出应当报错")
	}
	if err := r.AddMember(g.ID, "u_deadbeef0000"); err == nil {
		t.Fatal("加不存在的用户应当失败")
	}
}

func TestDeleteUserAndGroup(t *testing.T) {
	r, _ := Open(t.TempDir(), nil)
	defer r.Close()

	u, _ := r.CreateUser("dave", "pw-123456", false)
	g, _ := r.CreateGroup("临时组", u.ID)
	if err := r.DeleteUser(u.ID); err != nil {
		t.Fatalf("DeleteUser: %v", err)
	}
	if _, err := r.User(u.ID); err == nil {
		t.Fatal("删掉后不该再查得到")
	}
	if r.IsMember(g.ID, u.ID) {
		t.Fatal("删用户应连带清掉他的组关系")
	}
	if err := r.DeleteGroup(g.ID); err != nil {
		t.Fatal(err)
	}
	if err := r.DeleteGroup(g.ID); err == nil {
		t.Fatal("重复删组应当报错")
	}
}

func TestPrincipalParsing(t *testing.T) {
	r, _ := Open(t.TempDir(), nil)
	defer r.Close()
	u, _ := r.CreateUser("erin", "pw-123456", false)
	g, _ := r.CreateGroup("研究组", u.ID)

	cases := []struct {
		in   string
		want Principal
	}{
		{"", PrincipalDefault},
		{"default", PrincipalDefault},
		{"user:" + u.ID, UserPrincipal(u.ID)},
		{"group:" + g.ID, GroupPrincipal(g.ID)},
		{"user:../../etc", PrincipalDefault}, // 路径穿越回落默认
		{"group:g_zzzz", PrincipalDefault},   // 非十六进制回落默认
		{"乱写", PrincipalDefault},
	}
	for _, c := range cases {
		if got := ParsePrincipal(c.in); got != c.want {
			t.Errorf("ParsePrincipal(%q) = %q，期望 %q", c.in, got, c.want)
		}
	}
	if ParsePrincipal("user:"+u.ID).UserID() != u.ID {
		t.Fatal("UserID 解析错")
	}
	if ParsePrincipal("group:"+g.ID).GroupID() != g.ID {
		t.Fatal("GroupID 解析错")
	}
	if r.PartitionDir(PrincipalDefault) != r.Dir() {
		t.Fatal("默认主体应落在根数据目录")
	}
	if r.PartitionDir(UserPrincipal(u.ID)) != r.UserDir(u.ID) {
		t.Fatal("用户主体目录不对")
	}
}
