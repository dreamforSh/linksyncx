package proxyurl

import (
	"strings"
	"testing"
)

func TestParse_空字符串直连(t *testing.T) {
	trimmed, parsed, err := Parse("")
	if err != nil {
		t.Fatalf("空字符串应直连: %v", err)
	}
	if trimmed != "" {
		t.Errorf("trimmed 应为空: got %q", trimmed)
	}
	if parsed != nil {
		t.Errorf("parsed 应为 nil: got %v", parsed)
	}
}

func TestParse_空白字符串直连(t *testing.T) {
	trimmed, parsed, err := Parse("   ")
	if err != nil {
		t.Fatalf("空白字符串应直连: %v", err)
	}
	if trimmed != "" {
		t.Errorf("trimmed 应为空: got %q", trimmed)
	}
	if parsed != nil {
		t.Errorf("parsed 应为 nil: got %v", parsed)
	}
}

func TestParse_有效HTTP代理(t *testing.T) {
	trimmed, parsed, err := Parse("http://proxy.example.com:8080")
	if err != nil {
		t.Fatalf("有效 HTTP 代理应成功: %v", err)
	}
	if trimmed != "http://proxy.example.com:8080" {
		t.Errorf("trimmed 不匹配: got %q", trimmed)
	}
	if parsed == nil {
		t.Fatal("parsed 不应为 nil")
	}
	if parsed.Host != "proxy.example.com:8080" {
		t.Errorf("Host 不匹配: got %q", parsed.Host)
	}
}

func TestParse_有效HTTPS代理(t *testing.T) {
	_, parsed, err := Parse("https://proxy.example.com:443")
	if err != nil {
		t.Fatalf("有效 HTTPS 代理应成功: %v", err)
	}
	if parsed.Scheme != "https" {
		t.Errorf("Scheme 不匹配: got %q", parsed.Scheme)
	}
}

func TestParse_有效SOCKS5代理_自动升级为SOCKS5H(t *testing.T) {
	trimmed, parsed, err := Parse("socks5://127.0.0.1:1080")
	if err != nil {
		t.Fatalf("有效 SOCKS5 代理应成功: %v", err)
	}
	// socks5 自动升级为 socks5h，确保 DNS 由代理端解析
	if trimmed != "socks5h://127.0.0.1:1080" {
		t.Errorf("trimmed 应升级为 socks5h: got %q", trimmed)
	}
	if parsed.Scheme != "socks5h" {
		t.Errorf("Scheme 应升级为 socks5h: got %q", parsed.Scheme)
	}
}

func TestParse_无效URL(t *testing.T) {
	_, _, err := Parse("://invalid")
	if err == nil {
		t.Fatal("无效 URL 应返回错误")
	}
	if !strings.Contains(err.Error(), "invalid proxy URL") {
		t.Errorf("错误信息应包含 'invalid proxy URL': got %s", err.Error())
	}
}

func TestParse_缺少Host(t *testing.T) {
	_, _, err := Parse("http://")
	if err == nil {
		t.Fatal("缺少 host 应返回错误")
	}
	if !strings.Contains(err.Error(), "missing host") {
		t.Errorf("错误信息应包含 'missing host': got %s", err.Error())
	}
}

func TestParse_不支持的Scheme(t *testing.T) {
	_, _, err := Parse("ftp://proxy.example.com:21")
	if err == nil {
		t.Fatal("不支持的 scheme 应返回错误")
	}
	if !strings.Contains(err.Error(), "unsupported proxy scheme") {
		t.Errorf("错误信息应包含 'unsupported proxy scheme': got %s", err.Error())
	}
}

func TestParse_含密码URL脱敏(t *testing.T) {
	// 场景 1: 带密码的 socks5 URL 应成功解析并升级为 socks5h
	trimmed, parsed, err := Parse("socks5://user:secret_password@proxy.local:1080")
	if err != nil {
		t.Fatalf("含密码的有效 URL 应成功: %v", err)
	}
	if trimmed == "" || parsed == nil {
		t.Fatal("应返回非空结果")
	}
	if parsed.Scheme != "socks5h" {
		t.Errorf("Scheme 应升级为 socks5h: got %q", parsed.Scheme)
	}
	if !strings.HasPrefix(trimmed, "socks5h://") {
		t.Errorf("trimmed 应以 socks5h:// 开头: got %q", trimmed)
	}
	if parsed.User == nil {
		t.Error("升级后应保留 UserInfo")
	}

	// 场景 2: 带密码但缺少 host（触发 Redacted 脱敏路径）
	_, _, err = Parse("http://user:secret_password@:0/")
	if err == nil {
		t.Fatal("缺少 host 应返回错误")
	}
	if strings.Contains(err.Error(), "secret_password") {
		t.Error("错误信息不应包含明文密码")
	}
	if !strings.Contains(err.Error(), "missing host") {
		t.Errorf("错误信息应包含 'missing host': got %s", err.Error())
	}
}

// 解析失败的错误会进入传输层错误、ops 日志与管理端提示，不得带出原始 URL 或其片段。
// *url.Error 自身会回显完整 URL；内层错误回显的片段在畸形 URL 里也可能截自密码。
func TestParse_解析失败不泄漏凭据(t *testing.T) {
	cases := []struct {
		name   string
		raw    string
		secret string
		prefix string
	}{
		// Proxy.URL() 会把 host 里的空格转义成 %20，url.Parse 再以非法转义拒绝。
		{"host 含转义空格", "http://user:s3cr3tpw@bad%20host:8080", "s3cr3tpw", "invalid proxy URL"},
		{"host 含空格", "http://user:s3cr3tpw@bad host:8080", "s3cr3tpw", "invalid proxy URL"},
		// 未转义的 "/" 截断 authority，"/" 之前的密码段被当作端口报错。
		{"密码含未转义斜杠", "http://user:pa55/w0rd@proxy.local:8080", "pa55", "invalid proxy URL"},
		// 密码里的非法转义片段本身就是密码的一部分。
		{"密码含非法转义", "http://user:p%zzword@proxy.local:8080", "%zz", "invalid proxy URL"},
		// 漏写 "//"：scheme=user、opaque=s3cr3tpw@proxy.local:1080，Redacted() 不会遮住 opaque。
		{"漏写双斜杠", "user:s3cr3tpw@proxy.local:1080", "s3cr3tpw", "proxy URL missing host"},
		// 多写一个 "/"：凭据被解析进 path，Redacted() 同样原样输出。
		{"凭据落入 path", "http:///user:s3cr3tpw@proxy.local:8080", "s3cr3tpw", "proxy URL missing host"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := Parse(tc.raw)
			if err == nil {
				t.Fatalf("%q 应返回错误", tc.raw)
			}
			if strings.Contains(err.Error(), tc.secret) {
				t.Errorf("错误信息不应包含凭据片段 %q: got %s", tc.secret, err.Error())
			}
			if !strings.HasPrefix(err.Error(), tc.prefix) {
				t.Errorf("错误信息应以 %q 开头: got %s", tc.prefix, err.Error())
			}
		})
	}
}

// 去掉回显片段后，内层原因仍保留可读的分类，便于管理员定位配置错误。
func TestParse_解析失败保留原因分类(t *testing.T) {
	_, _, err := Parse("http://user:pw@bad host:8080")
	if err == nil {
		t.Fatal("host 含空格应返回错误")
	}
	if got, want := err.Error(), "invalid proxy URL: invalid character in host name"; got != want {
		t.Errorf("错误信息不匹配: got %q, want %q", got, want)
	}
}

func TestParse_带空白的有效URL(t *testing.T) {
	trimmed, parsed, err := Parse("  http://proxy.example.com:8080  ")
	if err != nil {
		t.Fatalf("带空白的有效 URL 应成功: %v", err)
	}
	if trimmed != "http://proxy.example.com:8080" {
		t.Errorf("trimmed 应去除空白: got %q", trimmed)
	}
	if parsed == nil {
		t.Fatal("parsed 不应为 nil")
	}
}

func TestParse_Scheme大小写不敏感(t *testing.T) {
	// 大写 SOCKS5 应被接受并升级为 socks5h
	trimmed, parsed, err := Parse("SOCKS5://proxy.example.com:1080")
	if err != nil {
		t.Fatalf("大写 SOCKS5 应被接受: %v", err)
	}
	if parsed.Scheme != "socks5h" {
		t.Errorf("大写 SOCKS5 Scheme 应升级为 socks5h: got %q", parsed.Scheme)
	}
	if !strings.HasPrefix(trimmed, "socks5h://") {
		t.Errorf("大写 SOCKS5 trimmed 应升级为 socks5h://: got %q", trimmed)
	}

	// 大写 HTTP 应被接受（不变）
	_, _, err = Parse("HTTP://proxy.example.com:8080")
	if err != nil {
		t.Fatalf("大写 HTTP 应被接受: %v", err)
	}
}

func TestParse_带认证的有效代理(t *testing.T) {
	trimmed, parsed, err := Parse("http://user:pass@proxy.example.com:8080")
	if err != nil {
		t.Fatalf("带认证的代理 URL 应成功: %v", err)
	}
	if parsed.User == nil {
		t.Error("应保留 UserInfo")
	}
	if trimmed != "http://user:pass@proxy.example.com:8080" {
		t.Errorf("trimmed 不匹配: got %q", trimmed)
	}
}

func TestParse_IPv6地址(t *testing.T) {
	trimmed, parsed, err := Parse("http://[::1]:8080")
	if err != nil {
		t.Fatalf("IPv6 代理 URL 应成功: %v", err)
	}
	if parsed.Hostname() != "::1" {
		t.Errorf("Hostname 不匹配: got %q", parsed.Hostname())
	}
	if trimmed != "http://[::1]:8080" {
		t.Errorf("trimmed 不匹配: got %q", trimmed)
	}
}

func TestParse_SOCKS5H保持不变(t *testing.T) {
	trimmed, parsed, err := Parse("socks5h://proxy.local:1080")
	if err != nil {
		t.Fatalf("有效 SOCKS5H 代理应成功: %v", err)
	}
	// socks5h 不需要升级，应保持原样
	if trimmed != "socks5h://proxy.local:1080" {
		t.Errorf("trimmed 不应变化: got %q", trimmed)
	}
	if parsed.Scheme != "socks5h" {
		t.Errorf("Scheme 应保持 socks5h: got %q", parsed.Scheme)
	}
}

func TestParse_无Scheme裸地址(t *testing.T) {
	// 无 scheme 的裸地址，Go url.Parse 将其视为 path，Host 为空
	_, _, err := Parse("proxy.example.com:8080")
	if err == nil {
		t.Fatal("无 scheme 的裸地址应返回错误")
	}
}
