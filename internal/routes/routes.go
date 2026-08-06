package routes

import (
	"embed"
	"io/fs"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/satym-in/tenant-saas-backend/internal/apikeys"
	"github.com/satym-in/tenant-saas-backend/internal/audit"
	"github.com/satym-in/tenant-saas-backend/internal/authz"
	"github.com/satym-in/tenant-saas-backend/internal/billing"
	"github.com/satym-in/tenant-saas-backend/internal/config"
	"github.com/satym-in/tenant-saas-backend/internal/eventbus"
	"github.com/satym-in/tenant-saas-backend/internal/identity"
	"github.com/satym-in/tenant-saas-backend/internal/invitations"
	"github.com/satym-in/tenant-saas-backend/internal/mailer"
	"github.com/satym-in/tenant-saas-backend/internal/middleware"
	"github.com/satym-in/tenant-saas-backend/internal/notifications"
	"github.com/satym-in/tenant-saas-backend/internal/platform"
	"github.com/satym-in/tenant-saas-backend/internal/preferences"
	"github.com/satym-in/tenant-saas-backend/internal/projects"
	"github.com/satym-in/tenant-saas-backend/internal/teams"
	"github.com/satym-in/tenant-saas-backend/internal/tenancy"
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

// Setup wires up all modules and returns a configured gin.Engine. This is the
// application's dependency injection root: every service/repository is
// constructed here and passed down, with no global state or package-level
// singletons anywhere in the module packages themselves.
func Setup(db *gorm.DB, cfg *config.Config) *gin.Engine {
	if cfg.Environment == "production" {
		gin.SetMode(gin.ReleaseMode)
	}

	router := gin.New()

	// --- Cross-cutting middleware, outermost first ---
	// RequestID runs before the logger so every log line and error envelope
	// carries the same correlation ID. Recovery sits inside RequestLogger so a
	// panic is converted to a 500 first and then logged with that status,
	// rather than escaping the logger entirely.
	appLogger := middleware.NewLogger(cfg.Environment)
	router.Use(middleware.RequestID())
	router.Use(middleware.RequestLogger(appLogger))
	router.Use(middleware.Recovery(appLogger))
	router.Use(middleware.SecurityHeaders())
	router.Use(middleware.CORS(cfg.CORSAllowedOrigins))

	// --- Module wiring (dependency injection root) ---
	tenantRepo := tenancy.NewRepository(db)
	tenantResolver := tenancy.NewResolver(tenantRepo, nil, cfg.TenantBaseDomain) // nil cache until Redis phase

	authzRepo := authz.NewRepository(db)
	authzService := authz.NewService(authzRepo, nil) // nil cache until Redis phase
	authzHandler := authz.NewHandler(authzService)

	auditService := audit.NewService(audit.NewRepository(db), appLogger)
	auditHandler := audit.NewHandler(auditService)

	notificationService := notifications.NewService(notifications.NewRepository(db))
	notificationHandler := notifications.NewHandler(notificationService)

	billingService := billing.NewService(billing.NewRepository(db), auditService)
	billingHandler := billing.NewHandler(billingService)

	teamService := teams.NewService(teams.NewRepository(db), auditService, notificationService)
	teamHandler := teams.NewHandler(teamService)

	// Projects take billingService as their quota checker, which is what enforces
	// the plan's max_projects limit at creation time.
	projectService := projects.NewService(projects.NewRepository(db), auditService, billingService, notificationService)
	projectHandler := projects.NewHandler(projectService)

	// Mail transport is chosen once, here: a configured Resend key selects the
	// HTTP sender, an unset one selects the no-op that logs an undeliverable
	// message. No flow branches on it, so an environment without a mail provider
	// behaves identically except that nothing is delivered.
	var mailSender mailer.Sender = mailer.Noop{Logger: appLogger}
	if cfg.ResendAPIKey != "" && cfg.MailFrom != "" && cfg.AppBaseURL != "" {
		mailSender = mailer.NewResend(cfg.ResendAPIKey, cfg.MailFrom)
	} else if cfg.ResendAPIKey != "" {
		// Half-configured: a key with no sender or no public base URL would
		// produce provider rejections or dead links on every send. Say so once at
		// startup rather than once per message.
		appLogger.Error("email disabled: RESEND_API_KEY is set but MAIL_FROM or APP_BASE_URL is missing")
	}
	mailNotifier := mailer.NewNotifier(mailSender, cfg.AppBaseURL, appLogger)

	invitationService := invitations.NewService(
		invitations.NewRepository(db), auditService, billingService, eventbus.NoopPublisher{}, authzService,
		mailNotifier, tenantRepo,
		invitations.Config{TTL: config.Duration(cfg.InvitationTTL, 0)})
	invitationHandler := invitations.NewHandler(invitationService)

	// API keys may only be scoped to permissions the platform actually defines, so
	// the grantable set is the permission catalog's own constants.
	apiKeyService := apikeys.NewService(apikeys.NewRepository(db), auditService, appLogger, authz.GrantableScopes())
	apiKeyHandler := apikeys.NewHandler(apiKeyService)

	preferenceService := preferences.NewService(preferences.NewRepository(db), auditService, authzService)
	preferenceHandler := preferences.NewHandler(preferenceService)

	identityRepo := identity.NewRepository(db)
	identityService := identity.NewService(identityRepo, tenantRepo, authzService, eventbus.NoopPublisher{}, mailNotifier, db, identity.Config{
		JWTSecret:            cfg.JWTSecret,
		AccessTokenTTL:       config.Duration(cfg.AccessTokenTTL, 0),
		RefreshTokenTTL:      config.Duration(cfg.RefreshTokenTTL, 0),
		PasswordResetTTL:     config.Duration(cfg.PasswordResetTTL, 0),
		EmailVerificationTTL: config.Duration(cfg.EmailVerificationTTL, 0),
	}, identity.OAuthConfig{
		GitHubClientID:     cfg.GitHubOAuthClientID,
		GitHubClientSecret: cfg.GitHubOAuthClientSecret,
		GitHubRedirectURL:  cfg.GitHubOAuthRedirectURL,
		StateSecret:        cfg.JWTSecret, // reuse JWT secret to sign OAuth state; no separate secret needed
	})
	identityHandler := identity.NewHandler(identityService)

	healthHandler := platform.NewHealthHandler(db)

	// 20 requests/minute per IP with a burst of 5 on unauthenticated endpoints, to
	// slow credential stuffing and invite-token guessing without a shared store.
	authLimiter := middleware.NewIPRateLimiter(20, 5)

	// Health checks are registered before tenant resolution: an orchestrator's
	// probe carries no tenant hint and must never be rejected for that.
	router.GET("/health", healthHandler.Health)
	router.GET("/health/ready", healthHandler.Ready)
	router.GET("/health/live", healthHandler.Live)

	// The static documents and assets are registered before tenant resolution
	// too, and for the same reason: they are identical for every tenant, so
	// resolving one is both pointless and a failure mode. Gin binds the
	// middleware chain at registration time, so ordering here is what keeps a
	// stylesheet request off the database entirely -- including the NoRoute
	// fallback, which would otherwise make an unknown hostname 404 the
	// dashboard instead of serving it.
	registerWebRoutes(router)

	router.Use(tenantResolver.Middleware())

	api := router.Group("/api/v1")
	{
		authGroup := api.Group("/auth")
		authGroup.Use(authLimiter.Middleware())
		{
			authGroup.POST("/register", identityHandler.Register)
			authGroup.POST("/login", identityHandler.Login)
			authGroup.POST("/refresh", identityHandler.Refresh)
			authGroup.POST("/logout", identityHandler.Logout)
			authGroup.POST("/forgot-password", identityHandler.ForgotPassword)
			authGroup.POST("/reset-password", identityHandler.ResetPassword)
			authGroup.POST("/verify-email", identityHandler.VerifyEmail)
			authGroup.GET("/oauth/:provider", identityHandler.OAuthAuthorize)
			authGroup.GET("/oauth/:provider/callback", identityHandler.OAuthCallback)
		}

		// Invite preview/accept are necessarily unauthenticated -- the invitee may
		// have no account yet -- so they are rate-limited to blunt token guessing.
		publicInvites := api.Group("/invitations")
		publicInvites.Use(authLimiter.Middleware())
		{
			publicInvites.GET("/preview", invitationHandler.Preview)
			publicInvites.POST("/accept", invitationHandler.Accept)
		}

		// The plan catalog is public so a pricing page can render pre-signup.
		api.GET("/billing/plans", billingHandler.ListPlans)

		protected := api.Group("/")
		// apikeys.Authenticate runs first and only acts when an API key is
		// presented; identity.RequireAuth then handles JWTs and produces the single
		// 401 when neither credential is present.
		protected.Use(apikeys.Authenticate(apiKeyService))
		protected.Use(identity.RequireAuth(cfg.JWTSecret, identityService))
		{
			protected.GET("/me", identityHandler.Me)
			protected.POST("/verify-email/request", identityHandler.RequestEmailVerification)

			// --- Self-service: always the caller's own account ---
			protected.GET("/profile", preferenceHandler.GetProfile)
			protected.PATCH("/profile", apikeys.RequireUserSession(), preferenceHandler.UpdateProfile)
			protected.POST("/profile/change-password", apikeys.RequireUserSession(), preferenceHandler.ChangePassword)
			protected.GET("/preferences", preferenceHandler.Get)
			protected.PATCH("/preferences", apikeys.RequireUserSession(), preferenceHandler.Update)

			// --- Notifications: scoped to the caller, no permission gate needed ---
			notificationGroup := protected.Group("/notifications")
			{
				notificationGroup.GET("", notificationHandler.List)
				notificationGroup.GET("/unread-count", notificationHandler.UnreadCount)
				notificationGroup.POST("/read-all", notificationHandler.MarkAllRead)
				notificationGroup.POST("/:notificationID/read", notificationHandler.MarkRead)
				notificationGroup.DELETE("/:notificationID", notificationHandler.Delete)
			}

			// --- Members ---
			protected.GET("/users",
				authzService.RequirePermission(authz.PermMemberView),
				identityHandler.ListUsers)

			// --- Roles and permissions ---
			roles := protected.Group("/roles")
			{
				roles.GET("", authzService.RequirePermission(authz.PermRoleView), authzHandler.ListRoles)
				roles.GET("/:roleID/permissions", authzService.RequirePermission(authz.PermRoleView), authzHandler.GetRolePermissions)
				roles.POST("", authzService.RequirePermission(authz.PermRoleManage), authzHandler.CreateRole)
				roles.PUT("/:roleID/permissions", authzService.RequirePermission(authz.PermRoleManage), authzHandler.UpdateRolePermissions)
				roles.DELETE("/:roleID", authzService.RequirePermission(authz.PermRoleManage), authzHandler.DeleteRole)
				roles.POST("/assign", authzService.RequirePermission(authz.PermRoleManage), authzHandler.AssignRole)
				roles.POST("/revoke", authzService.RequirePermission(authz.PermRoleManage), authzHandler.RevokeRole)
			}
			protected.GET("/permissions", authzService.RequirePermission(authz.PermRoleView), authzHandler.ListPermissionCatalog)

			// --- Teams ---
			teamGroup := protected.Group("/teams")
			{
				teamGroup.GET("", authzService.RequirePermission(authz.PermTeamView), teamHandler.List)
				teamGroup.POST("", authzService.RequirePermission(authz.PermTeamCreate), teamHandler.Create)
				teamGroup.GET("/:teamID", authzService.RequirePermission(authz.PermTeamView), teamHandler.Get)
				teamGroup.PATCH("/:teamID", authzService.RequirePermission(authz.PermTeamManage), teamHandler.Update)
				teamGroup.DELETE("/:teamID", authzService.RequirePermission(authz.PermTeamManage), teamHandler.Delete)
				teamGroup.GET("/:teamID/members", authzService.RequirePermission(authz.PermTeamView), teamHandler.ListMembers)
				teamGroup.POST("/:teamID/members", authzService.RequirePermission(authz.PermTeamManage), teamHandler.AddMember)
				teamGroup.DELETE("/:teamID/members/:userID", authzService.RequirePermission(authz.PermTeamManage), teamHandler.RemoveMember)
			}

			// --- Projects ---
			projectGroup := protected.Group("/projects")
			{
				projectGroup.GET("", authzService.RequirePermission(authz.PermProjectView), projectHandler.List)
				projectGroup.POST("", authzService.RequirePermission(authz.PermProjectCreate), projectHandler.Create)
				projectGroup.GET("/:projectID", authzService.RequirePermission(authz.PermProjectView), projectHandler.Get)
				projectGroup.PATCH("/:projectID", authzService.RequirePermission(authz.PermProjectManage), projectHandler.Update)
				projectGroup.DELETE("/:projectID", authzService.RequirePermission(authz.PermProjectDelete), projectHandler.Delete)
				projectGroup.GET("/:projectID/members", authzService.RequirePermission(authz.PermProjectView), projectHandler.ListMembers)
				projectGroup.POST("/:projectID/members", authzService.RequirePermission(authz.PermProjectManage), projectHandler.AddMember)
				projectGroup.DELETE("/:projectID/members/:userID", authzService.RequirePermission(authz.PermProjectManage), projectHandler.RemoveMember)
			}

			// --- Invitations (management side) ---
			inviteGroup := protected.Group("/invitations")
			{
				inviteGroup.GET("", authzService.RequirePermission(authz.PermMemberView), invitationHandler.List)
				inviteGroup.POST("", authzService.RequirePermission(authz.PermMemberInvite), invitationHandler.Create)
				inviteGroup.DELETE("/:inviteID", authzService.RequirePermission(authz.PermMemberInvite), invitationHandler.Revoke)
			}

			// --- API keys: minting is restricted to user sessions so a leaked key
			// cannot mint replacements for itself ---
			keyGroup := protected.Group("/api-keys")
			{
				keyGroup.GET("", authzService.RequirePermission(authz.PermAPIKeyView), apiKeyHandler.List)
				keyGroup.POST("", apikeys.RequireUserSession(), authzService.RequirePermission(authz.PermAPIKeyManage), apiKeyHandler.Create)
				keyGroup.DELETE("/:keyID", apikeys.RequireUserSession(), authzService.RequirePermission(authz.PermAPIKeyManage), apiKeyHandler.Revoke)
			}

			// --- Billing ---
			billingGroup := protected.Group("/billing")
			{
				billingGroup.GET("/subscription", authzService.RequirePermission(authz.PermBillingView), billingHandler.GetSubscription)
				billingGroup.GET("/usage", authzService.RequirePermission(authz.PermBillingView), billingHandler.GetUsage)
				billingGroup.POST("/subscription", authzService.RequirePermission(authz.PermBillingManage), billingHandler.ChangePlan)
				billingGroup.DELETE("/subscription", authzService.RequirePermission(authz.PermBillingManage), billingHandler.Cancel)
			}

			// --- Audit log and activity feed ---
			protected.GET("/audit-logs", authzService.RequirePermission(authz.PermAuditView), auditHandler.ListAuditLogs)
			protected.GET("/activity", authzService.RequirePermission(authz.PermOrgView), auditHandler.ListActivity)
		}
	}

	return router
}

// registerWebRoutes serves the embedded marketing page and single-page dashboard.
//
// "/" is the landing page and "/app" is the workspace. The dashboard routes with
// the URL fragment ("/app#/projects"), which the server never sees, so the split
// needs no path handling here and no base-path awareness in app.js.
//
// Unknown non-API paths fall through to the SPA document, which is what makes a
// stale bookmark or a hand-typed /projects still land somewhere useful, while
// anything under /api/ keeps returning a JSON 404 instead of HTML -- an API
// client parsing an HTML error page is a worse failure than a clear 404.
func registerWebRoutes(router *gin.Engine) {
	dashboard, err := webFiles.ReadFile("web/index.html")
	if err != nil {
		panic("embedded dashboard is unavailable: " + err.Error())
	}
	landing, err := webFiles.ReadFile("web/landing.html")
	if err != nil {
		panic("embedded landing page is unavailable: " + err.Error())
	}
	favicon, err := webFiles.ReadFile("web/assets/brand/favicon.ico")
	if err != nil {
		panic("embedded favicon is unavailable: " + err.Error())
	}
	manifest, err := webFiles.ReadFile("web/assets/site.webmanifest")
	if err != nil {
		panic("embedded web manifest is unavailable: " + err.Error())
	}

	serveDashboard := func(c *gin.Context) {
		c.Data(http.StatusOK, "text/html; charset=utf-8", dashboard)
	}

	router.GET("/", func(c *gin.Context) {
		c.Data(http.StatusOK, "text/html; charset=utf-8", landing)
	})
	router.GET("/app", serveDashboard)
	// Conventional root paths are still requested by browsers, crawlers and
	// install surfaces even when the documents declare explicit asset URLs.
	router.GET("/favicon.ico", func(c *gin.Context) {
		c.Data(http.StatusOK, "image/x-icon", favicon)
	})
	router.GET("/site.webmanifest", func(c *gin.Context) {
		c.Data(http.StatusOK, "application/manifest+json; charset=utf-8", manifest)
	})
	router.StaticFS("/assets", assetFileSystem())

	router.NoRoute(func(c *gin.Context) {
		path := c.Request.URL.Path
		if len(path) >= 5 && path[:5] == "/api/" {
			c.JSON(http.StatusNotFound, gin.H{
				"success": false,
				"error": gin.H{
					"code":    "NOT_FOUND",
					"message": "endpoint not found",
				},
			})
			return
		}
		serveDashboard(c)
	})
}
