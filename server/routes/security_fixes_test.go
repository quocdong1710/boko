package routes

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"github.com/joho/godotenv"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"boko/config"
	"boko/middleware"
	"boko/models"
	"boko/payments"
	"boko/services"
)

func ensureTestJWTAndAES(t *testing.T) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	if len(middleware.JwtSecret) == 0 {
		middleware.JwtSecret = []byte("0123456789abcdef0123456789abcdef")
	}
	if os.Getenv("APP_AES_KEY") == "" {
		t.Setenv("APP_AES_KEY", "0123456789abcdef0123456789abcdef")
	}
}

func initTestEnvAndDB(t *testing.T) {
	t.Helper()
	ensureTestJWTAndAES(t)
	_ = godotenv.Load("../.env")
	_ = godotenv.Load(".env")

	if config.DB != nil {
		return
	}

	dsn := strings.TrimSpace(os.Getenv("DATABASE_URL"))
	if dsn == "" {
		host := strings.TrimSpace(os.Getenv("DB_HOST"))
		if strings.HasPrefix(host, "postgres://") || strings.HasPrefix(host, "postgresql://") {
			dsn = host
		} else {
			if host == "" {
				host = "localhost"
			}
			port := strings.TrimSpace(os.Getenv("DB_PORT"))
			if port == "" {
				port = "5432"
			}
			user := strings.TrimSpace(os.Getenv("DB_USER"))
			if user == "" {
				user = "postgres"
			}
			password := strings.TrimSpace(os.Getenv("DB_PASSWORD"))
			if password == "" {
				password = "123456"
			}
			dbname := strings.TrimSpace(os.Getenv("DB_NAME"))
			if dbname == "" {
				dbname = "boko_db"
			}
			sslmode := strings.TrimSpace(os.Getenv("DB_SSLMODE"))
			if sslmode == "" {
				sslmode = "disable"
			}
			dsn = fmt.Sprintf("host=%s port=%s user=%s password=%s dbname=%s sslmode=%s connect_timeout=3",
				host, port, user, password, dbname, sslmode)
		}
	}

	db, err := gorm.Open(postgres.New(postgres.Config{
		DSN:                  dsn,
		PreferSimpleProtocol: true,
	}), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Skipf("Skipping DB-backed integration test (Postgres unreachable: %v)", err)
		return
	}
	sqlDB, err := db.DB()
	if err != nil || sqlDB.Ping() != nil {
		t.Skipf("Skipping DB-backed integration test (Postgres ping failed)")
		return
	}
	config.DB = db
	_ = config.DB.AutoMigrate(
		&models.User{},
		&models.Category{},
		&models.Book{},
		&models.Cart{},
		&models.Order{},
		&models.OrderItem{},
		&models.Review{},
		&models.Coupon{},
	)
}

func makeTestJWT(user models.User) string {
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"user_id": user.ID,
		"email":   user.Email,
		"role":    user.Role,
		"exp":     time.Now().Add(1 * time.Hour).Unix(),
	})
	s, _ := tok.SignedString(middleware.JwtSecret)
	return s
}

func createFixtureUser(t *testing.T, prefix, role string) models.User {
	t.Helper()
	hashed, err := bcrypt.GenerateFromPassword([]byte("SecretPass123!"), bcrypt.DefaultCost)
	if err != nil {
		t.Fatalf("hash password: %v", err)
	}
	u := models.User{
		Email:    fmt.Sprintf("%s_%d@fixture.boko.local", prefix, time.Now().UnixNano()),
		Password: string(hashed),
		Name:     prefix,
		Role:     role,
	}
	if err := config.DB.Create(&u).Error; err != nil {
		t.Fatalf("create fixture user: %v", err)
	}
	t.Cleanup(func() {
		config.DB.Where("user_id = ?", u.ID).Delete(&models.Order{})
		config.DB.Delete(&models.User{}, u.ID)
	})
	return u
}

func createFixtureOrder(t *testing.T, userID uint, total float64, method, status, paymentStatus string) models.Order {
	t.Helper()
	ord := models.Order{
		UserID:          userID,
		Total:           total,
		Status:          status,
		PaymentStatus:   paymentStatus,
		PaymentMethod:   method,
		ShippingAddress: fmt.Sprintf("Secret Address of User %d", userID),
		Phone:           fmt.Sprintf("090000%04d", userID%10000),
		PaymentOrderID:  fmt.Sprintf("BOKO_FIX_%d_%d", userID, time.Now().UnixNano()),
		CreatedAt:       time.Now(),
	}
	if err := config.DB.Create(&ord).Error; err != nil {
		t.Fatalf("create fixture order: %v", err)
	}
	t.Cleanup(func() {
		config.DB.Where("order_id = ?", ord.ID).Delete(&models.OrderItem{})
		config.DB.Delete(&models.Order{}, ord.ID)
	})
	return ord
}

// TestRoutes_MiddlewareAndMockGating_NoDB kiểm tra trực tiếp cấu hình route & middleware cho T13, T14, A01-2, A07-1 (rate limit), A08-1 (không phụ thuộc DB)
func TestRoutes_MiddlewareAndMockGating_NoDB(t *testing.T) {
	ensureTestJWTAndAES(t)

	// 1. Khi APP_ENV != demo hoặc ENABLE_PAYMENT_MOCK != true -> 3 route mock trả 404
	t.Setenv("APP_ENV", "release")
	t.Setenv("ENABLE_PAYMENT_MOCK", "false")
	t.Setenv("AUTH_RATE_LIMIT", "4")
	r := gin.New()
	SetupRoutes(r)

	adminToken := makeTestJWT(models.User{ID: 1, Email: "admin@boko.local", Role: "admin"})
	custToken := makeTestJWT(models.User{ID: 2, Email: "cust@boko.local", Role: "customer"})

	// T13 & A01-2: GET /api/orders/my-orders, /api/orders, /api/orders/1 không token hoặc token sai -> 401
	for _, path := range []string{
		"/api/orders/my-orders",
		"/api/orders/my-orders?email=victim@boko.com",
		"/api/orders",
		"/api/orders/1",
	} {
		req, _ := http.NewRequest("GET", path, nil)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("[T13/A01-2] expected 401 for anonymous %s, got %d", path, w.Code)
		}

		reqBad, _ := http.NewRequest("GET", path, nil)
		reqBad.Header.Set("Authorization", "Bearer invalid.token")
		wBad := httptest.NewRecorder()
		r.ServeHTTP(wBad, reqBad)
		if wBad.Code != http.StatusUnauthorized {
			t.Fatalf("[T13/A01-2] expected 401 for invalid token on %s, got %d", path, wBad.Code)
		}
	}

	// T14 & A01-2: GET /api/payment/{momo,vnpay,paypal}/status/:id không token -> 401; id=0 -> 400
	for _, p := range []string{"momo", "vnpay", "paypal"} {
		req, _ := http.NewRequest("GET", "/api/payment/"+p+"/status/1", nil)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("[T14/A01-2] expected 401 for anonymous /api/payment/%s/status/1, got %d", p, w.Code)
		}

		reqZero, _ := http.NewRequest("GET", "/api/payment/"+p+"/status/0", nil)
		reqZero.Header.Set("Authorization", "Bearer "+custToken)
		wZero := httptest.NewRecorder()
		r.ServeHTTP(wZero, reqZero)
		if wZero.Code != http.StatusBadRequest {
			t.Fatalf("[T14] expected 400 for id=0 on /api/payment/%s/status/0, got %d", p, wZero.Code)
		}
	}

	// PayPal capture & Confirm receipt: không token / token sai / token thiếu user_id -> 401 (không panic)
	zeroClaimTok := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"user_id": 0,
		"exp":     time.Now().Add(1 * time.Hour).Unix(),
	})
	zeroClaimStr, _ := zeroClaimTok.SignedString(middleware.JwtSecret)

	for _, postPath := range []string{
		"/api/payment/paypal/capture",
		"/api/orders/1/confirm-receipt",
	} {
		reqAnon, _ := http.NewRequest("POST", postPath, bytes.NewBufferString(`{"paypal_order_id":"PP_123"}`))
		reqAnon.Header.Set("Content-Type", "application/json")
		wAnon := httptest.NewRecorder()
		r.ServeHTTP(wAnon, reqAnon)
		if wAnon.Code != http.StatusUnauthorized {
			t.Fatalf("expected 401 for anonymous POST %s, got %d", postPath, wAnon.Code)
		}

		reqZeroClaim, _ := http.NewRequest("POST", postPath, bytes.NewBufferString(`{"paypal_order_id":"PP_123"}`))
		reqZeroClaim.Header.Set("Content-Type", "application/json")
		reqZeroClaim.Header.Set("Authorization", "Bearer "+zeroClaimStr)
		wZeroClaim := httptest.NewRecorder()
		r.ServeHTTP(wZeroClaim, reqZeroClaim)
		if wZeroClaim.Code != http.StatusUnauthorized {
			t.Fatalf("expected 401 for zero user_id claim on POST %s, got %d", postPath, wZeroClaim.Code)
		}
	}

	// A08-1: Mock routes tắt mặc định -> 404 kể cả khi gửi Admin JWT
	for _, path := range []string{
		"/api/payment/momo/mock-ipn/1",
		"/api/payment/paypal/mock-capture/1",
		"/api/payment/momo/simulator-ipn",
	} {
		req, _ := http.NewRequest("POST", path, bytes.NewBufferString(`{"order_id":"1","result_code":0}`))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+adminToken)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != http.StatusNotFound {
			t.Fatalf("[A08-1] expected 404 when mock disabled on %s, got %d", path, w.Code)
		}
	}

	// A08-1: Khi bật APP_ENV=demo & ENABLE_PAYMENT_MOCK=true -> không token 401, customer token 403
	t.Setenv("APP_ENV", "demo")
	t.Setenv("ENABLE_PAYMENT_MOCK", "true")
	rDemo := gin.New()
	SetupRoutes(rDemo)
	for _, path := range []string{
		"/api/payment/momo/mock-ipn/1",
		"/api/payment/paypal/mock-capture/1",
		"/api/payment/momo/simulator-ipn",
	} {
		reqAnon, _ := http.NewRequest("POST", path, bytes.NewBufferString(`{"order_id":"1","result_code":0}`))
		reqAnon.Header.Set("Content-Type", "application/json")
		wAnon := httptest.NewRecorder()
		rDemo.ServeHTTP(wAnon, reqAnon)
		if wAnon.Code != http.StatusUnauthorized {
			t.Fatalf("[A08-1] expected 401 for anonymous on %s when demo enabled, got %d", path, wAnon.Code)
		}

		reqCust, _ := http.NewRequest("POST", path, bytes.NewBufferString(`{"order_id":"1","result_code":0}`))
		reqCust.Header.Set("Content-Type", "application/json")
		reqCust.Header.Set("Authorization", "Bearer "+custToken)
		wCust := httptest.NewRecorder()
		rDemo.ServeHTTP(wCust, reqCust)
		if wCust.Code != http.StatusForbidden {
			t.Fatalf("[A08-1] expected 403 for customer on %s when demo enabled, got %d", path, wCust.Code)
		}
	}

	// A07-1: Rate limit áp dụng chung trên cả /api/login + /api/auth/login và /api/register + /api/auth/register
	gotLogin429 := false
	for i := 0; i < 6; i++ {
		path := "/api/login"
		if i%2 == 1 {
			path = "/api/auth/login"
		}
		reqRL, _ := http.NewRequest("POST", path, bytes.NewBufferString(`{}`))
		reqRL.Header.Set("Content-Type", "application/json")
		reqRL.RemoteAddr = "203.0.113.55:4000"
		wRL := httptest.NewRecorder()
		r.ServeHTTP(wRL, reqRL)
		if wRL.Code == http.StatusTooManyRequests {
			gotLogin429 = true
			break
		}
	}
	if !gotLogin429 {
		t.Fatalf("[A07-1] expected 429 Too Many Requests across /api/login and /api/auth/login aliases")
	}

	gotReg429 := false
	for i := 0; i < 6; i++ {
		path := "/api/register"
		if i%2 == 1 {
			path = "/api/auth/register"
		}
		reqRL, _ := http.NewRequest("POST", path, bytes.NewBufferString(`{}`))
		reqRL.Header.Set("Content-Type", "application/json")
		reqRL.RemoteAddr = "203.0.113.56:4000"
		wRL := httptest.NewRecorder()
		r.ServeHTTP(wRL, reqRL)
		if wRL.Code == http.StatusTooManyRequests {
			gotReg429 = true
			break
		}
	}
	if !gotReg429 {
		t.Fatalf("[A07-1] expected 429 Too Many Requests across /api/register and /api/auth/register aliases")
	}
}

// TestT13_OrderHistoryAccessControl kiểm tra T13 với dữ liệu fixture trong DB:
// - Không token / token sai -> 401
// - JWT A + ?email=B -> chỉ trả đơn của A, không lộ địa chỉ/SĐT của B
// - JWT A không truyền email -> chỉ trả đơn của A
func TestT13_OrderHistoryAccessControl(t *testing.T) {
	initTestEnvAndDB(t)
	r := gin.New()
	SetupRoutes(r)

	userA := createFixtureUser(t, "t13_userA", "customer")
	userB := createFixtureUser(t, "t13_userB", "customer")
	orderA := createFixtureOrder(t, userA.ID, 120000, "cod", "pending", "unpaid")
	orderB := createFixtureOrder(t, userB.ID, 250000, "cod", "pending", "unpaid")
	tokenA := makeTestJWT(userA)

	// 1. Không token -> 401
	req1, _ := http.NewRequest("GET", "/api/orders/my-orders", nil)
	w1 := httptest.NewRecorder()
	r.ServeHTTP(w1, req1)
	if w1.Code != http.StatusUnauthorized {
		t.Fatalf("T13 failed: expected 401 for anonymous /api/orders/my-orders, got %d", w1.Code)
	}

	// 2. Không token + ?email=B -> 401
	req2, _ := http.NewRequest("GET", "/api/orders/my-orders?email="+url.QueryEscape(userB.Email), nil)
	w2 := httptest.NewRecorder()
	r.ServeHTTP(w2, req2)
	if w2.Code != http.StatusUnauthorized {
		t.Fatalf("T13 failed: expected 401 for anonymous ?email=B, got %d", w2.Code)
	}

	// 3. Token sai -> 401
	req3, _ := http.NewRequest("GET", "/api/orders/my-orders", nil)
	req3.Header.Set("Authorization", "Bearer invalid.jwt.token")
	w3 := httptest.NewRecorder()
	r.ServeHTTP(w3, req3)
	if w3.Code != http.StatusUnauthorized {
		t.Fatalf("T13 failed: expected 401 for invalid token, got %d", w3.Code)
	}

	// 4. JWT A + ?email=B -> 200 OK nhưng chỉ chứa đơn của A, tuyệt đối không có đơn B
	req4, _ := http.NewRequest("GET", "/api/orders/my-orders?email="+url.QueryEscape(userB.Email), nil)
	req4.Header.Set("Authorization", "Bearer "+tokenA)
	w4 := httptest.NewRecorder()
	r.ServeHTTP(w4, req4)
	if w4.Code != http.StatusOK {
		t.Fatalf("T13 failed: expected 200 for JWT A, got %d", w4.Code)
	}
	var resp4 struct {
		Data []models.Order `json:"data"`
	}
	if err := json.Unmarshal(w4.Body.Bytes(), &resp4); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	foundA := false
	for _, o := range resp4.Data {
		if o.ID == orderB.ID || o.UserID == userB.ID || o.ShippingAddress == orderB.ShippingAddress {
			t.Fatalf("T13 failed: leaked User B order in response for User A")
		}
		if o.ID == orderA.ID {
			foundA = true
		}
	}
	if !foundA {
		t.Fatalf("T13 failed: expected User A's order in response")
	}

	// 5. JWT A không truyền email -> 200 OK chỉ đơn A
	req5, _ := http.NewRequest("GET", "/api/orders/my-orders", nil)
	req5.Header.Set("Authorization", "Bearer "+tokenA)
	w5 := httptest.NewRecorder()
	r.ServeHTTP(w5, req5)
	if w5.Code != http.StatusOK {
		t.Fatalf("T13 failed: expected 200 for JWT A without email, got %d", w5.Code)
	}
}

// TestT14_PaymentStatusOwnership kiểm tra T14:
// - Không token / token sai -> 401 cho cả 3 cổng (MoMo, VNPay, PayPal)
// - User A đọc status đơn của User B -> 404 cho cả 3 cổng
// - User A đọc status đơn của chính User A -> 200 OK
// - VNPay Return URL giả mạo / khác vnp_TxnRef hoặc khác vnp_Amount không được đánh dấu paid
// - PayPal capture và Create payment với order_id của người khác hoặc không token bị chặn, không ghi DB
func TestT14_PaymentStatusOwnership(t *testing.T) {
	initTestEnvAndDB(t)
	r := gin.New()
	SetupRoutes(r)

	userA := createFixtureUser(t, "t14_userA", "customer")
	userB := createFixtureUser(t, "t14_userB", "customer")
	orderA := createFixtureOrder(t, userA.ID, 150000, "vnpay", "pending", "unpaid")
	orderB := createFixtureOrder(t, userB.ID, 300000, "paypal", "pending", "unpaid")
	tokenA := makeTestJWT(userA)

	providers := []string{"momo", "vnpay", "paypal"}
	for _, p := range providers {
		// Không token -> 401
		u := fmt.Sprintf("/api/payment/%s/status/%d", p, orderB.ID)
		reqNoAuth, _ := http.NewRequest("GET", u, nil)
		wNoAuth := httptest.NewRecorder()
		r.ServeHTTP(wNoAuth, reqNoAuth)
		if wNoAuth.Code != http.StatusUnauthorized {
			t.Fatalf("T14 failed (%s): expected 401 without token, got %d", p, wNoAuth.Code)
		}

		// User A đọc đơn của User B -> 404
		reqCross, _ := http.NewRequest("GET", u, nil)
		reqCross.Header.Set("Authorization", "Bearer "+tokenA)
		wCross := httptest.NewRecorder()
		r.ServeHTTP(wCross, reqCross)
		if wCross.Code != http.StatusNotFound {
			t.Fatalf("T14 failed (%s): expected 404 when User A reads User B order, got %d", p, wCross.Code)
		}

		// User A đọc đơn của chính mình -> 200
		uOwn := fmt.Sprintf("/api/payment/%s/status/%d", p, orderA.ID)
		reqOwn, _ := http.NewRequest("GET", uOwn, nil)
		reqOwn.Header.Set("Authorization", "Bearer "+tokenA)
		wOwn := httptest.NewRecorder()
		r.ServeHTTP(wOwn, reqOwn)
		if wOwn.Code != http.StatusOK {
			t.Fatalf("T14 failed (%s): expected 200 when User A reads own order, got %d", p, wOwn.Code)
		}

		// Create payment với order_id của User B khi không có token -> 401, không tự tạo user rác trong DB
		probeEmail := fmt.Sprintf("probe_%s_%d@attacker.local", p, time.Now().UnixNano())
		createBody := fmt.Sprintf(`{"order_id":%d,"email":"%s"}`, orderB.ID, probeEmail)
		reqCreateAnon, _ := http.NewRequest("POST", "/api/payment/"+p+"/create", bytes.NewBufferString(createBody))
		reqCreateAnon.Header.Set("Content-Type", "application/json")
		wCreateAnon := httptest.NewRecorder()
		r.ServeHTTP(wCreateAnon, reqCreateAnon)
		if wCreateAnon.Code != http.StatusUnauthorized {
			t.Fatalf("T14 create (%s): expected 401 for anonymous order_id, got %d", p, wCreateAnon.Code)
		}
		var probeCount int64
		config.DB.Model(&models.User{}).Where("email = ?", probeEmail).Count(&probeCount)
		if probeCount != 0 {
			t.Fatalf("T14 create (%s): rejected request mutated DB by creating user %s", p, probeEmail)
		}

		// Create payment với order_id của User B bằng JWT của User A -> 404
		reqCreateCross, _ := http.NewRequest("POST", "/api/payment/"+p+"/create", bytes.NewBufferString(createBody))
		reqCreateCross.Header.Set("Content-Type", "application/json")
		reqCreateCross.Header.Set("Authorization", "Bearer "+tokenA)
		wCreateCross := httptest.NewRecorder()
		r.ServeHTTP(wCreateCross, reqCreateCross)
		if wCreateCross.Code != http.StatusNotFound {
			t.Fatalf("T14 create (%s): expected 404 when User A targets User B order_id, got %d", p, wCreateCross.Code)
		}
	}

	// PayPal capture: User A cố capture đơn PayPal của User B -> 404, trạng thái orderB không đổi
	reqCapCross, _ := http.NewRequest("POST", "/api/payment/paypal/capture",
		bytes.NewBufferString(fmt.Sprintf(`{"paypal_order_id":"%s"}`, orderB.PaymentOrderID)))
	reqCapCross.Header.Set("Content-Type", "application/json")
	reqCapCross.Header.Set("Authorization", "Bearer "+tokenA)
	wCapCross := httptest.NewRecorder()
	r.ServeHTTP(wCapCross, reqCapCross)
	if wCapCross.Code != http.StatusNotFound {
		t.Fatalf("T14 PayPal capture: expected 404 when User A captures User B order, got %d", wCapCross.Code)
	}
	var refreshedB models.Order
	config.DB.First(&refreshedB, orderB.ID)
	if refreshedB.PaymentStatus != "unpaid" || refreshedB.Status != "pending" {
		t.Fatalf("T14 PayPal capture: orderB mutated to %s/%s after rejected capture", refreshedB.PaymentStatus, refreshedB.Status)
	}

	// Kiểm tra VNPay return params có chữ ký hợp lệ nhưng của TxnRef khác -> không được đổi sang paid
	vnpCfg := payments.GetVnPayConfig()
	q := url.Values{}
	q.Set("vnp_TxnRef", "BOKO_OTHER_999999")
	q.Set("vnp_Amount", fmt.Sprintf("%d", int64(orderA.Total)*100))
	q.Set("vnp_ResponseCode", "00")
	q.Set("vnp_TransactionStatus", "00")
	q.Set("vnp_TransactionNo", "VNP123456")
	sig := services.CreateHmacSha512(q.Encode(), vnpCfg.HashSecret)
	q.Set("vnp_SecureHash", sig)

	reqForge, _ := http.NewRequest("GET", fmt.Sprintf("/api/payment/vnpay/status/%d?%s", orderA.ID, q.Encode()), nil)
	reqForge.Header.Set("Authorization", "Bearer "+tokenA)
	wForge := httptest.NewRecorder()
	r.ServeHTTP(wForge, reqForge)

	var refreshedA models.Order
	config.DB.First(&refreshedA, orderA.ID)
	if refreshedA.PaymentStatus == "paid" {
		t.Fatalf("T14 failed: orderA was marked paid using mismatched vnp_TxnRef")
	}

	// Kiểm tra VNPay return params có chữ ký hợp lệ, đúng TxnRef nhưng sai vnp_Amount -> không được đổi sang paid
	qBadAmt := url.Values{}
	qBadAmt.Set("vnp_TxnRef", orderA.PaymentOrderID)
	qBadAmt.Set("vnp_Amount", "100000") // 1,000 VND != 150,000 VND
	qBadAmt.Set("vnp_ResponseCode", "00")
	qBadAmt.Set("vnp_TransactionStatus", "00")
	qBadAmt.Set("vnp_TransactionNo", "VNP123456")
	qBadAmt.Set("vnp_SecureHash", services.CreateHmacSha512(qBadAmt.Encode(), vnpCfg.HashSecret))

	reqBadAmt, _ := http.NewRequest("GET", fmt.Sprintf("/api/payment/vnpay/status/%d?%s", orderA.ID, qBadAmt.Encode()), nil)
	reqBadAmt.Header.Set("Authorization", "Bearer "+tokenA)
	wBadAmt := httptest.NewRecorder()
	r.ServeHTTP(wBadAmt, reqBadAmt)

	config.DB.First(&refreshedA, orderA.ID)
	if refreshedA.PaymentStatus == "paid" {
		t.Fatalf("T14 failed: orderA was marked paid using mismatched vnp_Amount")
	}
}

// TestA01_2_AccessControlAliases kiểm tra A01-2 trên toàn bộ các alias đọc đơn và payment status
func TestA01_2_AccessControlAliases(t *testing.T) {
	initTestEnvAndDB(t)
	r := gin.New()
	SetupRoutes(r)

	userA := createFixtureUser(t, "a01_userA", "customer")
	userB := createFixtureUser(t, "a01_userB", "customer")
	orderA := createFixtureOrder(t, userA.ID, 125000, "cod", "pending", "unpaid")
	orderB := createFixtureOrder(t, userB.ID, 199000, "cod", "pending", "unpaid")
	tokenA := makeTestJWT(userA)

	// Alias GET /api/orders (anonymous -> 401, tokenA -> chỉ đơn A) và GET /api/orders/:id
	reqAnonList, _ := http.NewRequest("GET", "/api/orders", nil)
	wAnonList := httptest.NewRecorder()
	r.ServeHTTP(wAnonList, reqAnonList)
	if wAnonList.Code != http.StatusUnauthorized {
		t.Fatalf("A01-2 failed: expected 401 on anonymous GET /api/orders, got %d", wAnonList.Code)
	}

	reqOwnList, _ := http.NewRequest("GET", "/api/orders", nil)
	reqOwnList.Header.Set("Authorization", "Bearer "+tokenA)
	wOwnList := httptest.NewRecorder()
	r.ServeHTTP(wOwnList, reqOwnList)
	if wOwnList.Code != http.StatusOK {
		t.Fatalf("A01-2 failed: expected 200 on authenticated GET /api/orders, got %d", wOwnList.Code)
	}
	var listResp struct {
		Data []models.Order `json:"data"`
	}
	if err := json.Unmarshal(wOwnList.Body.Bytes(), &listResp); err != nil {
		t.Fatalf("unmarshal /api/orders: %v", err)
	}
	foundOwn := false
	for _, ord := range listResp.Data {
		if ord.ID == orderB.ID || ord.UserID == userB.ID {
			t.Fatalf("A01-2 failed: GET /api/orders leaked User B order")
		}
		if ord.ID == orderA.ID {
			foundOwn = true
		}
	}
	if !foundOwn {
		t.Fatalf("A01-2 failed: GET /api/orders did not return User A order")
	}

	reqCrossDetail, _ := http.NewRequest("GET", fmt.Sprintf("/api/orders/%d", orderB.ID), nil)
	reqCrossDetail.Header.Set("Authorization", "Bearer "+tokenA)
	wCrossDetail := httptest.NewRecorder()
	r.ServeHTTP(wCrossDetail, reqCrossDetail)
	if wCrossDetail.Code != http.StatusNotFound {
		t.Fatalf("A01-2 failed: expected 404 when User A requests GET /api/orders/:id of User B, got %d", wCrossDetail.Code)
	}
}

// TestA07_1_LoginTimingAndRateLimit kiểm tra A07-1:
// - Login với email tồn tại (sai mật khẩu) và email không tồn tại trả cùng 401 + thông báo chung
// - Đo nhiều mẫu (multi-sample) cho thấy login với email không tồn tại luôn thực thi dummy bcrypt
// - Rate limit áp dụng chung trên cả /api/login + /api/auth/login và /api/register + /api/auth/register
func TestA07_1_LoginTimingAndRateLimit(t *testing.T) {
	initTestEnvAndDB(t)
	t.Setenv("AUTH_RATE_LIMIT", "8")
	r := gin.New()
	SetupRoutes(r)

	existingUser := createFixtureUser(t, "a07_user", "customer")

	loginBody := func(email string) *bytes.Buffer {
		b, _ := json.Marshal(map[string]string{
			"email":    email,
			"password": "WrongPassword123!",
		})
		return bytes.NewBuffer(b)
	}

	// Đo nhiều request (3 mẫu mỗi loại) dưới ngưỡng rate limit
	var nonExistSamples []time.Duration
	var existSamples []time.Duration
	for sample := 0; sample < 3; sample++ {
		ip := fmt.Sprintf("198.51.100.%d:12345", 10+sample)

		startNonExist := time.Now()
		req1, _ := http.NewRequest("POST", "/api/login", loginBody(fmt.Sprintf("nonexistent_%d@fixture.boko.local", sample)))
		req1.Header.Set("Content-Type", "application/json")
		req1.RemoteAddr = ip
		w1 := httptest.NewRecorder()
		r.ServeHTTP(w1, req1)
		durNonExist := time.Since(startNonExist)
		nonExistSamples = append(nonExistSamples, durNonExist)

		startExist := time.Now()
		req2, _ := http.NewRequest("POST", "/api/auth/login", loginBody(existingUser.Email))
		req2.Header.Set("Content-Type", "application/json")
		req2.RemoteAddr = ip
		w2 := httptest.NewRecorder()
		r.ServeHTTP(w2, req2)
		durExist := time.Since(startExist)
		existSamples = append(existSamples, durExist)

		if w1.Code != http.StatusUnauthorized || w2.Code != http.StatusUnauthorized {
			t.Fatalf("A07-1 failed (sample %d): expected 401 for both logins, got %d and %d", sample, w1.Code, w2.Code)
		}
		if w1.Body.String() != w2.Body.String() {
			t.Fatalf("A07-1 failed (sample %d): response body mismatch: %s vs %s", sample, w1.Body.String(), w2.Body.String())
		}
		if durNonExist < 5*time.Millisecond {
			t.Fatalf("A07-1 failed (sample %d): non-existent user login returned too fast (%v), dummy bcrypt not executed", sample, durNonExist)
		}
	}
	t.Logf("A07-1 multi-sample timing: existing=%v, non-existing=%v", existSamples, nonExistSamples)

	// Kiểm tra Rate Limit chia sẻ giữa /api/login và /api/auth/login trên cùng 1 IP
	got429 := false
	for i := 0; i < 10; i++ {
		path := "/api/login"
		if i%2 == 1 {
			path = "/api/auth/login"
		}
		reqRL, _ := http.NewRequest("POST", path, loginBody(existingUser.Email))
		reqRL.Header.Set("Content-Type", "application/json")
		reqRL.RemoteAddr = "198.51.100.99:12345"
		wRL := httptest.NewRecorder()
		r.ServeHTTP(wRL, reqRL)
		if wRL.Code == http.StatusTooManyRequests {
			got429 = true
			break
		}
	}
	if !got429 {
		t.Fatalf("A07-1 failed: expected 429 Too Many Requests after exceeding AUTH_RATE_LIMIT across /api/login and /api/auth/login")
	}
}

// TestA08_1_MockPaymentSecurity kiểm tra A08-1:
// - Flag off -> 404 cho cả 3 route mock/simulator
// - Flag on:
//   + Không token -> 401, DB không đổi
//   + Customer token -> 403, DB không đổi
//   + Admin token nhưng sai payment_method hoặc đơn đã cancelled/completed -> 400, DB không đổi
//   + Admin token trên đơn pending + unpaid + đúng payment_method -> 200, cập nhật paid/confirmed choทั้ง MoMo, PayPal và Simulator
//   + IPN chính thức (MoMo/VNPay) vẫn kiểm tra chữ ký, số tiền và không hồi sinh đơn đã cancelled
func TestA08_1_MockPaymentSecurity(t *testing.T) {
	initTestEnvAndDB(t)

	customer := createFixtureUser(t, "a08_cust", "customer")
	admin := createFixtureUser(t, "a08_admin", "admin")
	momoOrder := createFixtureOrder(t, customer.ID, 180000, "momo", "pending", "unpaid")
	simOrder := createFixtureOrder(t, customer.ID, 210000, "momo", "pending", "unpaid")
	paypalOrder := createFixtureOrder(t, customer.ID, 220000, "paypal", "pending", "unpaid")
	cancelledOrder := createFixtureOrder(t, customer.ID, 150000, "momo", "cancelled", "unpaid")

	custToken := makeTestJWT(customer)
	adminToken := makeTestJWT(admin)

	// 1. Mặc định (Flag OFF) -> 404 Not Found
	t.Setenv("APP_ENV", "production")
	t.Setenv("ENABLE_PAYMENT_MOCK", "false")
	rOff := gin.New()
	SetupRoutes(rOff)

	mockPaths := []string{
		fmt.Sprintf("/api/payment/momo/mock-ipn/%d", momoOrder.ID),
		fmt.Sprintf("/api/payment/paypal/mock-capture/%d", paypalOrder.ID),
		"/api/payment/momo/simulator-ipn",
	}
	for _, path := range mockPaths {
		req, _ := http.NewRequest("POST", path, bytes.NewBufferString(`{"order_id":"1","result_code":0}`))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+adminToken)
		w := httptest.NewRecorder()
		rOff.ServeHTTP(w, req)
		if w.Code != http.StatusNotFound {
			t.Fatalf("A08-1 failed: expected 404 when mock flags off for %s, got %d", path, w.Code)
		}
	}

	// 2. Khi bật APP_ENV=demo và ENABLE_PAYMENT_MOCK=true
	t.Setenv("APP_ENV", "demo")
	t.Setenv("ENABLE_PAYMENT_MOCK", "true")
	rOn := gin.New()
	SetupRoutes(rOn)

	// 2a. Không token -> 401
	for _, path := range mockPaths {
		req, _ := http.NewRequest("POST", path, bytes.NewBufferString(`{"order_id":"1","result_code":0}`))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		rOn.ServeHTTP(w, req)
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("A08-1 failed: expected 401 without token on %s, got %d", path, w.Code)
		}
	}

	// 2b. Customer token -> 403 Forbidden, DB không đổi
	for _, path := range mockPaths {
		req, _ := http.NewRequest("POST", path, bytes.NewBufferString(fmt.Sprintf(`{"order_id":"%d","result_code":0}`, momoOrder.ID)))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+custToken)
		w := httptest.NewRecorder()
		rOn.ServeHTTP(w, req)
		if w.Code != http.StatusForbidden {
			t.Fatalf("A08-1 failed: expected 403 for customer token on %s, got %d", path, w.Code)
		}
	}
	var checkMomo models.Order
	config.DB.First(&checkMomo, momoOrder.ID)
	if checkMomo.PaymentStatus != "unpaid" || checkMomo.Status != "pending" {
		t.Fatalf("A08-1 failed: order state mutated after rejected customer request")
	}

	// 2c. Admin gọi mock-ipn MoMo trên đơn đã cancelled -> 400 Bad Request
	reqCancel, _ := http.NewRequest("POST", fmt.Sprintf("/api/payment/momo/mock-ipn/%d", cancelledOrder.ID), nil)
	reqCancel.Header.Set("Authorization", "Bearer "+adminToken)
	wCancel := httptest.NewRecorder()
	rOn.ServeHTTP(wCancel, reqCancel)
	if wCancel.Code != http.StatusBadRequest {
		t.Fatalf("A08-1 failed: expected 400 when mocking cancelled order, got %d", wCancel.Code)
	}

	// 2d. Admin gọi mock-ipn MoMo trên đơn PayPal (sai provider) -> 400 Bad Request
	reqWrongMethod, _ := http.NewRequest("POST", fmt.Sprintf("/api/payment/momo/mock-ipn/%d", paypalOrder.ID), nil)
	reqWrongMethod.Header.Set("Authorization", "Bearer "+adminToken)
	wWrongMethod := httptest.NewRecorder()
	rOn.ServeHTTP(wWrongMethod, reqWrongMethod)
	if wWrongMethod.Code != http.StatusBadRequest {
		t.Fatalf("A08-1 failed: expected 400 when mocking wrong payment method, got %d", wWrongMethod.Code)
	}

	// 2e. Admin gọi mock-ipn MoMo trên đơn MoMo hợp lệ -> 200 OK, chuyển sang paid/confirmed
	reqAdminOk, _ := http.NewRequest("POST", fmt.Sprintf("/api/payment/momo/mock-ipn/%d", momoOrder.ID), nil)
	reqAdminOk.Header.Set("Authorization", "Bearer "+adminToken)
	wAdminOk := httptest.NewRecorder()
	rOn.ServeHTTP(wAdminOk, reqAdminOk)
	if wAdminOk.Code != http.StatusOK {
		t.Fatalf("A08-1 failed: expected 200 for admin on valid pending momo order, got %d (%s)", wAdminOk.Code, wAdminOk.Body.String())
	}
	config.DB.First(&checkMomo, momoOrder.ID)
	if checkMomo.PaymentStatus != "paid" || checkMomo.Status != "confirmed" {
		t.Fatalf("A08-1 failed: expected order to be paid/confirmed, got %s/%s", checkMomo.PaymentStatus, checkMomo.Status)
	}

	// 2f. Admin gọi lại lần 2 trên đơn đã paid -> 400 Bad Request
	reqRetry, _ := http.NewRequest("POST", fmt.Sprintf("/api/payment/momo/mock-ipn/%d", momoOrder.ID), nil)
	reqRetry.Header.Set("Authorization", "Bearer "+adminToken)
	wRetry := httptest.NewRecorder()
	rOn.ServeHTTP(wRetry, reqRetry)
	if wRetry.Code != http.StatusBadRequest {
		t.Fatalf("A08-1 failed: expected 400 on already paid order, got %d", wRetry.Code)
	}

	// 2g. Admin gọi mock-capture PayPal trên đơn PayPal hợp lệ -> 200 OK
	reqPaypalOk, _ := http.NewRequest("POST", fmt.Sprintf("/api/payment/paypal/mock-capture/%d", paypalOrder.ID), nil)
	reqPaypalOk.Header.Set("Authorization", "Bearer "+adminToken)
	wPaypalOk := httptest.NewRecorder()
	rOn.ServeHTTP(wPaypalOk, reqPaypalOk)
	if wPaypalOk.Code != http.StatusOK {
		t.Fatalf("A08-1 failed: expected 200 for admin on valid pending paypal order, got %d", wPaypalOk.Code)
	}
	var checkPaypal models.Order
	config.DB.First(&checkPaypal, paypalOrder.ID)
	if checkPaypal.PaymentStatus != "paid" || checkPaypal.Status != "confirmed" {
		t.Fatalf("A08-1 failed: expected paypalOrder to be paid/confirmed, got %s/%s", checkPaypal.PaymentStatus, checkPaypal.Status)
	}

	// 2h. Admin gọi simulator-ipn trên đơn MoMo hợp lệ -> 200 OK
	simBody := fmt.Sprintf(`{"order_id":"%s","result_code":0,"pay_type":"qr","tamper_signature":false}`, simOrder.PaymentOrderID)
	reqSimOk, _ := http.NewRequest("POST", "/api/payment/momo/simulator-ipn", bytes.NewBufferString(simBody))
	reqSimOk.Header.Set("Content-Type", "application/json")
	reqSimOk.Header.Set("Authorization", "Bearer "+adminToken)
	wSimOk := httptest.NewRecorder()
	rOn.ServeHTTP(wSimOk, reqSimOk)
	if wSimOk.Code != http.StatusOK {
		t.Fatalf("A08-1 failed: expected 200 for admin on valid simulator-ipn, got %d (%s)", wSimOk.Code, wSimOk.Body.String())
	}
	var checkSim models.Order
	config.DB.First(&checkSim, simOrder.ID)
	if checkSim.PaymentStatus != "paid" || checkSim.Status != "confirmed" {
		t.Fatalf("A08-1 failed: expected simOrder to be paid/confirmed, got %s/%s", checkSim.PaymentStatus, checkSim.Status)
	}

	// 3. Kiểm tra IPN chính thức của MoMo không cho phép sai số tiền hoặc hồi sinh đơn đã cancelled
	momoCfg := payments.GetMomoConfig()
	rawCancelSig := fmt.Sprintf(
		"accessKey=%s&amount=%d&extraData=&message=OK&orderId=%s&orderInfo=test&orderType=momo_wallet&partnerCode=%s&payType=qr&requestId=REQ_1&responseTime=1710000000000&resultCode=0&transId=999",
		momoCfg.AccessKey, int64(cancelledOrder.Total), cancelledOrder.PaymentOrderID, momoCfg.PartnerCode,
	)
	ipnCancelPayload, _ := json.Marshal(payments.MomoIpnRequest{
		PartnerCode:  momoCfg.PartnerCode,
		OrderID:      cancelledOrder.PaymentOrderID,
		RequestID:    "REQ_1",
		Amount:       int64(cancelledOrder.Total),
		OrderInfo:    "test",
		OrderType:    "momo_wallet",
		TransID:      999,
		ResultCode:   0,
		Message:      "OK",
		PayType:      "qr",
		ResponseTime: 1710000000000,
		ExtraData:    "",
		Signature:    services.CreateHmacSha256(rawCancelSig, momoCfg.SecretKey),
	})
	reqIpnCancel, _ := http.NewRequest("POST", "/api/payment/momo/ipn", bytes.NewBuffer(ipnCancelPayload))
	reqIpnCancel.Header.Set("Content-Type", "application/json")
	wIpnCancel := httptest.NewRecorder()
	rOn.ServeHTTP(wIpnCancel, reqIpnCancel)

	var checkCancelled models.Order
	config.DB.First(&checkCancelled, cancelledOrder.ID)
	if checkCancelled.Status != "cancelled" || checkCancelled.PaymentStatus == "paid" {
		t.Fatalf("A08-1 failed: official MomoIPN resurrected cancelled order to %s/%s", checkCancelled.Status, checkCancelled.PaymentStatus)
	}
}
