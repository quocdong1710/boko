package controllers

import (
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"boko/config"
	"boko/models"
	"boko/payments"
	"boko/services"
)

// ==================== PAYMENT CONTROLLER ====================

func loadOwnedPaymentOrder(c *gin.Context) (models.Order, bool) {
	value, exists := c.Get("user_id")
	uid, ok := value.(uint)
	if !exists || !ok || uid == 0 {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Yêu cầu đăng nhập"})
		return models.Order{}, false
	}

	id, err := strconv.ParseUint(c.Param("id"), 10, 32)
	if err != nil || id == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "ID đơn hàng không hợp lệ"})
		return models.Order{}, false
	}

	var order models.Order
	if err := config.DB.Where("id = ? AND user_id = ?", uint(id), uid).
		First(&order).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Không tìm thấy đơn hàng"})
		return models.Order{}, false
	}
	return order, true
}

func requireAdminActor(c *gin.Context) (uint, bool) {
	value, exists := c.Get("user_id")
	uid, ok := value.(uint)
	if !exists || !ok || uid == 0 {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Yêu cầu đăng nhập"})
		return 0, false
	}
	role, _ := c.Get("role")
	if role != "admin" {
		c.JSON(http.StatusForbidden, gin.H{"error": "Chỉ admin mới có quyền thực hiện"})
		return 0, false
	}
	return uid, true
}

// CreateMomoPayment — Tạo yêu cầu thanh toán MoMo cho một đơn hàng (Phương án A)
func CreateMomoPayment(c *gin.Context) {
	var input struct {
		OrderID         uint    `json:"order_id"`
		Amount          float64 `json:"amount"`
		ShippingAddress string  `json:"shipping_address"`
		Phone           string  `json:"phone"`
		Email           string  `json:"email"`
		RedirectURL     string  `json:"redirect_url"`
		RequestType     string  `json:"request_type"`
	}

	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Dữ liệu không hợp lệ: " + err.Error()})
		return
	}

	// 1. Tìm đơn hàng có sẵn hoặc tạo đơn hàng mới nếu truyền amount
	var order models.Order
	if input.OrderID > 0 {
		val, exists := c.Get("user_id")
		uid, ok := val.(uint)
		if !exists || !ok || uid == 0 {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "Yêu cầu đăng nhập để thanh toán đơn hàng có sẵn"})
			return
		}
		if err := config.DB.Where("id = ? AND user_id = ?", input.OrderID, uid).First(&order).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "Không tìm thấy đơn hàng hoặc bạn không có quyền truy cập"})
			return
		}
	} else if input.Amount >= 1000 {
		currentUserID := resolvePaypalUserID(c, input.Email)
		order = models.Order{
			UserID:          currentUserID,
			Total:           input.Amount,
			Status:          "pending",
			PaymentStatus:   "unpaid",
			PaymentMethod:   "momo",
			ShippingAddress: input.ShippingAddress,
			Phone:           input.Phone,
			CreatedAt:       time.Now(),
		}
		if err := config.DB.Create(&order).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Không thể tạo đơn hàng: " + err.Error()})
			return
		}
	} else {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Vui lòng cung cấp order_id hoặc số tiền amount hợp lệ (>= 1,000 VND)"})
		return
	}

	// 2. Kiểm tra nếu đơn đã thanh toán hoặc đã kết thúc/hủy
	if order.PaymentStatus == "paid" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Đơn hàng này đã được thanh toán thành công trước đó"})
		return
	}
	if order.Status == "cancelled" || order.Status == "completed" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Đơn hàng không ở trạng thái cho phép thanh toán"})
		return
	}

	// 3. Gọi MoMo Service để tạo URL thanh toán
	reqType := input.RequestType
	if reqType == "" {
		reqType = "payWithMethod"
	}
	momoResp, err := services.CreateMomoPaymentUrl(&order, input.RedirectURL, reqType)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	// 4. Lưu lại mã phiên giao dịch MoMo vào database
	config.DB.Model(&order).Updates(map[string]interface{}{
		"payment_method":     "momo",
		"payment_order_id":   momoResp.OrderID,
		"payment_request_id": momoResp.RequestID,
	})

	c.JSON(http.StatusOK, gin.H{
		"message":       "Khởi tạo giao dịch MoMo thành công",
		"order_id":      order.ID,
		"amount":        order.Total,
		"momo_order_id": momoResp.OrderID,
		"pay_url":       momoResp.PayURL,
		"deeplink":      momoResp.Deeplink,
		"qr_code_url":   momoResp.QrCodeURL,
		"applink":       momoResp.Applink,
	})
}

// MomoIPN — Webhook nhận thông báo kết quả thanh toán từ MoMo Server (Server-to-Server)
func MomoIPN(c *gin.Context) {
	var ipnReq payments.MomoIpnRequest
	if err := c.ShouldBindJSON(&ipnReq); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Payload không hợp lệ: " + err.Error()})
		return
	}

	// 1. Xác thực chữ ký số HMAC-SHA256 (Bảo mật chống giả mạo)
	if !services.VerifyMomoIpnSignature(&ipnReq) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid signature (Chữ ký không hợp lệ)"})
		return
	}

	// 2. Tìm đơn hàng theo payment_order_id hoặc parse từ OrderID (BOKO_<id>_<timestamp>) khi đơn chưa gắn mã khác
	var order models.Order
	if err := config.DB.Where("payment_order_id = ?", ipnReq.OrderID).First(&order).Error; err != nil {
		var id uint
		if n, _ := fmt.Sscanf(ipnReq.OrderID, "BOKO_%d_", &id); n == 1 && id > 0 {
			var candidate models.Order
			if err2 := config.DB.Where("id = ?", id).First(&candidate).Error; err2 == nil {
				if candidate.PaymentOrderID == "" || candidate.PaymentOrderID == ipnReq.OrderID {
					order = candidate
				}
			}
		}
	}

	if order.ID == 0 {
		c.JSON(http.StatusNotFound, gin.H{"error": "Không tìm thấy đơn hàng tương ứng"})
		return
	}

	// 3. Đối soát số tiền thanh toán
	if ipnReq.Amount != int64(order.Total) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid amount (Số tiền thanh toán không khớp)"})
		return
	}

	// 4. Cơ chế Idempotency & State Guard: Nếu đơn đã thanh toán hoặc không còn ở trạng thái pending, không cập nhật lại
	if order.PaymentStatus == "paid" || order.Status == "cancelled" || order.Status == "completed" {
		c.Status(http.StatusNoContent)
		return
	}

	// 5. Cập nhật trạng thái đơn hàng nguyên tử dựa trên resultCode của MoMo
	if ipnReq.ResultCode == 0 {
		// Thanh toán thành công
		config.DB.Model(&order).Where("status = ? AND payment_status = ?", "pending", "unpaid").Updates(map[string]interface{}{
			"payment_status":   "paid",
			"status":           "confirmed",
			"payment_trans_id": strconv.FormatInt(ipnReq.TransID, 10),
			"payment_method":   "momo",
		})
	} else {
		// Thanh toán thất bại hoặc người dùng hủy
		config.DB.Model(&order).Where("status = ? AND payment_status = ?", "pending", "unpaid").Updates(map[string]interface{}{
			"payment_status": "failed",
		})
	}

	// Phản hồi HTTP 204 No Content theo đúng chuẩn MoMo Webhook
	c.Status(http.StatusNoContent)
}

// GetPaymentStatus — Kiểm tra trạng thái thanh toán của đơn hàng (có tự động sync Gateway)
func GetPaymentStatus(c *gin.Context) {
	order, ok := loadOwnedPaymentOrder(c)
	if !ok {
		return
	}

	// Tự động kiểm tra trực tiếp MoMo Gateway nếu đơn MoMo đang unpaid mà đã có mã MoMo Order
	if (order.PaymentMethod == "momo" || order.PaymentMethod == "") &&
		order.PaymentStatus != "paid" && order.Status == "pending" &&
		order.PaymentOrderID != "" && order.PaymentRequestID != "" {
		if queryResp, err := services.QueryMomoTransaction(order.PaymentOrderID, order.PaymentRequestID); err == nil {
			if queryResp.ResultCode == 0 && (queryResp.Amount == 0 || queryResp.Amount == int64(order.Total)) {
				config.DB.Model(&order).Where("status = ? AND payment_status = ?", "pending", "unpaid").Updates(map[string]interface{}{
					"payment_status":   "paid",
					"status":           "confirmed",
					"payment_trans_id": strconv.FormatInt(queryResp.TransID, 10),
					"payment_method":   "momo",
				})
				order.PaymentStatus = "paid"
				order.Status = "confirmed"
				order.PaymentTransID = strconv.FormatInt(queryResp.TransID, 10)
			}
		}
	}

	c.JSON(http.StatusOK, gin.H{
		"order_id":         order.ID,
		"total":            order.Total,
		"status":           order.Status,
		"payment_method":   order.PaymentMethod,
		"payment_status":   order.PaymentStatus,
		"payment_trans_id": order.PaymentTransID,
		"payment_order_id": order.PaymentOrderID,
		"updated_at":       order.UpdatedAt,
	})
}

// MockMomoIPN — API tiện ích cho admin demo test giả lập IPN thành công nội bộ
func MockMomoIPN(c *gin.Context) {
	actorID, ok := requireAdminActor(c)
	if !ok {
		return
	}

	orderIDStr := c.Param("id")
	orderID, err := strconv.ParseUint(orderIDStr, 10, 32)
	if err != nil || orderID == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "ID đơn hàng không hợp lệ"})
		return
	}

	var order models.Order
	if err := config.DB.Where("id = ?", uint(orderID)).First(&order).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Không tìm thấy đơn hàng"})
		return
	}

	if order.Status == "cancelled" || order.Status == "completed" ||
		order.PaymentStatus == "paid" || order.PaymentMethod != "momo" ||
		order.Status != "pending" || order.PaymentStatus != "unpaid" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Đơn không ở trạng thái có thể giả lập"})
		return
	}

	mockTransID := fmt.Sprintf("MOCK_TRANS_%d", time.Now().UnixMilli())
	res := config.DB.Model(&order).
		Where("status = ? AND payment_status = ? AND payment_method = ?",
			"pending", "unpaid", "momo").
		Updates(map[string]interface{}{
			"payment_status":   "paid",
			"status":           "confirmed",
			"payment_trans_id": mockTransID,
		})
	if res.Error != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Không thể cập nhật thanh toán demo"})
		return
	}
	if res.RowsAffected == 0 {
		c.JSON(http.StatusConflict, gin.H{"error": "Đơn đã đổi trạng thái"})
		return
	}

	log.Printf("[AUDIT][SIMULATED_PAYMENT] simulated=true action=mock_momo_ipn actor_user_id=%d order_id=%d trans_id=%s", actorID, order.ID, mockTransID)

	c.JSON(http.StatusOK, gin.H{
		"message":          "✅ Giả lập Webhook MoMo IPN thành công!",
		"order_id":         order.ID,
		"payment_status":   "paid",
		"status":           "confirmed",
		"payment_trans_id": mockTransID,
		"simulated":        true,
	})
}

// MomoSimulatorIPN — Xử lý thanh toán từ Cổng Giả Lập MoMo Sandbox với ký số HMAC-SHA256 đầy đủ
func MomoSimulatorIPN(c *gin.Context) {
	actorID, ok := requireAdminActor(c)
	if !ok {
		return
	}

	var input struct {
		OrderID         string `json:"order_id"`
		ResultCode      int    `json:"result_code"`
		PayType         string `json:"pay_type"`
		TamperSignature bool   `json:"tamper_signature"`
	}

	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Dữ liệu không hợp lệ: " + err.Error()})
		return
	}

	// 1. Tìm đơn hàng
	var order models.Order
	if err := config.DB.Where("payment_order_id = ?", input.OrderID).First(&order).Error; err != nil {
		var id uint
		if n, _ := fmt.Sscanf(input.OrderID, "BOKO_%d_", &id); n == 1 {
			config.DB.Where("id = ?", id).First(&order)
		} else if numID, errParse := strconv.ParseUint(input.OrderID, 10, 32); errParse == nil {
			config.DB.Where("id = ?", uint(numID)).First(&order)
		}
	}

	if order.ID == 0 {
		c.JSON(http.StatusNotFound, gin.H{"error": "Không tìm thấy đơn hàng tương ứng"})
		return
	}

	if order.Status == "cancelled" || order.Status == "completed" ||
		order.PaymentStatus == "paid" || order.PaymentMethod != "momo" ||
		order.Status != "pending" || order.PaymentStatus != "unpaid" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Đơn không ở trạng thái có thể giả lập"})
		return
	}

	cfg := payments.GetMomoConfig()
	timestamp := time.Now().UnixMilli()
	transID := timestamp

	momoOrderID := order.PaymentOrderID
	if momoOrderID == "" {
		momoOrderID = fmt.Sprintf("BOKO_%d_%d", order.ID, timestamp)
	}
	momoRequestID := order.PaymentRequestID
	if momoRequestID == "" {
		momoRequestID = fmt.Sprintf("REQ_%d_%d", order.ID, timestamp)
	}

	payType := input.PayType
	if payType == "" {
		payType = "credit"
	}

	message := "Thành công."
	if input.ResultCode != 0 {
		message = "Giao dịch bị từ chối hoặc bị hủy bởi khách hàng."
	}

	// 2. Tạo chuỗi ký HMAC-SHA256 chuẩn MoMo
	rawSignature := fmt.Sprintf(
		"accessKey=%s&amount=%d&extraData=%s&message=%s&orderId=%s&orderInfo=%s&orderType=%s&partnerCode=%s&payType=%s&requestId=%s&responseTime=%d&resultCode=%d&transId=%d",
		cfg.AccessKey, int64(order.Total), "", message, momoOrderID,
		fmt.Sprintf("Thanh toan don hang Boko #%d", order.ID), "momo_wallet",
		cfg.PartnerCode, payType, momoRequestID, timestamp, input.ResultCode, transID,
	)

	validSignature := services.CreateHmacSha256(rawSignature, cfg.SecretKey)

	submittedSignature := validSignature
	if input.TamperSignature {
		submittedSignature = "TAMPERED_INVALID_SIG_" + validSignature[:16]
	}

	ipnReq := payments.MomoIpnRequest{
		PartnerCode:  cfg.PartnerCode,
		OrderID:      momoOrderID,
		RequestID:    momoRequestID,
		Amount:       int64(order.Total),
		OrderInfo:    fmt.Sprintf("Thanh toan don hang Boko #%d", order.ID),
		OrderType:    "momo_wallet",
		TransID:      transID,
		ResultCode:   input.ResultCode,
		Message:      message,
		PayType:      payType,
		ResponseTime: timestamp,
		ExtraData:    "",
		Signature:    submittedSignature,
	}

	// 3. Kiểm tra tính toàn vẹn và xác thực chữ ký số bằng thuật toán crypto của hệ thống
	isSignatureValid := services.VerifyMomoIpnSignature(&ipnReq)
	if !isSignatureValid {
		c.JSON(http.StatusForbidden, gin.H{
			"success":             false,
			"security_status":     "BLOCKED_TAMPERING_DETECTED",
			"error":               "CẢNH BÁO AN TOÀN TMĐT: Chữ ký số HMAC-SHA256 không hợp lệ hoặc gói tin đã bị can thiệp! Hệ thống từ chối cập nhật đơn hàng.",
			"submitted_signature": submittedSignature,
			"tampered":            true,
		})
		return
	}

	// 4. Cập nhật trạng thái đơn hàng có điều kiện nguyên tử
	if input.ResultCode == 0 {
		res := config.DB.Model(&order).
			Where("status = ? AND payment_status = ? AND payment_method = ?",
				"pending", "unpaid", "momo").
			Updates(map[string]interface{}{
				"payment_status":     "paid",
				"status":             "confirmed",
				"payment_trans_id":   strconv.FormatInt(transID, 10),
				"payment_method":     "momo",
				"payment_order_id":   momoOrderID,
				"payment_request_id": momoRequestID,
			})
		if res.Error != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Không thể cập nhật thanh toán demo"})
			return
		}
		if res.RowsAffected == 0 {
			c.JSON(http.StatusConflict, gin.H{"error": "Đơn đã đổi trạng thái"})
			return
		}
	} else {
		res := config.DB.Model(&order).
			Where("status = ? AND payment_status = ? AND payment_method = ?",
				"pending", "unpaid", "momo").
			Updates(map[string]interface{}{
				"payment_status": "failed",
			})
		if res.Error != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Không thể cập nhật thanh toán demo"})
			return
		}
		if res.RowsAffected == 0 {
			c.JSON(http.StatusConflict, gin.H{"error": "Đơn đã đổi trạng thái"})
			return
		}
	}

	log.Printf("[AUDIT][SIMULATED_PAYMENT] simulated=true action=momo_simulator_ipn actor_user_id=%d order_id=%d result_code=%d trans_id=%d", actorID, order.ID, input.ResultCode, transID)

	c.JSON(http.StatusOK, gin.H{
		"success":          true,
		"simulated":        true,
		"security_status":  "SIGNATURE_VERIFIED_HMAC_SHA256",
		"message":          "Xác thực chữ ký điện tử HMAC-SHA256 thành công. Giao dịch đã được cập nhật an toàn.",
		"order_id":         order.ID,
		"payment_order_id": momoOrderID,
		"trans_id":         strconv.FormatInt(transID, 10),
		"amount":           order.Total,
		"result_code":      input.ResultCode,
		"redirect_url": fmt.Sprintf("/payment/momo-callback?orderId=%s&resultCode=%d&message=%s&transId=%d&amount=%d",
			momoOrderID, input.ResultCode, message, transID, int64(order.Total)),
	})
}

// ==================== PAYPAL CONTROLLER (giống cấu trúc MoMo/VNPay phía trên) ====================

// resolvePaypalUserID — xác định user cho đơn PayPal: ưu tiên JWT đã xác thực,
// nếu không có thì liên kết qua email, cuối cùng tạo khách vãng lai (giống VNPay)
func resolvePaypalUserID(c *gin.Context, email string) uint {
	if val, exists := c.Get("user_id"); exists {
		if uid, ok := val.(uint); ok && uid != 0 {
			return uid
		}
	}
	targetEmail := strings.TrimSpace(email)
	if targetEmail == "" {
		targetEmail = "guest@boko.com"
	}
	var user models.User
	if err := config.DB.Where("email = ?", targetEmail).First(&user).Error; err != nil {
		user = models.User{
			Email: targetEmail,
			Name:  "Khách mua hàng",
			Role:  "customer",
		}
		config.DB.Create(&user)
	}
	return user.ID
}

// CreatePaypalPayment — Tạo đơn PayPal (dùng được cho cả user đăng nhập và khách)
func CreatePaypalPayment(c *gin.Context) {
	var input struct {
		OrderID         uint    `json:"order_id"`
		Amount          float64 `json:"amount"`
		ShippingAddress string  `json:"shipping_address"`
		Phone           string  `json:"phone"`
		Email           string  `json:"email"`
		RedirectURL     string  `json:"redirect_url"` // web nhận kết quả sau khi duyệt (luồng redirect)
	}

	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Dữ liệu không hợp lệ: " + err.Error()})
		return
	}

	// 1. Tìm đơn hàng có sẵn hoặc tạo đơn hàng mới nếu truyền amount
	var order models.Order
	if input.OrderID > 0 {
		val, exists := c.Get("user_id")
		uid, ok := val.(uint)
		if !exists || !ok || uid == 0 {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "Yêu cầu đăng nhập để thanh toán đơn hàng có sẵn"})
			return
		}
		if err := config.DB.Where("id = ? AND user_id = ?", input.OrderID, uid).First(&order).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "Không tìm thấy đơn hàng hoặc bạn không có quyền truy cập"})
			return
		}
	} else if input.Amount >= 1000 {
		currentUserID := resolvePaypalUserID(c, input.Email)
		order = models.Order{
			UserID:          currentUserID,
			Total:           input.Amount,
			Status:          "pending",
			PaymentStatus:   "unpaid",
			PaymentMethod:   "paypal",
			ShippingAddress: input.ShippingAddress,
			Phone:           input.Phone,
			CreatedAt:       time.Now(),
		}
		if err := config.DB.Create(&order).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Không thể tạo đơn hàng: " + err.Error()})
			return
		}
	} else {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Số tiền thanh toán PayPal tối thiểu là 1,000 VND"})
		return
	}

	// 2. Kiểm tra nếu đơn đã thanh toán hoặc đã hủy/hoàn tất
	if order.PaymentStatus == "paid" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Đơn hàng này đã được thanh toán thành công trước đó"})
		return
	}
	if order.Status == "cancelled" || order.Status == "completed" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Đơn hàng không ở trạng thái cho phép thanh toán"})
		return
	}

	// 3. Gọi PayPal Service để tạo đơn (quy đổi VND sang USD trong service).
	// Có redirect_url thì PayPal redirect về sau khi duyệt (kiểu VNPay/MoMo),
	// không có thì dùng popup SDK rồi gọi capture sau.
	returnURL, cancelURL := strings.TrimSpace(input.RedirectURL), ""
	if returnURL != "" {
		cancelURL = returnURL
	}
	paypalResp, err := services.CreatePaypalOrder(order.Total, returnURL, cancelURL)
	if err != nil {
		if strings.Contains(err.Error(), "chưa cấu hình") || strings.Contains(err.Error(), "PayPal chưa được cấu hình") {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": err.Error()})
		} else {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		}
		return
	}

	// 4. Lưu lại mã đơn PayPal vào database
	config.DB.Model(&order).Updates(map[string]interface{}{
		"payment_method":   "paypal",
		"payment_order_id": paypalResp.ID,
	})

	c.JSON(http.StatusOK, gin.H{
		"message":         "Khởi tạo giao dịch PayPal thành công",
		"order_id":        order.ID,
		"amount":          order.Total,
		"amount_usd":      services.ConvertVNDToUSD(order.Total),
		"paypal_order_id": paypalResp.ID,
		"approve_url":     paypalResp.ApproveURL(),
	})
}

// CapturePaypalPayment — Thu tiền đơn PayPal đã được user approve (bắt buộc quyền sở hữu)
func CapturePaypalPayment(c *gin.Context) {
	value, exists := c.Get("user_id")
	uid, ok := value.(uint)
	if !exists || !ok || uid == 0 {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Yêu cầu đăng nhập"})
		return
	}

	var input struct {
		PaypalOrderID string `json:"paypal_order_id" binding:"required"`
	}

	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Dữ liệu không hợp lệ: " + err.Error()})
		return
	}

	// 1. Tìm đơn hàng theo mã đơn PayPal và bắt buộc khớp chủ sở hữu user_id
	var order models.Order
	if err := config.DB.Where("payment_order_id = ? AND user_id = ?", input.PaypalOrderID, uid).First(&order).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Không tìm thấy đơn hàng tương ứng"})
		return
	}

	// 2. Cơ chế Idempotency: Nếu đơn đã thanh toán rồi, không capture lại
	if order.PaymentStatus == "paid" {
		c.JSON(http.StatusOK, gin.H{
			"message":  "Đơn hàng đã được thanh toán trước đó",
			"order_id": order.ID, "payment_status": "paid", "status": order.Status,
		})
		return
	}

	if order.Status == "cancelled" || order.Status == "completed" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Đơn hàng không ở trạng thái cho phép thanh toán"})
		return
	}

	// 3. Capture + đối chiếu số tiền trong service
	cap, err := services.CapturePaypalOrder(input.PaypalOrderID, order.Total)
	if err != nil {
		config.DB.Model(&order).Where("status = ? AND payment_status = ?", "pending", "unpaid").Updates(map[string]interface{}{"payment_status": "failed"})
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// 4. Đánh dấu đã thanh toán (giống IPN MoMo)
	config.DB.Model(&order).Where("status = ? AND payment_status = ?", "pending", "unpaid").Updates(map[string]interface{}{
		"payment_status":   "paid",
		"status":           "confirmed",
		"payment_trans_id": cap.CaptureID,
		"payment_method":   "paypal",
	})

	c.JSON(http.StatusOK, gin.H{
		"message":  "Thanh toán PayPal thành công!",
		"order_id": order.ID, "total": order.Total,
		"payment_status": "paid", "status": "confirmed",
		"payment_trans_id": cap.CaptureID,
	})
}

// GetPaypalPaymentStatus — Kiểm tra trạng thái thanh toán PayPal của đơn hàng (có tự động sync Gateway)
func GetPaypalPaymentStatus(c *gin.Context) {
	order, ok := loadOwnedPaymentOrder(c)
	if !ok {
		return
	}

	// Tự động đồng bộ với PayPal nếu đơn PayPal đang unpaid mà đã có mã PayPal Order
	if (order.PaymentMethod == "paypal" || order.PaymentMethod == "") &&
		order.PaymentStatus != "paid" && order.Status == "pending" && order.PaymentOrderID != "" {
		if st, err := services.QueryPaypalOrder(order.PaymentOrderID); err == nil && st.Status == "COMPLETED" {
			config.DB.Model(&order).Where("status = ? AND payment_status = ?", "pending", "unpaid").Updates(map[string]interface{}{
				"payment_status":   "paid",
				"status":           "confirmed",
				"payment_trans_id": st.ID,
				"payment_method":   "paypal",
			})
			order.PaymentStatus = "paid"
			order.Status = "confirmed"
			order.PaymentTransID = st.ID
		}
	}

	c.JSON(http.StatusOK, gin.H{
		"order_id":         order.ID,
		"total":            order.Total,
		"status":           order.Status,
		"payment_method":   order.PaymentMethod,
		"payment_status":   order.PaymentStatus,
		"payment_trans_id": order.PaymentTransID,
		"payment_order_id": order.PaymentOrderID,
		"updated_at":       order.UpdatedAt,
	})
}

// MockPaypalCapture — API tiện ích cho admin demo test giả lập capture thành công nội bộ
func MockPaypalCapture(c *gin.Context) {
	actorID, ok := requireAdminActor(c)
	if !ok {
		return
	}

	orderIDStr := c.Param("id")
	orderID, err := strconv.ParseUint(orderIDStr, 10, 32)
	if err != nil || orderID == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "ID đơn hàng không hợp lệ"})
		return
	}

	var order models.Order
	if err := config.DB.Where("id = ?", uint(orderID)).First(&order).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Không tìm thấy đơn hàng"})
		return
	}

	if order.Status == "cancelled" || order.Status == "completed" ||
		order.PaymentStatus == "paid" || order.PaymentMethod != "paypal" ||
		order.Status != "pending" || order.PaymentStatus != "unpaid" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Đơn không ở trạng thái có thể giả lập"})
		return
	}

	mockTransID := fmt.Sprintf("MOCK_PAYPAL_%d", time.Now().UnixMilli())
	res := config.DB.Model(&order).
		Where("status = ? AND payment_status = ? AND payment_method = ?",
			"pending", "unpaid", "paypal").
		Updates(map[string]interface{}{
			"payment_status":   "paid",
			"status":           "confirmed",
			"payment_trans_id": mockTransID,
		})
	if res.Error != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Không thể cập nhật thanh toán demo"})
		return
	}
	if res.RowsAffected == 0 {
		c.JSON(http.StatusConflict, gin.H{"error": "Đơn đã đổi trạng thái"})
		return
	}

	log.Printf("[AUDIT][SIMULATED_PAYMENT] simulated=true action=mock_paypal_capture actor_user_id=%d order_id=%d trans_id=%s", actorID, order.ID, mockTransID)

	c.JSON(http.StatusOK, gin.H{
		"message":          "✅ Giả lập capture PayPal thành công!",
		"order_id":         order.ID,
		"payment_status":   "paid",
		"status":           "confirmed",
		"payment_trans_id": mockTransID,
		"simulated":        true,
	})
}
