package controller

import (
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/middleware"
	"github.com/gin-contrib/sessions"
	"github.com/gin-contrib/sessions/cookie"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestShopProxyRewritesUpstreamURLs(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "json escaped captcha urls become path absolute",
			in:   `{"img_url":"https:\/\/9.plus\/shopApi\/common\/captchaImg.html?key=k1","check_url":"https:\/\/9.plus\/shopApi\/common\/captchaCheck.html?key=k2"}`,
			want: `{"img_url":"\/shopApi\/common\/captchaImg.html?key=k1","check_url":"\/shopApi\/common\/captchaCheck.html?key=k2"}`,
		},
		{
			name: "plain absolute url",
			in:   `<a href="https://9.plus/shop/2F7A86NF">shop</a>`,
			want: `<a href="/shop/2F7A86NF">shop</a>`,
		},
		{
			name: "protocol relative url",
			in:   `url("//9.plus/package/shop/assets/a.css")`,
			want: `url("/package/shop/assets/a.css")`,
		},
		{
			name: "unrelated content untouched",
			in:   `{"msg":"验证码不正确","host":"example.com"}`,
			want: `{"msg":"验证码不正确","host":"example.com"}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, rewriteShopUpstreamURLs(tt.in))
		})
	}
}

func TestShopProxyCookieRewrite(t *testing.T) {
	upstream := &http.Cookie{Name: "PHPSESSID", Value: "sess123", Path: "/", MaxAge: 1440}

	rewritten := rewriteShopProxyCookie(upstream, false)
	assert.Equal(t, "shopproxy_PHPSESSID", rewritten.Name)
	assert.Equal(t, "sess123", rewritten.Value)
	assert.Equal(t, "/", rewritten.Path)
	assert.Equal(t, 1440, rewritten.MaxAge)
	assert.Equal(t, http.SameSiteLaxMode, rewritten.SameSite)
	assert.False(t, rewritten.Secure)

	assert.True(t, rewriteShopProxyCookie(upstream, true).Secure)
}

func TestShopProxyHostNormalization(t *testing.T) {
	tests := []struct {
		raw  string
		want string
	}{
		{"", ""},
		{"shop.example.com", "shop.example.com"},
		{" https://Shop.Example.com/ ", "shop.example.com"},
		{"http://shop.example.com", "shop.example.com"},
	}
	for _, tt := range tests {
		t.Setenv("SHOP_PROXY_HOST", tt.raw)
		assert.Equal(t, tt.want, ShopProxyHost(), "raw=%q", tt.raw)
	}
}

// newShopProxyTestEngine mirrors setShopProxyRouter's panel-origin wiring plus
// a login helper so tests can obtain a real web session.
func newShopProxyTestEngine(t *testing.T, guards ...gin.HandlerFunc) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.Use(sessions.Sessions("session", cookie.NewStore([]byte("test-secret"))))
	engine.GET("/test-login", func(c *gin.Context) {
		session := sessions.Default(c)
		session.Set("id", 1)
		require.NoError(t, session.Save())
		c.Status(http.StatusOK)
	})
	handlers := append(guards, ProxyShop)
	engine.GET("/shop/:code", handlers...)
	engine.Any("/shopApi/*shopPath", handlers...)
	engine.GET("/package/*shopPath", handlers...)
	return engine
}

func TestShopProxyCaptchaFlowOnPanelOrigin(t *testing.T) {
	pngBytes := []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a, 0x00, 0xff}
	var captchaImgCookieHeader, captchaImgAuthHeader, captchaImgReferer string

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/shopApi/Common/captchaStart":
			http.SetCookie(w, &http.Cookie{Name: "PHPSESSID", Value: "sess123", Path: "/", MaxAge: 1440})
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			_, _ = w.Write([]byte(`{"code":1,"data":{"img_url":"https:\/\/9.plus\/shopApi\/common\/captchaImg.html?key=k1","check_url":"https:\/\/9.plus\/shopApi\/common\/captchaCheck.html?key=k2"}}`))
		case "/shopApi/common/captchaImg.html":
			captchaImgCookieHeader = r.Header.Get("Cookie")
			captchaImgAuthHeader = r.Header.Get("Authorization")
			captchaImgReferer = r.Header.Get("Referer")
			if ck, err := r.Cookie("PHPSESSID"); err == nil && ck.Value == "sess123" {
				w.Header().Set("Content-Type", "image/png; charset=utf-8")
				_, _ = w.Write(pngBytes)
				return
			}
			w.Header().Set("Content-Type", "text/html; charset=UTF-8")
		case "/shop/CODE1":
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write([]byte("<html>shop page</html>"))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer upstream.Close()

	originalBase := shopUpstreamBase
	shopUpstreamBase = upstream.URL
	defer func() { shopUpstreamBase = originalBase }()

	engine := newShopProxyTestEngine(t, middleware.WebSessionAuth())
	panel := httptest.NewServer(engine)
	defer panel.Close()

	// Without a web session every proxied path is rejected.
	anonResp, err := http.Get(panel.URL + "/shop/CODE1")
	require.NoError(t, err)
	_ = anonResp.Body.Close()
	assert.Equal(t, http.StatusUnauthorized, anonResp.StatusCode)

	jar, err := cookiejar.New(nil)
	require.NoError(t, err)
	client := &http.Client{Jar: jar}
	loginResp, err := client.Get(panel.URL + "/test-login")
	require.NoError(t, err)
	_ = loginResp.Body.Close()
	require.Equal(t, http.StatusOK, loginResp.StatusCode)

	// captchaStart: absolute upstream URLs are rewritten, the upstream session
	// cookie is re-issued namespaced on the panel origin, and the response is
	// never cacheable.
	startResp, err := client.Get(panel.URL + "/shopApi/Common/captchaStart")
	require.NoError(t, err)
	startBody, err := io.ReadAll(startResp.Body)
	_ = startResp.Body.Close()
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, startResp.StatusCode)
	assert.NotContains(t, string(startBody), "9.plus")
	assert.Contains(t, string(startBody), `"img_url":"\/shopApi\/common\/captchaImg.html?key=k1"`)
	assert.Equal(t, "no-store", startResp.Header.Get("Cache-Control"))
	var proxiedCookie *http.Cookie
	for _, ck := range startResp.Cookies() {
		if ck.Name == "shopproxy_PHPSESSID" {
			proxiedCookie = ck
		}
	}
	require.NotNil(t, proxiedCookie, "namespaced shop session cookie must be issued")
	assert.Equal(t, "sess123", proxiedCookie.Value)

	// captchaImg: the namespaced cookie flows back upstream under its original
	// name, and the binary body streams through untouched.
	imgResp, err := client.Get(panel.URL + "/shopApi/common/captchaImg.html?key=k1")
	require.NoError(t, err)
	imgBody, err := io.ReadAll(imgResp.Body)
	_ = imgResp.Body.Close()
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, imgResp.StatusCode)
	assert.Equal(t, pngBytes, imgBody)

	// Trust boundary: the upstream shop saw only its own session cookie, no
	// panel session cookie, Authorization or Referer.
	assert.Equal(t, "PHPSESSID=sess123", captchaImgCookieHeader)
	assert.Empty(t, captchaImgAuthHeader)
	assert.Empty(t, captchaImgReferer)

	// Page HTML is proxied for logged-in users and marked non-cacheable
	// (the Cache() middleware default would otherwise cache it for a week).
	pageResp, err := client.Get(panel.URL + "/shop/CODE1")
	require.NoError(t, err)
	pageBody, err := io.ReadAll(pageResp.Body)
	_ = pageResp.Body.Close()
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, pageResp.StatusCode)
	assert.Equal(t, "<html>shop page</html>", string(pageBody))
	assert.Equal(t, "no-cache", pageResp.Header.Get("Cache-Control"))
}

func TestShopProxyHostGate(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte("proxied:" + r.URL.Path))
	}))
	defer upstream.Close()

	originalBase := shopUpstreamBase
	shopUpstreamBase = upstream.URL
	defer func() { shopUpstreamBase = originalBase }()

	serveIndexPage := func(c *gin.Context) {
		c.Data(http.StatusOK, "text/html; charset=utf-8", []byte("spa-index"))
	}
	engine := newShopProxyTestEngine(t, ShopProxyHostGate("shop.example.com", serveIndexPage))

	tests := []struct {
		name       string
		host       string
		path       string
		wantStatus int
		wantBody   string
	}{
		{"dedicated host serves proxy", "shop.example.com", "/shop/CODE1", http.StatusOK, "proxied:/shop/CODE1"},
		{"dedicated host with port serves proxy", "shop.example.com:8443", "/shopApi/x", http.StatusOK, "proxied:/shopApi/x"},
		{"other host page request gets spa", "panel.example.com", "/shop/CODE1", http.StatusOK, "spa-index"},
		{"other host api request gets 404", "panel.example.com", "/shopApi/Common/captchaStart", http.StatusNotFound, ""},
		{"other host asset request gets 404", "panel.example.com", "/package/shop/assets/a.js", http.StatusNotFound, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tt.path, nil)
			req.Host = tt.host
			recorder := httptest.NewRecorder()
			engine.ServeHTTP(recorder, req)
			assert.Equal(t, tt.wantStatus, recorder.Code)
			if tt.wantBody != "" {
				assert.Equal(t, tt.wantBody, recorder.Body.String())
			}
		})
	}

	// The public dedicated-host mode must not require a web session.
	req := httptest.NewRequest(http.MethodGet, "/shop/CODE1", nil)
	req.Host = "shop.example.com"
	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, req)
	assert.Equal(t, http.StatusOK, recorder.Code)
	assert.True(t, strings.HasPrefix(recorder.Body.String(), "proxied:"))
}
