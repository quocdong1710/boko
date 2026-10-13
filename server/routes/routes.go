package routes

import (
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"boko/controllers"
	"boko/middleware"
	pmiddleware "boko/payments/middleware"
)

func SetupRoutes(r *gin.Engine) {
	// ==================== PUBLIC ROUTES ====================

	r.GET("/", func(c *gin.Context) {
		c.JSON(200, gin.H{"message": "Boko API is running! 🚀"})
	})

	// Rate limit riêng cho nhóm Auth (áp dụng chung cho cả alias /api/* và /api/auth/*)
	authRateLimit := 10
	if v := strings.TrimSpace(os.Getenv("AUTH_RATE_LIMIT")); v != "" {
		if parsed, err := strconv.Atoi(v); err == nil && parsed > 0 {
			authRateLimit = parsed
		}
	}
	loginRateLimiter := pmiddleware.RateLimitMiddleware(authRateLimit, 1*time.Minute, func(c *gin.Context) string {
		return "login:" + c.ClientIP()
	})
	registerRateLimiter := pmiddleware.RateLimitMiddleware(authRateLimit, 1*time.Minute, func(c *gin.Context) string {
		return "register:" + c.ClientIP()
	})

	// Auth — frontend tương thích
	r.POST("/api/register", registerRateLimiter, controllers.Register)
	r.POST("/api/login", loginRateLimiter, controllers.Login)

	// Frontend gọi /api/auth/* (alias cho tương thích)
	r.POST("/api/auth/login", loginRateLimiter, controllers.Login)
	r.POST("/api/auth/register", registerRateLimiter, controllers.Register)
	r.POST("/api/auth/logout", controllers.Logout)

	// Books (public)
	r.GET("/api/books", controllers.GetBooks)
	r.GET("/api/books/:id", controllers.GetBook)

	// Categories (public)
	r.GET("/api/categories", controllers.GetCategories)
	r.GET("/api/categories/:id", controllers.GetCategory)

	// Reviews (public)
	r.GET("/api/books/:id/reviews", controllers.GetBookReviews)

	// Payments Webhook & Status
	r.POST("/api/payment/momo/ipn", controllers.MomoIPN)
	r.GET("/api/payment/momo/status/:id", middleware.AuthRequired, controllers.GetPaymentStatus)

	// Payments Create (hỗ trợ cả người dùng đăng nhập và khách)
	r.POST("/api/payment/momo/create", middleware.AuthOptional, controllers.CreateMomoPayment)

	// VNPAY Payment Routes
	r.GET("/api/payment/vnpay/ipn", controllers.VnPayIPN)
	r.GET("/api/payment/vnpay/status/:id", middleware.AuthRequired, controllers.GetVnPayPaymentStatus)
	r.POST("/api/payment/vnpay/create", middleware.AuthOptional, controllers.CreateVnPayPayment)

	// PayPal Payment Routes — cùng cấu trúc với MoMo/VNPay
	r.GET("/api/payment/paypal/status/:id", middleware.AuthRequired, controllers.GetPaypalPaymentStatus)
	r.POST("/api/payment/paypal/create", middleware.AuthOptional, controllers.CreatePaypalPayment)
	r.POST("/api/payment/paypal/capture", middleware.AuthRequired, controllers.CapturePaypalPayment)

	// Mock / Simulator Payment Routes (chỉ bật khi APP_ENV=demo và ENABLE_PAYMENT_MOCK=true, bắt buộc quyền Admin)
	mockEnabled := strings.EqualFold(os.Getenv("APP_ENV"), "demo") &&
		strings.EqualFold(os.Getenv("ENABLE_PAYMENT_MOCK"), "true")
	if mockEnabled {
		r.POST("/api/payment/momo/mock-ipn/:id",
			middleware.AuthRequired, middleware.AdminRequired, controllers.MockMomoIPN)
		r.POST("/api/payment/paypal/mock-capture/:id",
			middleware.AuthRequired, middleware.AdminRequired, controllers.MockPaypalCapture)
		r.POST("/api/payment/momo/simulator-ipn",
			middleware.AuthRequired, middleware.AdminRequired, controllers.MomoSimulatorIPN)
	}

	// COD & Order Tracking Routes (hỗ trợ cả khách đăng nhập và khách vãng lai)
	r.POST("/api/orders/quote", controllers.GetOrderQuote)
	r.POST("/api/orders/cod/create", middleware.AuthOptional, controllers.CreateCodOrder)
	r.POST("/api/orders/:id/confirm-receipt", middleware.AuthRequired, controllers.ConfirmReceiptOrder)
	r.GET("/api/orders/my-orders", middleware.AuthRequired, controllers.GetMyOrders)

	// Thẻ ATM Nội Địa NCB — Xác thực OTP 1-chạm
	r.POST("/api/payment/atm/verify-otp", middleware.AuthOptional, controllers.VerifyAtmOtpPayment)

	// ==================== PROTECTED ROUTES ====================

	auth := r.Group("/api")
	auth.Use(middleware.AuthRequired)
	{
		// Profile — frontend tương thích
		auth.GET("/profile", controllers.GetProfile)
		auth.PUT("/profile", controllers.UpdateProfile)
		auth.GET("/auth/me", controllers.GetProfile)         // alias frontend
		auth.PUT("/auth/profile", controllers.UpdateProfile) // alias frontend

		// Books (seller)
		auth.POST("/books", middleware.SellerRequired, controllers.CreateBook)
		auth.PUT("/books/:id", middleware.SellerRequired, controllers.UpdateBook)
		auth.DELETE("/books/:id", middleware.SellerRequired, controllers.DeleteBook)

		// Categories (admin)
		auth.POST("/categories", middleware.AdminRequired, controllers.CreateCategory)
		auth.PUT("/categories/:id", middleware.AdminRequired, controllers.UpdateCategory)
		auth.DELETE("/categories/:id", middleware.AdminRequired, controllers.DeleteCategory)

		// Cart
		auth.GET("/cart", controllers.GetCart)
		auth.POST("/cart", controllers.AddToCart)
		auth.PUT("/cart/:id", controllers.UpdateCartItem)
		auth.DELETE("/cart/:id", controllers.RemoveFromCart)

		// Orders
		auth.POST("/orders", controllers.CreateOrder)
		auth.GET("/orders", controllers.GetMyOrders)
		auth.GET("/orders/:id", controllers.GetOrderDetail)
		auth.PUT("/orders/:id/cancel", controllers.CancelOrder)

		// Reviews
		auth.POST("/books/:id/reviews", controllers.CreateReview)
		auth.DELETE("/reviews/:id", controllers.DeleteReview)

		// Coupons
		auth.POST("/coupons", middleware.SellerRequired, controllers.CreateCoupon)
		auth.POST("/apply-coupon", controllers.ApplyCoupon)
		auth.GET("/coupons", controllers.GetCoupons)
		auth.DELETE("/coupons/:id", middleware.SellerRequired, controllers.DeleteCoupon)

		// Seller Dashboard
		auth.GET("/seller/books", middleware.SellerRequired, controllers.SellerGetBooks)
		auth.GET("/seller/orders", middleware.SellerRequired, controllers.SellerGetOrders)
		auth.PUT("/seller/orders/:id/status", middleware.SellerRequired, controllers.SellerUpdateOrderStatus)
	}
}
