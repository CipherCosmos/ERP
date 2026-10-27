package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"gbos/internal/auth"
	"gbos/internal/config"
	"gbos/internal/db"
	"gbos/internal/handlers"
	"gbos/internal/middleware"
	"gbos/internal/models"
	"gbos/internal/rbac"
	"github.com/gin-gonic/gin"
)

func main() {
	cfg := config.Load()

	// JWT security: refuse to start in release mode with default secret
	if cfg.GinMode == "release" && cfg.IsDefaultSecret() {
		log.Fatal("FATAL: Cannot start in release mode with default JWT_SECRET. Set JWT_SECRET environment variable.")
	}
	if cfg.IsDefaultSecret() {
		log.Println("WARNING: Using default JWT secret. Set JWT_SECRET environment variable for production.")
	}

	auth.Configure(cfg.JWTSecret, cfg.JWTExpiration)

	if err := db.Initialize(cfg.DatabasePath); err != nil {
		log.Fatalf("Failed to initialize database: %v", err)
	}
	defer db.Close()

	models.DB = db.DB

	authMw := middleware.NewAuthMiddleware()
	rateLimiter := middleware.NewRateLimiter()

	gin.SetMode(cfg.GinMode)
	r := gin.Default()

	// Global rate limit: 100 requests/second per IP, with burst of 100
	r.Use(rateLimiter.GlobalRateLimit(100, 100))

	corsOrigin := cfg.CORSOrigin
	r.Use(func(c *gin.Context) {
		c.Header("Access-Control-Allow-Origin", corsOrigin)
		c.Header("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		c.Header("Access-Control-Allow-Headers", "Content-Type, Authorization, X-Store-ID, X-User-Role")
		if c.Request.Method == "OPTIONS" {
			c.AbortWithStatus(204)
			return
		}
		c.Next()
	})

	r.GET("/health", handlers.Health)
	r.HEAD("/health", handlers.Health)

	api := r.Group("/api/v1")
	api.Use(middleware.AuditMutationRequests())
	{
		api.GET("/health", handlers.Health)
		api.HEAD("/health", handlers.Health)

		api.POST("/auth/login", rateLimiter.LoginRateLimit(), handlers.Login)
		api.POST("/auth/register", authMw.RequireAuth(), authMw.RequirePermission(rbac.PermUserRegister), handlers.Register)
		api.GET("/auth/users", authMw.RequireAuth(), authMw.RequirePermission(rbac.PermUserList), handlers.ListUsers)
		api.POST("/auth/users/:id/deactivate", authMw.RequireAuth(), authMw.RequirePermission(rbac.PermUserDeactivate), handlers.DeactivateUser)
		api.POST("/auth/users/:id/activate", authMw.RequireAuth(), authMw.RequirePermission(rbac.PermUserDeactivate), handlers.ActivateUser)
		api.POST("/auth/change-password", authMw.RequireAuth(), authMw.RequirePermission(rbac.PermPasswordChange), handlers.ChangePassword)

		categories := api.Group("/categories")
		{
			cats := handlers.NewCategoryHandler()
			categories.GET("", authMw.RequireAuth(), authMw.RequirePermission(rbac.PermCatalogRead), cats.List)
			categories.POST("", authMw.RequireAuth(), authMw.RequirePermission(rbac.PermCategoryWrite), cats.Create)
		}

		products := api.Group("/products")
		{
			prods := handlers.NewProductHandler()
			products.GET("", authMw.RequireAuth(), authMw.RequirePermission(rbac.PermCatalogRead), prods.List)
			products.GET("/:id/packing-units", authMw.RequireAuth(), authMw.RequirePermission(rbac.PermCatalogRead), prods.ListPackingUnits)
			products.POST("/:id/packing-units", authMw.RequireAuth(), authMw.RequirePermission(rbac.PermProductCatalogWrite), prods.CreatePackingUnit)
			products.GET("/:id", authMw.RequireAuth(), authMw.RequirePermission(rbac.PermCatalogRead), prods.Get)
			products.POST("", authMw.RequireAuth(), authMw.RequirePermission(rbac.PermProductCatalogWrite), prods.Create)
			products.PUT("/:id", authMw.RequireAuth(), authMw.RequirePermission(rbac.PermProductUpdate), prods.Update)
			products.POST("/price", authMw.RequireAuth(), authMw.RequirePermission(rbac.PermProductPrice), prods.SetPrice)
		}

		stock := api.Group("/stock")
		{
			s := handlers.NewStockHandler()
			stock.GET("", authMw.RequireAuth(), authMw.RequirePermission(rbac.PermCatalogRead), s.List)
			stock.GET("/product/:id", authMw.RequireAuth(), authMw.RequirePermission(rbac.PermCatalogRead), s.GetProductStock)
			stock.POST("/adjust", authMw.RequireAuth(), authMw.RequirePermission(rbac.PermStockMove), s.Adjust)
			stock.POST("/transfer", authMw.RequireAuth(), authMw.RequirePermission(rbac.PermStockMove), s.Transfer)
			stock.POST("/convert", authMw.RequireAuth(), authMw.RequirePermission(rbac.PermStockMove), s.Convert)
			stock.GET("/low", authMw.RequireAuth(), authMw.RequirePermission(rbac.PermCatalogRead), s.LowStock)
			stock.GET("/adjustments", authMw.RequireAuth(), authMw.RequirePermission(rbac.PermCatalogRead), s.GetAdjustments)
		}

		sync := api.Group("/sync")
		{
			s := handlers.NewSyncHandler()
			sync.GET("/pending", authMw.RequireAuth(), authMw.RequirePermission(rbac.PermSyncRead), s.GetPending)
			sync.POST("/mark", authMw.RequireAuth(), authMw.RequirePermission(rbac.PermSyncMark), s.MarkSynced)
			sync.POST("/push", authMw.RequireAuth(), authMw.RequirePermission(rbac.PermSyncPush), s.Push)
			sync.POST("/conflict", authMw.RequireAuth(), authMw.RequirePermission(rbac.PermSyncConflict), s.ConflictResolution)
		}

		invoices := api.Group("/invoices")
		{
			inv := handlers.NewInvoiceHandler()
			invoices.GET("", authMw.RequireAuth(), authMw.RequirePermission(rbac.PermInvoiceRead), inv.List)
			invoices.GET("/gstr1", authMw.RequireAuth(), authMw.RequirePermission(rbac.PermReportView), inv.GSTR1Export)
			invoices.POST("/gstr1/mark-filed", authMw.RequireAuth(), authMw.RequirePermission(rbac.PermGSTR1File), inv.GSTR1MarkFiled)
			invoices.POST("", authMw.RequireAuth(), authMw.RequirePermission(rbac.PermInvoiceCreate), inv.Create)
			invoices.POST("/:id/void", authMw.RequireAuth(), authMw.RequirePermission(rbac.PermInvoiceVoid), inv.Void)
			invoices.GET("/:id", authMw.RequireAuth(), authMw.RequirePermission(rbac.PermInvoiceRead), inv.Get)
		}

		customers := api.Group("/customers")
		{
			cust := handlers.NewCustomerHandler()
			customers.GET("", authMw.RequireAuth(), authMw.RequirePermission(rbac.PermCatalogRead), cust.List)
			customers.GET("/:id", authMw.RequireAuth(), authMw.RequirePermission(rbac.PermCatalogRead), cust.Get)
			customers.POST("", authMw.RequireAuth(), authMw.RequirePermission(rbac.PermCustomerCreate), cust.Create)
			customers.PUT("/:id", authMw.RequireAuth(), authMw.RequirePermission(rbac.PermCustomerAdmin), cust.Update)
		}

		credit := api.Group("/credit")
		{
			cre := handlers.NewCreditHandler()
			credit.GET("", authMw.RequireAuth(), authMw.RequirePermission(rbac.PermCreditView), cre.List)
			credit.GET("/overdue", authMw.RequireAuth(), authMw.RequirePermission(rbac.PermCreditView), cre.Overdue)
			credit.POST("/payment", authMw.RequireAuth(), authMw.RequirePermission(rbac.PermCreditPay), cre.Payment)
			credit.GET("/:id", authMw.RequireAuth(), authMw.RequirePermission(rbac.PermCreditView), cre.Get)
		}

		vendors := api.Group("/vendors")
		{
			v := handlers.NewVendorHandler()
			vendors.GET("", authMw.RequireAuth(), authMw.RequirePermission(rbac.PermCatalogRead), v.List)
			vendors.GET("/:id", authMw.RequireAuth(), authMw.RequirePermission(rbac.PermCatalogRead), v.Get)
			vendors.POST("", authMw.RequireAuth(), authMw.RequirePermission(rbac.PermVendorWrite), v.Create)
			vendors.PUT("/:id", authMw.RequireAuth(), authMw.RequirePermission(rbac.PermVendorUpdate), v.Update)
		}

		purchases := api.Group("/purchases")
		{
			p := handlers.NewPurchaseHandler()
			purchases.GET("", authMw.RequireAuth(), authMw.RequirePermission(rbac.PermPurchaseRead), p.List)
			purchases.GET("/:id", authMw.RequireAuth(), authMw.RequirePermission(rbac.PermPurchaseRead), p.Get)
			purchases.POST("", authMw.RequireAuth(), authMw.RequirePermission(rbac.PermPurchaseWrite), p.Create)
			purchases.POST("/receive", authMw.RequireAuth(), authMw.RequirePermission(rbac.PermPurchaseWrite), p.Receive)
			purchases.POST("/cancel/:id", authMw.RequireAuth(), authMw.RequirePermission(rbac.PermPurchaseWrite), p.Cancel)
		}

		analytics := api.Group("/analytics")
		{
			a := handlers.NewAnalyticsHandler()
			analytics.GET("/dashboard", authMw.RequireAuth(), authMw.RequirePermission(rbac.PermAnalyticsView), a.Dashboard)
			analytics.GET("/sales/category", authMw.RequireAuth(), authMw.RequirePermission(rbac.PermAnalyticsView), a.SalesByCategory)
			analytics.GET("/sales/top", authMw.RequireAuth(), authMw.RequirePermission(rbac.PermAnalyticsView), a.TopProducts)
			analytics.GET("/sales/hourly", authMw.RequireAuth(), authMw.RequirePermission(rbac.PermAnalyticsView), a.HourlySales)
			analytics.GET("/sales/payment", authMw.RequireAuth(), authMw.RequirePermission(rbac.PermAnalyticsView), a.PaymentModeBreakup)
			analytics.GET("/multi-store", authMw.RequireAuth(), authMw.RequirePermission(rbac.PermAnalyticsView), a.MultiStore)
		}

		audit := api.Group("/audit")
		{
			audit.GET("", authMw.RequireAuth(), authMw.RequirePermission(rbac.PermAuditView), handlers.NewAuditHandler().List)
			audit.POST("", authMw.RequireAuth(), authMw.RequirePermission(rbac.PermAuditPost), handlers.NewAuditHandler().Log)
		}

		stores := api.Group("/stores")
		{
			s := handlers.NewStoreHandler()
			stores.GET("", authMw.RequireAuth(), authMw.RequirePermission(rbac.PermCatalogRead), s.List)
			stores.GET("/:id", authMw.RequireAuth(), authMw.RequirePermission(rbac.PermCatalogRead), s.Get)
			stores.POST("", authMw.RequireAuth(), authMw.RequirePermission(rbac.PermStoreCreate), s.Create)
			stores.PUT("/:id", authMw.RequireAuth(), authMw.RequirePermission(rbac.PermStoreUpdate), s.Update)
		}

		uoms := api.Group("/uoms")
		{
			u := handlers.NewUOMHandler()
			uoms.GET("", authMw.RequireAuth(), authMw.RequirePermission(rbac.PermCatalogRead), u.List)
			uoms.POST("", authMw.RequireAuth(), authMw.RequirePermission(rbac.PermUOMWrite), u.Create)
		}

		barcodes := api.Group("/barcodes")
		{
			b := handlers.NewBarcodeHandler()
			barcodes.POST("/generate", authMw.RequireAuth(), authMw.RequirePermission(rbac.PermBarcodeGenerate), b.Generate)
			barcodes.GET("/lookup/:barcode", authMw.RequireAuth(), authMw.RequirePermission(rbac.PermCatalogRead), b.Lookup)
		}

		expiry := api.Group("/expiry")
		{
			e := handlers.NewExpiryHandler()
			expiry.GET("/expiring", authMw.RequireAuth(), authMw.RequirePermission(rbac.PermExpiryRead), e.GetExpiring)
			expiry.GET("/expired", authMw.RequireAuth(), authMw.RequirePermission(rbac.PermExpiryRead), e.GetExpired)
		}

		payments := api.Group("/payments")
		{
			p := handlers.NewPaymentHandler()
			payments.POST("/split", authMw.RequireAuth(), authMw.RequirePermission(rbac.PermPaymentSplit), p.AddSplitPayments)
			payments.GET("/invoice/:id", authMw.RequireAuth(), authMw.RequirePermission(rbac.PermPaymentRead), p.GetInvoicePayments)
			payments.GET("/settlement", authMw.RequireAuth(), authMw.RequirePermission(rbac.PermPaymentRead), p.GetSettlementReport)
		}

		returns := api.Group("/returns")
		{
			ret := handlers.NewReturnHandler()
			returns.GET("", authMw.RequireAuth(), authMw.RequirePermission(rbac.PermReturnRead), ret.List)
			returns.POST("", authMw.RequireAuth(), authMw.RequirePermission(rbac.PermReturnCreate), ret.Create)
			returns.POST("/:id/approve", authMw.RequireAuth(), authMw.RequirePermission(rbac.PermReturnApprove), ret.Approve)
		}

		losses := api.Group("/losses")
		{
			l := handlers.NewLossHandler()
			losses.GET("", authMw.RequireAuth(), authMw.RequirePermission(rbac.PermLossRead), l.List)
			losses.POST("", authMw.RequireAuth(), authMw.RequirePermission(rbac.PermLossWrite), l.Create)
		}

		grn := api.Group("/grn")
		{
			g := handlers.NewGRNHandler()
			grn.GET("", authMw.RequireAuth(), authMw.RequirePermission(rbac.PermGRNRead), g.List)
			grn.GET("/:id", authMw.RequireAuth(), authMw.RequirePermission(rbac.PermGRNRead), g.Get)
			grn.POST("", authMw.RequireAuth(), authMw.RequirePermission(rbac.PermGRNWrite), g.Create)
		}

		reports := api.Group("/reports")
		{
			rep := handlers.NewReportHandler()
			reports.GET("/profit-loss", authMw.RequireAuth(), authMw.RequirePermission(rbac.PermReportView), rep.GetProfitLoss)
			reports.GET("/credit-aging", authMw.RequireAuth(), authMw.RequirePermission(rbac.PermReportView), rep.GetCreditAging)
			reports.GET("/gstr-3b", authMw.RequireAuth(), authMw.RequirePermission(rbac.PermReportView), rep.GetGSTR3B)
		}

		expenses := api.Group("/expenses")
		{
			e := handlers.NewExpenseHandler()
			expenses.GET("", authMw.RequireAuth(), authMw.RequirePermission(rbac.PermExpenseRead), e.List)
			expenses.GET("/categories", authMw.RequireAuth(), authMw.RequirePermission(rbac.PermExpenseRead), e.GetCategories)
			expenses.GET("/summary", authMw.RequireAuth(), authMw.RequirePermission(rbac.PermExpenseRead), e.GetSummary)
			expenses.POST("", authMw.RequireAuth(), authMw.RequirePermission(rbac.PermExpenseWrite), e.Create)
		}

		vendorPayments := api.Group("/vendor-payments")
		{
			vp := handlers.NewVendorPaymentHandler()
			vendorPayments.GET("", authMw.RequireAuth(), authMw.RequirePermission(rbac.PermVendorPayment), vp.List)
			vendorPayments.POST("", authMw.RequireAuth(), authMw.RequirePermission(rbac.PermVendorPayment), vp.Create)
			vendorPayments.GET("/summary", authMw.RequireAuth(), authMw.RequirePermission(rbac.PermVendorPayment), vp.Summary)
		}

		purchaseReturns := api.Group("/purchase-returns")
		{
			pr := handlers.NewPurchaseReturnHandler()
			purchaseReturns.GET("", authMw.RequireAuth(), authMw.RequirePermission(rbac.PermPurchaseReturn), pr.List)
			purchaseReturns.POST("", authMw.RequireAuth(), authMw.RequirePermission(rbac.PermPurchaseReturn), pr.Create)
			purchaseReturns.POST("/:id/approve", authMw.RequireAuth(), authMw.RequirePermission(rbac.PermPurchaseReturn), pr.Approve)
		}
	}

	// Graceful shutdown
	srv := &http.Server{
		Addr:    ":" + cfg.ServerPort,
		Handler: r,
	}

	go func() {
		log.Printf("Starting GBOS server on port %s (mode: %s)", cfg.ServerPort, cfg.GinMode)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("Server error: %v", err)
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit
	log.Println("Shutting down server...")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		log.Printf("Server forced to shutdown: %v", err)
	}
	log.Println("Server stopped")
}
