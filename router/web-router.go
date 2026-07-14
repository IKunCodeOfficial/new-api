package router

import (
	"embed"
	"net/http"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/controller"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/gin-contrib/gzip"
	"github.com/gin-contrib/static"
	"github.com/gin-gonic/gin"
)

// ThemeAssets holds the embedded frontend assets for both themes.
type ThemeAssets struct {
	DefaultBuildFS   embed.FS
	DefaultIndexPage []byte
	ClassicBuildFS   embed.FS
	ClassicIndexPage []byte
}

func SetWebRouter(router *gin.Engine, assets ThemeAssets) {
	defaultFS := common.EmbedFolder(assets.DefaultBuildFS, "web/default/dist")
	classicFS := common.EmbedFolder(assets.ClassicBuildFS, "web/classic/dist")
	themeFS := common.NewThemeAwareFS(defaultFS, classicFS)

	serveIndexPage := func(c *gin.Context) {
		c.Header("Cache-Control", "no-cache")
		if common.GetTheme() == "classic" {
			c.Data(http.StatusOK, "text/html; charset=utf-8", assets.ClassicIndexPage)
		} else {
			c.Data(http.StatusOK, "text/html; charset=utf-8", assets.DefaultIndexPage)
		}
	}

	router.Use(gzip.Gzip(gzip.DefaultCompression))
	router.Use(middleware.GlobalWebRateLimit())
	router.Use(middleware.Cache())
	router.Use(static.Serve("/", themeFS))

	setShopProxyRouter(router, serveIndexPage)

	router.NoRoute(func(c *gin.Context) {
		c.Set(middleware.RouteTagKey, "web")
		if strings.HasPrefix(c.Request.RequestURI, "/v1") || strings.HasPrefix(c.Request.RequestURI, "/api") || strings.HasPrefix(c.Request.RequestURI, "/assets") {
			controller.RelayNotFound(c)
			return
		}
		serveIndexPage(c)
	})
}

// The recharge-center shop is reverse-proxied so its session cookie stays
// first-party inside the iframe (see controller/shop_proxy.go). With
// SHOP_PROXY_HOST unset the proxy rides the panel origin and requires a
// logged-in session; when set, it answers only on that dedicated host, public
// but rate-limited, and every other host sees the SPA as if the routes did
// not exist.
func setShopProxyRouter(router *gin.Engine, serveIndexPage gin.HandlerFunc) {
	var guards []gin.HandlerFunc
	if host := controller.ShopProxyHost(); host == "" {
		guards = []gin.HandlerFunc{middleware.WebSessionAuth()}
	} else {
		guards = []gin.HandlerFunc{
			controller.ShopProxyHostGate(host, serveIndexPage),
			middleware.ShopProxyRateLimit(),
		}
	}
	handlers := append(guards, controller.ProxyShop)
	router.GET("/shop/:code", handlers...)
	router.Any("/shopApi/*shopPath", handlers...)
	router.GET("/package/*shopPath", handlers...)
}
