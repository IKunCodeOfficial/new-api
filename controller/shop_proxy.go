package controller

import (
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/gin-gonic/gin"
)

// The recharge-center shop (https://9.plus) keeps its captcha/session state in
// a first-party PHPSESSID cookie issued without SameSite=None, so browsers
// drop it inside a cross-site iframe and the captcha can neither load nor
// verify. Reverse-proxying the shop under our own origin makes that cookie
// first-party again. Route wiring lives in router/web-router.go.
const (
	shopProxyCookiePrefix = "shopproxy_"
	// Bodies larger than this stream through without URL rewriting; the shop
	// API responses that need rewriting are small JSON payloads.
	shopProxyRewriteMaxBody = 4 << 20
)

// Overridable in tests.
var shopUpstreamBase = "https://9.plus"

var shopProxyClient = &http.Client{
	Timeout: 60 * time.Second,
	CheckRedirect: func(req *http.Request, via []*http.Request) error {
		// Relay redirects to the browser (with Location rewritten) instead of
		// following them server-side, so upstream Set-Cookie is never lost.
		return http.ErrUseLastResponse
	},
}

// ShopProxyHost returns the normalized SHOP_PROXY_HOST value. Empty means the
// proxy rides the panel origin behind a logged-in session; non-empty means it
// answers only on that dedicated host, public and rate-limited, so the
// third-party shop scripts never run on the panel origin. The dedicated host
// must share the panel's registrable domain (e.g. shop.example.com next to
// example.com), otherwise the iframe is cross-site again and the proxied
// cookie is dropped just like the upstream one.
func ShopProxyHost() string {
	host := strings.ToLower(strings.TrimSpace(common.GetEnvOrDefaultString("SHOP_PROXY_HOST", "")))
	host = strings.TrimPrefix(host, "https://")
	host = strings.TrimPrefix(host, "http://")
	return strings.Trim(host, "/")
}

// ShopProxyHostGate hides the shop proxy from every host except the dedicated
// one: page requests fall back to the SPA (as if the route did not exist) and
// API/asset requests get 404.
func ShopProxyHostGate(dedicatedHost string, serveIndexPage gin.HandlerFunc) gin.HandlerFunc {
	return func(c *gin.Context) {
		if shopProxyHostMatches(dedicatedHost, c.Request.Host) {
			c.Next()
			return
		}
		if strings.HasPrefix(c.Request.URL.Path, "/shop/") {
			serveIndexPage(c)
		} else {
			c.Status(http.StatusNotFound)
		}
		c.Abort()
	}
}

func shopProxyHostMatches(dedicatedHost, requestHost string) bool {
	requestHost = strings.ToLower(requestHost)
	if requestHost == dedicatedHost {
		return true
	}
	hostname, _, err := net.SplitHostPort(requestHost)
	return err == nil && hostname == dedicatedHost
}

// ProxyShop relays /shop/:code (the shop page), /shopApi/* (its API, including
// the captcha endpoints) and /package/* (its static assets) to the upstream
// shop.
func ProxyShop(c *gin.Context) {
	if strings.Contains(c.Request.URL.Path, "..") {
		c.Status(http.StatusBadRequest)
		return
	}
	upstreamURL := shopUpstreamBase + c.Request.URL.Path
	if c.Request.URL.RawQuery != "" {
		upstreamURL += "?" + c.Request.URL.RawQuery
	}
	upstreamReq, err := http.NewRequestWithContext(c.Request.Context(), c.Request.Method, upstreamURL, c.Request.Body)
	if err != nil {
		c.Status(http.StatusBadGateway)
		return
	}
	upstreamReq.ContentLength = c.Request.ContentLength
	copyShopProxyRequestHeaders(c.Request, upstreamReq)

	resp, err := shopProxyClient.Do(upstreamReq)
	if err != nil {
		common.SysError("shop proxy upstream error: " + err.Error())
		c.Status(http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	relayShopProxyResponse(c, resp)
}

// Only benign browser headers cross the trust boundary: the panel session
// cookie, Authorization and Referer/Origin must never reach the third-party
// shop. Accept-Encoding is intentionally not forwarded either — the transport
// then negotiates gzip itself and transparently decompresses, so textual
// bodies are always plain for URL rewriting.
var shopProxyRequestHeaders = []string{
	"Accept", "Accept-Language", "Content-Type", "User-Agent", "X-Requested-With",
}

func copyShopProxyRequestHeaders(src, upstream *http.Request) {
	for _, name := range shopProxyRequestHeaders {
		if value := src.Header.Get(name); value != "" {
			upstream.Header.Set(name, value)
		}
	}
	for _, ck := range src.Cookies() {
		if name, ok := strings.CutPrefix(ck.Name, shopProxyCookiePrefix); ok {
			upstream.AddCookie(&http.Cookie{Name: name, Value: ck.Value})
		}
	}
}

// Upstream headers relayed verbatim. Everything else (HSTS, NEL, Alt-Svc,
// CORS, raw Set-Cookie, ...) is dropped so the shop cannot reconfigure the
// panel origin.
var shopProxyResponseHeaders = []string{"Content-Type", "Last-Modified", "ETag"}

func relayShopProxyResponse(c *gin.Context, resp *http.Response) {
	for _, name := range shopProxyResponseHeaders {
		if value := resp.Header.Get(name); value != "" {
			c.Header(name, value)
		}
	}
	for _, ck := range resp.Cookies() {
		http.SetCookie(c.Writer, rewriteShopProxyCookie(ck, requestIsHTTPS(c.Request)))
	}
	if location := resp.Header.Get("Location"); location != "" {
		c.Header("Location", rewriteShopUpstreamURLs(location))
	}

	isShopAPI := strings.HasPrefix(c.Request.URL.Path, "/shopApi/")
	// The Cache() middleware pre-set a week-long Cache-Control; that must
	// never apply to session-bound API responses or the (unhashed) shop page.
	if isShopAPI {
		c.Header("Cache-Control", "no-store")
	} else if upstreamCacheControl := resp.Header.Get("Cache-Control"); upstreamCacheControl != "" {
		c.Header("Cache-Control", upstreamCacheControl)
	} else if strings.HasPrefix(c.Request.URL.Path, "/shop/") {
		c.Header("Cache-Control", "no-cache")
	}

	if isShopAPI && isTextualShopResponse(resp) {
		body, err := io.ReadAll(io.LimitReader(resp.Body, shopProxyRewriteMaxBody+1))
		if err != nil {
			c.Status(http.StatusBadGateway)
			return
		}
		if len(body) <= shopProxyRewriteMaxBody {
			c.Data(resp.StatusCode, resp.Header.Get("Content-Type"), []byte(rewriteShopUpstreamURLs(string(body))))
			return
		}
		c.Status(resp.StatusCode)
		_, _ = c.Writer.Write(body)
		_, _ = io.Copy(c.Writer, resp.Body)
		return
	}

	c.Status(resp.StatusCode)
	_, _ = io.Copy(c.Writer, resp.Body)
}

func isTextualShopResponse(resp *http.Response) bool {
	if resp.Header.Get("Content-Encoding") != "" {
		// Still compressed despite the transport's transparent negotiation;
		// stream through untouched rather than corrupt it.
		return false
	}
	contentType := strings.ToLower(resp.Header.Get("Content-Type"))
	return strings.HasPrefix(contentType, "text/") ||
		strings.Contains(contentType, "json") ||
		strings.Contains(contentType, "javascript") ||
		strings.Contains(contentType, "xml")
}

// captchaStart returns absolute upstream URLs (JSON-escaped, e.g.
// "https:\/\/9.plus\/shopApi\/common\/captchaImg.html?key=..."); they must
// become path-absolute so the browser keeps talking to this proxy instead of
// going cross-site again.
var shopUpstreamURLReplacer = strings.NewReplacer(
	`https:\/\/9.plus`, "",
	`http:\/\/9.plus`, "",
	"https://9.plus", "",
	"http://9.plus", "",
	`\/\/9.plus`, "",
	"//9.plus", "",
)

func rewriteShopUpstreamURLs(s string) string {
	return shopUpstreamURLReplacer.Replace(s)
}

// The upstream cookie is re-issued on our origin under a namespaced name so it
// can never collide with (or be mistaken for) a panel cookie. Lax, not
// Strict: the shop iframe is same-site with the panel — or with the dedicated
// proxy host — and its subresource requests must carry this cookie.
func rewriteShopProxyCookie(upstream *http.Cookie, secure bool) *http.Cookie {
	return &http.Cookie{
		Name:     shopProxyCookiePrefix + upstream.Name,
		Value:    upstream.Value,
		Path:     "/",
		Expires:  upstream.Expires,
		MaxAge:   upstream.MaxAge,
		Secure:   upstream.Secure || secure,
		HttpOnly: upstream.HttpOnly,
		SameSite: http.SameSiteLaxMode,
	}
}

func requestIsHTTPS(r *http.Request) bool {
	return r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
}
