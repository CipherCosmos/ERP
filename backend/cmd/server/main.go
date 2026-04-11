package main

import (
	"log"

	"github.com/gin-gonic/gin"
	"gbos/internal/auth"
	"gbos/internal/config"
	"gbos/internal/db"
	"gbos/internal/handlers"
	"gbos/internal/middleware"
	"gbos/internal/models"
)

func main() {
	cfg := config.Load()
	auth.Configure(cfg.JWTSecret, cfg.JWTExpiration)

	if err := db.Initialize(cfg.DatabasePath); err != nil {
		log.Fatalf("Failed to initialize database: %v", err)
	}
	defer db.Close()

	models.DB = db.DB

	authMw := middleware.NewAuthMiddleware()

	r := gin.Default()

	r.Use(func(c *gin.Context) {
		c.Header("Access-Control-Allow-Origin", "*")
		c.Header("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		c.Header("Access-Control-Allow-Headers", "Content-Type, Authorization, X-Store-ID, X-User-Role")
		if c.Request.Method == "OPTIONS" {
			c.AbortWithStatus(204)
			return
		}
		c.Next()
	})

	api := r.Group("/api/v1")
	{
		api.GET("/health", handlers.Health)

		api.POST("/auth/login", handlers.Login)
		api.POST("/auth/register", handlers.Register)

		categories := api.Group("/categories")
		{
			cats := handlers.NewCategoryHandler()
			categories.GET("", cats.List)
			categories.POST("", authMw.RequireAuth(), authMw.RequireRole("owner", "manager"), cats.Create)
		}

		products := api.Group("/products")
		{
			prods := handlers.NewProductHandler()
			products.GET("", prods.List)
			products.GET("/:id/packing-units", prods.ListPackingUnits)
			products.POST("/:id/packing-units", authMw.RequireAuth(), authMw.RequireRole("owner", "manager"), prods.CreatePackingUnit)
			products.GET("/:id", prods.Get)
			products.POST("", authMw.RequireAuth(), authMw.RequireRole("owner", "manager"), prods.Create)
			products.POST("/price", authMw.RequireAuth(), authMw.RequireRole("owner", "manager"), prods.SetPrice)
		}

		stock := api.Group("/stock")
		{
			s := handlers.NewStockHandler()
			stock.GET("", s.List)
			stock.GET("/product/:id", s.GetProductStock)
			stock.POST("/adjust", authMw.RequireAuth(), authMw.RequireRole("owner", "manager", "warehouse"), s.Adjust)
			stock.POST("/transfer", authMw.RequireAuth(), authMw.RequireRole("owner", "manager", "warehouse"), s.Transfer)
			stock.GET("/low", s.LowStock)
			stock.GET("/adjustments", s.GetAdjustments)
		}

		sync := api.Group("/sync")
		{
			s := handlers.NewSyncHandler()
			sync.GET("/pending", s.GetPending)
			sync.POST("/mark", authMw.RequireAuth(), s.MarkSynced)
			sync.POST("/push", authMw.RequireAuth(), authMw.RequireRole("owner", "manager", "warehouse"), s.Push)
			sync.POST("/conflict", authMw.RequireAuth(), authMw.RequireRole("owner", "manager"), s.ConflictResolution)
		}

		invoices := api.Group("/invoices")
		{
			inv := handlers.NewInvoiceHandler()
			invoices.GET("", inv.List)
			invoices.GET("/:id", inv.Get)
			invoices.POST("", authMw.RequireAuth(), inv.Create)
			invoices.GET("/gstr1", inv.GSTR1Export)
		}

		customers := api.Group("/customers")
		{
			cust := handlers.NewCustomerHandler()
			customers.GET("", cust.List)
			customers.GET("/:id", cust.Get)
			customers.POST("", authMw.RequireAuth(), cust.Create)
			customers.PUT("/:id", authMw.RequireAuth(), authMw.RequireRole("owner", "manager"), cust.Update)
		}

		credit := api.Group("/credit")
		{
			cre := handlers.NewCreditHandler()
			credit.GET("", cre.List)
			credit.GET("/:id", cre.Get)
			credit.POST("/payment", authMw.RequireAuth(), cre.Payment)
			credit.GET("/overdue", cre.Overdue)
		}

		vendors := api.Group("/vendors")
		{
			v := handlers.NewVendorHandler()
			vendors.GET("", v.List)
			vendors.GET("/:id", v.Get)
			vendors.POST("", authMw.RequireAuth(), authMw.RequireRole("owner", "manager"), v.Create)
		}

		purchases := api.Group("/purchases")
		{
			p := handlers.NewPurchaseHandler()
			purchases.GET("", p.List)
			purchases.GET("/:id", p.Get)
			purchases.POST("", authMw.RequireAuth(), authMw.RequireRole("owner", "manager", "warehouse"), p.Create)
			purchases.POST("/receive", authMw.RequireAuth(), authMw.RequireRole("owner", "manager", "warehouse"), p.Receive)
			purchases.POST("/cancel/:id", authMw.RequireAuth(), authMw.RequireRole("owner", "manager", "warehouse"), p.Cancel)
		}

		analytics := api.Group("/analytics")
		{
			a := handlers.NewAnalyticsHandler()
			analytics.GET("/dashboard", a.Dashboard)
			analytics.GET("/sales/category", a.SalesByCategory)
			analytics.GET("/sales/top", a.TopProducts)
			analytics.GET("/sales/hourly", a.HourlySales)
			analytics.GET("/sales/payment", a.PaymentModeBreakup)
		}

		audit := api.Group("/audit")
		{
			audit.GET("", handlers.NewAuditHandler().List)
			audit.POST("", authMw.RequireAuth(), authMw.RequireRole("owner", "manager"), handlers.NewAuditHandler().Log)
		}

		stores := api.Group("/stores")
		{
			s := handlers.NewStoreHandler()
			stores.GET("", s.List)
			stores.GET("/:id", s.Get)
			stores.POST("", authMw.RequireAuth(), authMw.RequireRole("owner"), s.Create)
			stores.PUT("/:id", authMw.RequireAuth(), authMw.RequireRole("owner", "manager"), s.Update)
		}

		uoms := api.Group("/uoms")
		{
			u := handlers.NewUOMHandler()
			uoms.GET("", u.List)
			uoms.POST("", authMw.RequireAuth(), authMw.RequireRole("owner", "manager"), u.Create)
		}

		barcodes := api.Group("/barcodes")
		{
			b := handlers.NewBarcodeHandler()
			barcodes.POST("/generate", authMw.RequireAuth(), authMw.RequireRole("owner", "manager"), b.Generate)
			barcodes.GET("/lookup/:barcode", b.Lookup)
		}

		expiry := api.Group("/expiry")
		{
			e := handlers.NewExpiryHandler()
			expiry.GET("/expiring", e.GetExpiring)
			expiry.GET("/expired", e.GetExpired)
		}

		payments := api.Group("/payments")
		{
			p := handlers.NewPaymentHandler()
			payments.POST("/split", authMw.RequireAuth(), p.AddSplitPayments)
			payments.GET("/invoice/:id", p.GetInvoicePayments)
			payments.GET("/settlement", p.GetSettlementReport)
		}

		returns := api.Group("/returns")
		{
			r := handlers.NewReturnHandler()
			returns.GET("", r.List)
			returns.POST("", authMw.RequireAuth(), r.Create)
			returns.POST("/:id/approve", authMw.RequireAuth(), authMw.RequireRole("owner", "manager"), r.Approve)
		}

		losses := api.Group("/losses")
		{
			l := handlers.NewLossHandler()
			losses.GET("", l.List)
			losses.POST("", authMw.RequireAuth(), authMw.RequireRole("owner", "manager", "warehouse"), l.Create)
		}

		grn := api.Group("/grn")
		{
			g := handlers.NewGRNHandler()
			grn.GET("", g.List)
			grn.GET("/:id", g.Get)
			grn.POST("", authMw.RequireAuth(), authMw.RequireRole("owner", "manager", "warehouse"), g.Create)
		}

		reports := api.Group("/reports")
		{
			r := handlers.NewReportHandler()
			reports.GET("/profit-loss", r.GetProfitLoss)
			reports.GET("/credit-aging", r.GetCreditAging)
			reports.GET("/gstr-3b", r.GetGSTR3B)
		}

		expenses := api.Group("/expenses")
		{
			e := handlers.NewExpenseHandler()
			expenses.GET("", e.List)
			expenses.GET("/categories", e.GetCategories)
			expenses.GET("/summary", e.GetSummary)
			expenses.POST("", authMw.RequireAuth(), authMw.RequireRole("owner", "manager"), e.Create)
		}
	}

	log.Printf("Starting GBOS server on port %s", cfg.ServerPort)
	r.Run(":" + cfg.ServerPort)
}