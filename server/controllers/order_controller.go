package controllers

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"boko/config"
	"boko/models"
)

// ==================== ORDER ====================

// CreateOrder — tạo đơn hàng từ giỏ hàng (dùng transaction)
func CreateOrder(c *gin.Context) {
	userID, _ := c.Get("user_id")

	var input struct {
		ShippingAddress string `json:"shipping_address" binding:"required"`
		Phone           string `json:"phone" binding:"required"`
		PaymentMethod   string `json:"payment_method"` // cod | momo
		CouponCode      string `json:"coupon_code"`
	}

	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Dữ liệu không hợp lệ: " + err.Error()})
		return
	}

	if input.PaymentMethod == "" {
		input.PaymentMethod = "cod"
	}

	// ===== TRANSACTION =====
	err := config.DB.Transaction(func(tx *gorm.DB) error {
		// 1. Lấy giỏ hàng
		type cartItem struct {
			CartID   uint
			BookID   uint
			Title    string
			Price    float64
			Quantity int
			Stock    int
			SellerID uint
		}

		var cartItems []cartItem
		tx.Table("carts").
			Select("carts.id as cart_id, carts.book_id, books.title, books.price, carts.quantity, books.stock, books.user_id as seller_id").
			Joins("join books on books.id = carts.book_id").
			Where("carts.user_id = ?", userID).
			Scan(&cartItems)

		if len(cartItems) == 0 {
			return errors.New("Giỏ hàng trống")
		}

		// 2. Kiểm tra tồn kho
		var total float64
		for _, item := range cartItems {
			if item.Quantity > item.Stock {
				return errors.New("Sách \"" + item.Title + "\" không đủ tồn kho")
			}
			total += item.Price * float64(item.Quantity)
		}

		// 3. Kiểm tra & áp dụng coupon nếu có
		discountPercent := 0
		if input.CouponCode != "" {
			var coupon models.Coupon
			if result := tx.Where("code = ? AND is_active = ?", input.CouponCode, true).First(&coupon); result.Error != nil {
				return errors.New("Mã giảm giá không hợp lệ")
			}
			if time.Now().After(coupon.ExpiresAt) {
				return errors.New("Mã giảm giá đã hết hạn")
			}
			if coupon.UsedCount >= coupon.MaxUses {
				return errors.New("Mã giảm giá đã hết lượt sử dụng")
			}
			discountPercent = coupon.DiscountPercent

			// Tăng UsedCount
			tx.Model(&coupon).Update("used_count", coupon.UsedCount+1)
		}

		// Tính tổng sau giảm giá
		finalTotal := total * (100 - float64(discountPercent)) / 100

		// 4. Tạo đơn hàng
		order := models.Order{
			UserID:          userID.(uint),
			Total:           finalTotal,
			Status:          "pending",
			ShippingAddress: input.ShippingAddress,
			Phone:           input.Phone,
			PaymentMethod:   input.PaymentMethod,
			CouponCode:      input.CouponCode,
			DiscountPercent: discountPercent,
			CreatedAt:       time.Now(),
		}
		if err := tx.Create(&order).Error; err != nil {
			return err
		}

		// 5. Tạo OrderItem + trừ stock
		for _, item := range cartItems {
			orderItem := models.OrderItem{
				OrderID:  order.ID,
				BookID:   item.BookID,
				Title:    item.Title,
				Price:    item.Price,
				Quantity: item.Quantity,
			}
			if err := tx.Create(&orderItem).Error; err != nil {
				return err
			}

			// Trừ tồn kho
			if err := tx.Model(&models.Book{}).Where("id = ?", item.BookID).
				Update("stock", gorm.Expr("stock - ?", item.Quantity)).Error; err != nil {
				return err
			}
		}

		// 6. Xoá giỏ hàng
		if err := tx.Where("user_id = ?", userID).Delete(&models.Cart{}).Error; err != nil {
			return err
		}

		return nil
	})
	// ===== END TRANSACTION =====

	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// Lấy đơn vừa tạo
	var order models.Order
	config.DB.Preload("Items").Where("user_id = ?", userID).Order("id desc").First(&order)

	c.JSON(http.StatusCreated, gin.H{
		"message":        "Đặt hàng thành công!",
		"order_id":       order.ID,
		"total":          order.Total,
		"status":         order.Status,
		"payment_method": order.PaymentMethod,
		"payment_status": order.PaymentStatus,
	})
}

// GetMyOrders — lịch sử đơn hàng của user (chỉ dùng user_id từ JWT)
func GetMyOrders(c *gin.Context) {
	value, exists := c.Get("user_id")
	uid, ok := value.(uint)
	if !exists || !ok || uid == 0 {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Yêu cầu đăng nhập"})
		return
	}

	var orders []models.Order
	if err := config.DB.Preload("Items").
		Where("user_id = ?", uid).
		Order("created_at DESC").Find(&orders).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Không thể tải đơn hàng"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": orders})
}

// applyCouponAtomic — Áp dụng và cập nhật quota coupon nguyên tử trong Transaction
func applyCouponAtomic(tx *gorm.DB, code string) (int, error) {
	cleanCode := strings.TrimSpace(code)
	if cleanCode == "" {
		return 0, nil
	}
	var coupon models.Coupon
	if err := tx.Where("code = ? AND is_active = ?", cleanCode, true).First(&coupon).Error; err != nil {
		return 0, fmt.Errorf("Mã giảm giá \"%s\" không tồn tại hoặc đã bị khóa", cleanCode)
	}
	if time.Now().After(coupon.ExpiresAt) {
		return 0, fmt.Errorf("Mã giảm giá \"%s\" đã hết hạn sử dụng", cleanCode)
	}
	res := tx.Model(&coupon).
		Where("id = ? AND used_count < max_uses AND is_active = ? AND expires_at > ?", coupon.ID, true, time.Now()).
		Update("used_count", gorm.Expr("used_count + 1"))
	if res.Error != nil {
		return 0, res.Error
	}
	if res.RowsAffected == 0 {
		return 0, fmt.Errorf("Mã giảm giá \"%s\" đã hết lượt sử dụng", cleanCode)
	}
	return coupon.DiscountPercent, nil
}

// GetOrderQuote — Báo giá chuẩn từ Backend trước khi người mua chốt đơn hàng
func GetOrderQuote(c *gin.Context) {
	var input struct {
		CouponCode string `json:"coupon_code"`
		Items      []struct {
			BookID   uint `json:"book_id" binding:"required,gt=0"`
			Quantity int  `json:"quantity" binding:"required,gt=0"`
		} `json:"items" binding:"required,min=1,dive"`
	}

	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Dữ liệu báo giá không hợp lệ: " + err.Error()})
		return
	}

	var subtotal float64
	for _, it := range input.Items {
		if it.Quantity <= 0 || it.BookID == 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Mặt hàng hoặc số lượng không hợp lệ"})
			return
		}
		var book models.Book
		if err := config.DB.First(&book, it.BookID).Error; err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("Không tìm thấy sách ID %d", it.BookID)})
			return
		}
		if it.Quantity > book.Stock {
			c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("Sách \"%s\" không đủ tồn kho (Còn lại: %d, yêu cầu: %d)", book.Title, book.Stock, it.Quantity)})
			return
		}
		subtotal += book.Price * float64(it.Quantity)
	}

	discountPercent := 0
	cleanCode := strings.TrimSpace(input.CouponCode)
	if cleanCode != "" {
		var coupon models.Coupon
		if err := config.DB.Where("code = ? AND is_active = ?", cleanCode, true).First(&coupon).Error; err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("Mã giảm giá \"%s\" không tồn tại hoặc đã bị khóa", cleanCode)})
			return
		}
		if time.Now().After(coupon.ExpiresAt) {
			c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("Mã giảm giá \"%s\" đã hết hạn sử dụng", cleanCode)})
			return
		}
		if coupon.UsedCount >= coupon.MaxUses {
			c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("Mã giảm giá \"%s\" đã hết lượt sử dụng", cleanCode)})
			return
		}
		discountPercent = coupon.DiscountPercent
	}

	discountAmount := subtotal * float64(discountPercent) / 100
	finalTotal := subtotal - discountAmount

	c.JSON(http.StatusOK, gin.H{
		"subtotal":         subtotal,
		"discount_percent": discountPercent,
		"discount_amount":  discountAmount,
		"total":            finalTotal,
		"coupon_code":      cleanCode,
	})
}

// CreateCodOrder — Đặt hàng COD: Kiểm tra tồn kho, tính lại giá từ DB và trừ kho trong Transaction
func CreateCodOrder(c *gin.Context) {
	idempotencyKey := strings.TrimSpace(c.GetHeader("X-Idempotency-Key"))
	if idempotencyKey != "" {
		var existingOrders []models.Order
		if err := config.DB.Preload("Items").Where("idempotency_key = ?", idempotencyKey).Limit(1).Find(&existingOrders).Error; err == nil && len(existingOrders) > 0 {
			existingOrder := existingOrders[0]
			c.JSON(http.StatusOK, gin.H{
				"message":        "Đơn hàng đã được tạo trước đó (Idempotency Key khớp)",
				"order_id":       existingOrder.ID,
				"order":          existingOrder,
				"total":          existingOrder.Total,
				"status":         existingOrder.Status,
				"payment_method": existingOrder.PaymentMethod,
				"payment_status": existingOrder.PaymentStatus,
			})
			return
		}
	}

	var input struct {
		Amount          float64 `json:"amount"`
		ShippingAddress string  `json:"shipping_address" binding:"required"`
		Phone           string  `json:"phone" binding:"required"`
		Email           string  `json:"email"`
		CustomerName    string  `json:"customer_name"`
		CouponCode      string  `json:"coupon_code"`
		Items           []struct {
			BookID   uint    `json:"book_id" binding:"required,gt=0"`
			Title    string  `json:"title"`
			Price    float64 `json:"price"`
			Quantity int     `json:"quantity" binding:"required,gt=0"`
		} `json:"items" binding:"required,min=1,dive"`
	}

	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Dữ liệu không hợp lệ: " + err.Error()})
		return
	}

	// Validate dữ liệu địa chỉ và điện thoại không được chỉ có khoảng trắng
	cleanAddress := strings.TrimSpace(input.ShippingAddress)
	cleanPhone := strings.TrimSpace(input.Phone)
	if cleanAddress == "" || cleanPhone == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Địa chỉ giao hàng và số điện thoại không được để trống"})
		return
	}

	// 1. Xác định User ID
	var currentUserID uint
	if val, exists := c.Get("user_id"); exists {
		if uid, ok := val.(uint); ok {
			currentUserID = uid
		}
	}
	if currentUserID == 0 {
		targetEmail := strings.TrimSpace(input.Email)
		if targetEmail == "" {
			targetEmail = "guest@boko.com"
		}
		var user models.User
		if err := config.DB.Where("email = ?", targetEmail).First(&user).Error; err != nil {
			name := strings.TrimSpace(input.CustomerName)
			if name == "" {
				name = "Khách mua hàng"
			}
			user = models.User{
				Email: targetEmail,
				Name:  name,
				Role:  "customer",
			}
			config.DB.Create(&user)
		}
		currentUserID = user.ID
	}

	var createdOrder models.Order

	// 2. TRANSACTION: KIỂM TRA TỒN KHO, TÍNH LẠI GIÁ VÀ TRỪ KHO NGUYÊN TỬ
	err := config.DB.Transaction(func(tx *gorm.DB) error {
		type itemValidation struct {
			BookID   uint
			Title    string
			Price    float64
			Quantity int
		}
		var validatedItems []itemValidation
		var calculatedSubtotal float64

		// Duyệt từng mặt hàng từ DB
		for _, it := range input.Items {
			if it.BookID == 0 || it.Quantity <= 0 {
				return fmt.Errorf("Mặt hàng ID %d hoặc số lượng (%d) không hợp lệ", it.BookID, it.Quantity)
			}

			var book models.Book
			if err := tx.First(&book, it.BookID).Error; err != nil {
				return fmt.Errorf("Không tìm thấy sách ID %d", it.BookID)
			}

			// Chặn nếu đặt vượt số lượng tồn kho
			if it.Quantity > book.Stock {
				return fmt.Errorf("Sách \"%s\" không đủ tồn kho (Còn lại: %d, yêu cầu: %d)", book.Title, book.Stock, it.Quantity)
			}

			calculatedSubtotal += book.Price * float64(it.Quantity)
			validatedItems = append(validatedItems, itemValidation{
				BookID:   book.ID,
				Title:    book.Title,
				Price:    book.Price,
				Quantity: it.Quantity,
			})
		}

		// Áp dụng Coupon nguyên tử
		discountPercent, err := applyCouponAtomic(tx, input.CouponCode)
		if err != nil {
			return err
		}

		// Tính tổng tiền an toàn từ DB (Chống Price Tampering D05)
		finalTotal := calculatedSubtotal * float64(100-discountPercent) / 100

		// Tạo đơn hàng COD với cờ StockDeducted = true
		order := models.Order{
			UserID:          currentUserID,
			Total:           finalTotal,
			Status:          "shipping", // Quyết định demo Stepper
			PaymentStatus:   "unpaid",
			PaymentMethod:   "cod",
			ShippingAddress: cleanAddress,
			Phone:           cleanPhone,
			CouponCode:      strings.TrimSpace(input.CouponCode),
			DiscountPercent: discountPercent,
			StockDeducted:   true,
			IdempotencyKey:  idempotencyKey,
			CreatedAt:       time.Now(),
		}
		if err := tx.Create(&order).Error; err != nil {
			return err
		}

		// Lưu OrderItems và TRỪ TỒN KHO NGUYÊN TỬ
		for _, it := range validatedItems {
			orderItem := models.OrderItem{
				OrderID:  order.ID,
				BookID:   it.BookID,
				Title:    it.Title,
				Price:    it.Price,
				Quantity: it.Quantity,
			}
			if err := tx.Create(&orderItem).Error; err != nil {
				return err
			}

			// Trừ kho có điều kiện stock >= quantity
			res := tx.Model(&models.Book{}).
				Where("id = ? AND stock >= ?", it.BookID, it.Quantity).
				Update("stock", gorm.Expr("stock - ?", it.Quantity))
			if res.Error != nil {
				return res.Error
			}
			if res.RowsAffected == 0 {
				return fmt.Errorf("Sách \"%s\" vừa hết hàng trong kho", it.Title)
			}
		}

		createdOrder = order
		return nil
	})

	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	config.DB.Preload("Items").First(&createdOrder, createdOrder.ID)
	c.JSON(http.StatusCreated, gin.H{
		"message":        "Đặt hàng COD thành công!",
		"order_id":       createdOrder.ID,
		"order":          createdOrder,
		"total":          createdOrder.Total,
		"status":         createdOrder.Status,
		"payment_method": createdOrder.PaymentMethod,
		"payment_status": createdOrder.PaymentStatus,
	})
}

// ConfirmReceiptOrder — Người mua xác nhận đã nhận hàng COD thành công
func ConfirmReceiptOrder(c *gin.Context) {
	orderID := c.Param("id")

	// 1. BẮT BUỘC ĐĂNG NHẬP (Chống D09a-anonymous: không token không được thao tác)
	val, exists := c.Get("user_id")
	if !exists {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Yêu cầu đăng nhập để xác nhận nhận hàng"})
		return
	}
	uid, ok := val.(uint)
	if !ok || uid == 0 {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Token người dùng không hợp lệ"})
		return
	}

	var order models.Order
	if err := config.DB.Preload("Items").First(&order, orderID).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Không tìm thấy đơn hàng"})
		return
	}

	// 2. KIỂM TRA QUYỀN SỞ HỮU (Chủ sở hữu hoặc Admin)
	role, _ := c.Get("role")
	if order.UserID != uid && role != "admin" {
		c.JSON(http.StatusForbidden, gin.H{"error": "Bạn không có quyền xác nhận đơn hàng của người khác"})
		return
	}

	// 3. STATE GUARD (PR05 / D08): Chỉ cho phép receipt khi đơn đang shipping
	if order.Status != "shipping" {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": fmt.Sprintf("Không thể xác nhận đơn hàng đang ở trạng thái \"%s\". Đơn hàng phải đang ở trạng thái \"shipping\"", order.Status),
		})
		return
	}

	// 4. PAYMENT GUARD: Đơn hàng online chưa thanh toán (unpaid) cấm receipt tự đổi thành paid
	if order.PaymentMethod != "cod" && order.PaymentStatus != "paid" {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "Đơn hàng thanh toán trực tuyến chưa hoàn tất thanh toán, không thể xác nhận nhận hàng",
		})
		return
	}

	// 5. Cập nhật trạng thái hoàn thành an toàn
	order.Status = "completed"
	if order.PaymentMethod == "cod" {
		order.PaymentStatus = "paid"
	}
	config.DB.Save(&order)

	c.JSON(http.StatusOK, gin.H{
		"message":        "Xác nhận nhận hàng thành công!",
		"order":          order,
		"status":         order.Status,
		"payment_status": order.PaymentStatus,
	})
}

// GetOrderDetail — chi tiết 1 đơn hàng
func GetOrderDetail(c *gin.Context) {
	value, exists := c.Get("user_id")
	uid, ok := value.(uint)
	if !exists || !ok || uid == 0 {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Yêu cầu đăng nhập"})
		return
	}
	orderID := c.Param("id")

	var order models.Order
	if result := config.DB.Preload("Items").
		Where("id = ? AND user_id = ?", orderID, uid).
		First(&order); result.Error != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Không tìm thấy đơn hàng"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"data": order})
}

// CancelOrder — huỷ đơn hàng bởi khách hàng (đồng nhất với quy tắc StockDeducted và khóa FOR UPDATE)
func CancelOrder(c *gin.Context) {
	val, exists := c.Get("user_id")
	if !exists {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Yêu cầu đăng nhập"})
		return
	}
	userID, ok := val.(uint)
	if !ok || userID == 0 {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Token không hợp lệ"})
		return
	}
	orderID := c.Param("id")

	err := config.DB.Transaction(func(tx *gorm.DB) error {
		var order models.Order
		if err := tx.Set("gorm:query_option", "FOR UPDATE").
			Preload("Items").
			Where("id = ? AND user_id = ?", orderID, userID).
			First(&order).Error; err != nil {
			return fmt.Errorf("NOT_FOUND")
		}

		// Chỉ cho phép hủy pending, confirmed, shipping. Cấm completed và cancelled
		if order.Status == "completed" || order.Status == "cancelled" {
			return fmt.Errorf("INVALID_STATUS: Đơn hàng đã ở trạng thái %s", order.Status)
		}

		if err := tx.Model(&order).Update("status", "cancelled").Error; err != nil {
			return err
		}

		// CHỈ HOÀN KHO KHI ĐƠN ĐÃ TRỪ KHO VÀ CHƯA TỪNG ĐƯỢC HOÀN (StockDeducted == true)
		if order.StockDeducted {
			if err := tx.Model(&order).Update("stock_deducted", false).Error; err != nil {
				return err
			}

			for _, it := range order.Items {
				res := tx.Model(&models.Book{}).Where("id = ?", it.BookID).
					Update("stock", gorm.Expr("stock + ?", it.Quantity))
				if res.Error != nil {
					return res.Error
				}
				if res.RowsAffected == 0 {
					return fmt.Errorf("Sách ID %d không còn tồn tại để hoàn kho", it.BookID)
				}
			}
		}
		return nil
	})

	if err != nil {
		if err.Error() == "NOT_FOUND" {
			c.JSON(http.StatusNotFound, gin.H{"error": "Không tìm thấy đơn hàng"})
			return
		}
		if strings.HasPrefix(err.Error(), "INVALID_STATUS") {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Không thể hủy đơn hàng đã hoàn tất hoặc đã bị hủy"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Lỗi khi hủy đơn hàng: " + err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "Đã hủy đơn hàng thành công!", "status": "cancelled"})
}

// VerifyAtmOtpPayment — Xác thực OTP Thẻ ATM Nội Địa NCB 1-chạm và hoàn tất thanh toán (Vá D03/D05)
func VerifyAtmOtpPayment(c *gin.Context) {
	idempotencyKey := strings.TrimSpace(c.GetHeader("X-Idempotency-Key"))
	if idempotencyKey != "" {
		var existingOrders []models.Order
		if err := config.DB.Preload("Items").Where("idempotency_key = ?", idempotencyKey).Limit(1).Find(&existingOrders).Error; err == nil && len(existingOrders) > 0 {
			existingOrder := existingOrders[0]
			c.JSON(http.StatusOK, gin.H{
				"success":        true,
				"message":        "Đơn hàng ATM đã được xác nhận trước đó (Idempotency Key khớp)",
				"order_id":       existingOrder.ID,
				"transaction_id": existingOrder.PaymentTransID,
				"total":          existingOrder.Total,
				"payment_status": existingOrder.PaymentStatus,
				"status":         existingOrder.Status,
			})
			return
		}
	}

	var input struct {
		Amount          float64 `json:"amount"`
		CardNumber      string  `json:"card_number"`
		CardHolder      string  `json:"card_holder"`
		BankName        string  `json:"bank_name"`
		Otp             string  `json:"otp"`
		ShippingAddress string  `json:"shipping_address" binding:"required"`
		Phone           string  `json:"phone" binding:"required"`
		Email           string  `json:"email"`
		CustomerName    string  `json:"customer_name"`
		CouponCode      string  `json:"coupon_code"`
		Items           []struct {
			BookID   uint    `json:"book_id" binding:"required,gt=0"`
			Title    string  `json:"title"`
			Price    float64 `json:"price"`
			Quantity int     `json:"quantity" binding:"required,gt=0"`
		} `json:"items" binding:"required,min=1,dive"`
	}

	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Dữ liệu không hợp lệ: " + err.Error()})
		return
	}

	// 1. Kiểm tra địa chỉ và SĐT không được chỉ có khoảng trắng
	cleanAddress := strings.TrimSpace(input.ShippingAddress)
	cleanPhone := strings.TrimSpace(input.Phone)
	if cleanAddress == "" || cleanPhone == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Địa chỉ giao hàng và số điện thoại không được để trống"})
		return
	}

	// 2. Kiểm tra mã OTP: Sandbox test OTP là 123456
	if input.Otp != "123456" {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "Mã xác thực OTP không chính xác. Vui lòng nhập mã OTP test: 123456",
		})
		return
	}

	// 3. Xác định user_id
	var currentUserID uint
	if val, exists := c.Get("user_id"); exists {
		if uid, ok := val.(uint); ok {
			currentUserID = uid
		}
	}

	if currentUserID == 0 {
		targetEmail := strings.TrimSpace(input.Email)
		if targetEmail == "" {
			targetEmail = "guest@boko.com"
		}
		var user models.User
		if err := config.DB.Where("email = ?", targetEmail).First(&user).Error; err != nil {
			name := strings.TrimSpace(input.CustomerName)
			if name == "" {
				name = "Khách mua hàng"
			}
			user = models.User{
				Email: targetEmail,
				Name:  name,
				Role:  "customer",
			}
			config.DB.Create(&user)
		}
		currentUserID = user.ID
	}

	transID := fmt.Sprintf("NCB_OTP_%d", time.Now().Unix())
	var createdOrder models.Order

	// 4. TRANSACTION: KIỂM TRA TỒN KHO, TÍNH GIÁ CHUẨN DB VÀ TRỪ KHO NGUYÊN TỬ
	err := config.DB.Transaction(func(tx *gorm.DB) error {
		type itemValidation struct {
			BookID   uint
			Title    string
			Price    float64
			Quantity int
		}

		var validatedItems []itemValidation
		var calculatedSubtotal float64

		// Duyệt từng sách từ DB để lấy GIÁ THẬT và KIỂM TRA TỒN KHO (Chống D05-ATM)
		for _, it := range input.Items {
			if it.Quantity <= 0 || it.BookID == 0 {
				return fmt.Errorf("Mặt hàng ID %d hoặc số lượng (%d) không hợp lệ", it.BookID, it.Quantity)
			}

			var book models.Book
			if err := tx.First(&book, it.BookID).Error; err != nil {
				return fmt.Errorf("Không tìm thấy sách ID %d", it.BookID)
			}

			// Chặn nếu đặt vượt tồn kho (Khắc phục D03-ATM)
			if it.Quantity > book.Stock {
				return fmt.Errorf("Sách \"%s\" không đủ tồn kho (Còn lại: %d, yêu cầu: %d)", book.Title, book.Stock, it.Quantity)
			}

			// LẤY GIÁ TỪ DB ĐỂ TÍNH TIỀN (Khắc phục triệt để D05-ATM)
			calculatedSubtotal += book.Price * float64(it.Quantity)
			validatedItems = append(validatedItems, itemValidation{
				BookID:   book.ID,
				Title:    book.Title,
				Price:    book.Price, // Giá chuẩn từ DB
				Quantity: it.Quantity,
			})
		}

		// Áp dụng coupon nguyên tử nếu có
		discountPercent, err := applyCouponAtomic(tx, input.CouponCode)
		if err != nil {
			return err
		}

		finalTotal := calculatedSubtotal * float64(100-discountPercent) / 100

		// Tạo đơn hàng với giá chuẩn từ DB, cờ StockDeducted = true
		order := models.Order{
			UserID:          currentUserID,
			Total:           finalTotal,
			Status:          "shipping",
			PaymentStatus:   "paid",
			PaymentMethod:   "atm",
			PaymentTransID:  transID,
			PaymentOrderID:  transID,
			ShippingAddress: cleanAddress,
			Phone:           cleanPhone,
			CouponCode:      strings.TrimSpace(input.CouponCode),
			DiscountPercent: discountPercent,
			StockDeducted:   true,
			IdempotencyKey:  idempotencyKey,
			CreatedAt:       time.Now(),
		}

		if err := tx.Create(&order).Error; err != nil {
			return fmt.Errorf("Không thể tạo đơn hàng: %v", err)
		}

		// Lưu OrderItems với GIÁ CHUẨN DB và TRỪ KHO NGUYÊN TỬ
		for _, it := range validatedItems {
			orderItem := models.OrderItem{
				OrderID:  order.ID,
				BookID:   it.BookID,
				Title:    it.Title,
				Price:    it.Price, // Giá chuẩn từ DB
				Quantity: it.Quantity,
			}
			if err := tx.Create(&orderItem).Error; err != nil {
				return fmt.Errorf("Không thể lưu chi tiết đơn hàng: %v", err)
			}

			// Trừ kho có điều kiện nguyên tử stock >= quantity
			res := tx.Model(&models.Book{}).
				Where("id = ? AND stock >= ?", it.BookID, it.Quantity).
				Update("stock", gorm.Expr("stock - ?", it.Quantity))
			if res.Error != nil {
				return res.Error
			}
			if res.RowsAffected == 0 {
				return fmt.Errorf("Sách \"%s\" vừa hết hàng trong kho", it.Title)
			}
		}

		createdOrder = order
		return nil
	})

	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"success":        true,
		"message":        "Xác thực OTP thành công! Đơn hàng đã được thanh toán qua Thẻ ATM NCB.",
		"order_id":       createdOrder.ID,
		"transaction_id": transID,
		"total":          createdOrder.Total,
		"payment_status": createdOrder.PaymentStatus,
		"status":         createdOrder.Status,
	})
}

