package routes

import (
	"embed"
	"io/fs"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/satym-in/tenant-saas-backend/internal/config"
	"github.com/satym-in/tenant-saas-backend/internal/handlers"
	"github.com/satym-in/tenant-saas-backend/internal/middleware"
	"gorm.io/gorm"
)

// webFiles packages the dashboard into the server binary, so the API and UI can
// be deployed together without an additional web server or a CORS boundary.
//
//go:embed web/*
var webFiles embed.FS

func assetFileSystem() http.FileSystem {
	assets, err := fs.Sub(webFiles, "web/assets")
	if err != nil {
		panic("embedded web assets are unavailable: " + err.Error())
	}
	return http.FS(assets)
}

// Setup wires up all API and embedded web routes and returns a configured gin.Engine.
func Setup(db *gorm.DB, cfg *config.Config) *gin.Engine {
	if cfg.Environment == "production" {
		gin.SetMode(gin.ReleaseMode)
	}

	router := gin.Default()

	authHandler := handlers.NewAuthHandler(db, cfg)
	userHandler := handlers.NewUserHandler(db)

	router.GET("/health", handlers.HealthCheck)

	// 20 requests/minute per IP with a burst of 5 on auth endpoints to slow down
	// credential stuffing / brute force attempts without a shared store dependency.
	authLimiter := middleware.NewIPRateLimiter(20, 5)

	api := router.Group("/api/v1")
	{
		authGroup := api.Group("/auth")
		authGroup.Use(authLimiter.Middleware())
		{
			authGroup.POST("/register", authHandler.Register)
			authGroup.POST("/login", authHandler.Login)
		}

		protected := api.Group("/")
		protected.Use(middleware.RequireAuth(cfg))
		{
			protected.GET("/me", userHandler.Me)
			protected.GET("/users", userHandler.ListUsers)
		}
	}

	dashboard, err := webFiles.ReadFile("web/index.html")
	if err != nil {
		panic("embedded dashboard is unavailable: " + err.Error())
	}

	router.GET("/", func(c *gin.Context) {
		c.Data(http.StatusOK, "text/html; charset=utf-8", dashboard)
	})
	router.StaticFS("/assets", assetFileSystem())

	return router
}
