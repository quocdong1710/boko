package controllers

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"boko/payments"
	"boko/services"
)

func setupTestRouter() *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.Default()
	r.POST("/api/payment/momo/ipn", MomoIPN)
	return r
}

// TestMomoIPN_InvalidSignature — Đảm bảo chữ ký giả mạo bị từ chối với 400 Bad Request
func TestMomoIPN_InvalidSignature(t *testing.T) {
	r := setupTestRouter()

	fakePayload := payments.MomoIpnRequest{
		PartnerCode:  "MOMOBKUN20180529",
		OrderID:      "BOKO_TEST_1",
		RequestID:    "REQ_TEST_1",
		Amount:       50000,
		ResultCode:   0,
		Message:      "Thành công.",
		Signature:    "fake_invalid_signature_hex_12345",
		ResponseTime: time.Now().UnixMilli(),
	}

	body, _ := json.Marshal(fakePayload)
	req, _ := http.NewRequest("POST", "/api/payment/momo/ipn", bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")

	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("Mong đợi mã lỗi 400 Bad Request cho chữ ký giả mạo, nhưng nhận được: %d", w.Code)
	}

	t.Logf("✅ Đã chặn thành công request có chữ ký giả mạo (HTTP %d)", w.Code)
}

// TestMomoIPN_SignatureGenerationVerification — Đảm bảo chuỗi ký chuẩn của IPN được tạo và xác thực thành công
func TestMomoIPN_SignatureGenerationVerification(t *testing.T) {
	cfg := payments.GetMomoConfig()

	reqData := payments.MomoIpnRequest{
		PartnerCode:  cfg.PartnerCode,
		OrderID:      "BOKO_9999_TEST",
		RequestID:    "REQ_9999_TEST",
		Amount:       100000,
		OrderInfo:    "Thanh toan test Boko",
		OrderType:    "momo_wallet",
		TransID:      1234567890,
		ResultCode:   0,
		Message:      "Thành công.",
		PayType:      "qr",
		ResponseTime: 1710000000000,
		ExtraData:    "",
	}

	rawSignature := "accessKey=" + cfg.AccessKey +
		"&amount=100000" +
		"&extraData=" +
		"&message=" + reqData.Message +
		"&orderId=" + reqData.OrderID +
		"&orderInfo=" + reqData.OrderInfo +
		"&orderType=" + reqData.OrderType +
		"&partnerCode=" + cfg.PartnerCode +
		"&payType=" + reqData.PayType +
		"&requestId=" + reqData.RequestID +
		"&responseTime=1710000000000" +
		"&resultCode=0" +
		"&transId=1234567890"

	reqData.Signature = services.CreateHmacSha256(rawSignature, cfg.SecretKey)

	if !services.VerifyMomoIpnSignature(&reqData) {
		t.Fatalf("Lỗi: VerifyMomoIpnSignature phải trả về true cho chữ ký hợp lệ")
	}

	t.Logf("✅ Chữ ký IPN hợp lệ đã được xác thực thành công!")
}

// TestT13_GetMyOrders_RejectsWithoutAuthOrWithEmailOnly — Đảm bảo GetMyOrders từ chối 401 khi không có user_id trong context (kể cả khi truyền ?email=)
func TestT13_GetMyOrders_RejectsWithoutAuthOrWithEmailOnly(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.GET("/api/orders/my-orders", GetMyOrders)

	for _, path := range []string{"/api/orders/my-orders", "/api/orders/my-orders?email=victim@boko.com"} {
		req, _ := http.NewRequest("GET", path, nil)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("T13: expected 401 for %s without JWT user_id, got %d", path, w.Code)
		}
	}
}

// TestT14_PaymentStatus_RejectsUnauthenticatedAndInvalidID — Đảm bảo cả 3 hàm Get*PaymentStatus từ chối 401 khi thiếu user_id và 400 khi ID không hợp lệ
func TestT14_PaymentStatus_RejectsUnauthenticatedAndInvalidID(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.GET("/api/payment/momo/status/:id", GetPaymentStatus)
	r.GET("/api/payment/vnpay/status/:id", GetVnPayPaymentStatus)
	r.GET("/api/payment/paypal/status/:id", GetPaypalPaymentStatus)

	for _, p := range []string{"momo", "vnpay", "paypal"} {
		req, _ := http.NewRequest("GET", "/api/payment/"+p+"/status/1", nil)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("T14 (%s): expected 401 without user_id, got %d", p, w.Code)
		}
	}

	// Khi có user_id hợp lệ nhưng id=0 hoặc id=abc -> 400 Bad Request trước khi chạm DB
	rWithUser := gin.New()
	rWithUser.Use(func(c *gin.Context) {
		c.Set("user_id", uint(42))
		c.Next()
	})
	rWithUser.GET("/api/payment/momo/status/:id", GetPaymentStatus)
	rWithUser.GET("/api/payment/vnpay/status/:id", GetVnPayPaymentStatus)
	rWithUser.GET("/api/payment/paypal/status/:id", GetPaypalPaymentStatus)

	for _, p := range []string{"momo", "vnpay", "paypal"} {
		for _, badID := range []string{"0", "abc"} {
			req, _ := http.NewRequest("GET", "/api/payment/"+p+"/status/"+badID, nil)
			w := httptest.NewRecorder()
			rWithUser.ServeHTTP(w, req)
			if w.Code != http.StatusBadRequest {
				t.Fatalf("T14 (%s, id=%s): expected 400 Bad Request, got %d", p, badID, w.Code)
			}
		}
	}
}

// TestA08_1_MockPaymentHandlers_RequireAdmin — Đảm bảo MockMomoIPN, MockPaypalCapture, MomoSimulatorIPN chặn 401/403 nếu không phải admin
func TestA08_1_MockPaymentHandlers_RequireAdmin(t *testing.T) {
	gin.SetMode(gin.TestMode)

	// 1. Không có user_id -> 401
	rAnon := gin.New()
	rAnon.POST("/api/payment/momo/mock-ipn/:id", MockMomoIPN)
	rAnon.POST("/api/payment/paypal/mock-capture/:id", MockPaypalCapture)
	rAnon.POST("/api/payment/momo/simulator-ipn", MomoSimulatorIPN)

	for _, path := range []string{"/api/payment/momo/mock-ipn/1", "/api/payment/paypal/mock-capture/1", "/api/payment/momo/simulator-ipn"} {
		req, _ := http.NewRequest("POST", path, bytes.NewBufferString(`{"order_id":"1","result_code":0}`))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		rAnon.ServeHTTP(w, req)
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("A08-1 anon (%s): expected 401, got %d", path, w.Code)
		}
	}

	// 2. Role customer -> 403
	rCust := gin.New()
	rCust.Use(func(c *gin.Context) {
		c.Set("user_id", uint(7))
		c.Set("role", "customer")
		c.Next()
	})
	rCust.POST("/api/payment/momo/mock-ipn/:id", MockMomoIPN)
	rCust.POST("/api/payment/paypal/mock-capture/:id", MockPaypalCapture)
	rCust.POST("/api/payment/momo/simulator-ipn", MomoSimulatorIPN)

	for _, path := range []string{"/api/payment/momo/mock-ipn/1", "/api/payment/paypal/mock-capture/1", "/api/payment/momo/simulator-ipn"} {
		req, _ := http.NewRequest("POST", path, bytes.NewBufferString(`{"order_id":"1","result_code":0}`))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		rCust.ServeHTTP(w, req)
		if w.Code != http.StatusForbidden {
			t.Fatalf("A08-1 customer (%s): expected 403, got %d", path, w.Code)
		}
	}
}

// TestT14_CaptureAndCreateOrderID_RequireAuthWithoutDBMutation — Đảm bảo CapturePaypalPayment và Create*Payment với order_id > 0 từ chối 401 ngay cả khi config.DB == nil (không ghi DB)
func TestT14_CaptureAndCreateOrderID_RequireAuthWithoutDBMutation(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.POST("/api/payment/paypal/capture", CapturePaypalPayment)
	r.POST("/api/payment/momo/create", CreateMomoPayment)
	r.POST("/api/payment/vnpay/create", CreateVnPayPayment)
	r.POST("/api/payment/paypal/create", CreatePaypalPayment)

	reqCap, _ := http.NewRequest("POST", "/api/payment/paypal/capture", bytes.NewBufferString(`{"paypal_order_id":"PP_TEST_1"}`))
	reqCap.Header.Set("Content-Type", "application/json")
	wCap := httptest.NewRecorder()
	r.ServeHTTP(wCap, reqCap)
	if wCap.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for unauthenticated CapturePaypalPayment, got %d", wCap.Code)
	}

	for _, p := range []string{"momo", "vnpay", "paypal"} {
		reqCreate, _ := http.NewRequest("POST", "/api/payment/"+p+"/create", bytes.NewBufferString(`{"order_id":99,"email":"probe@attacker.local"}`))
		reqCreate.Header.Set("Content-Type", "application/json")
		wCreate := httptest.NewRecorder()
		r.ServeHTTP(wCreate, reqCreate)
		if wCreate.Code != http.StatusUnauthorized {
			t.Fatalf("expected 401 for unauthenticated Create (%s) with order_id > 0 without touching DB, got %d", p, wCreate.Code)
		}
	}
}

