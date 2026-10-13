package controllers

import (
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"

	"boko/config"
	"boko/middleware"
	"boko/models"
)

var dummyOnce sync.Once
var dummyHash []byte
var dummyInitErr error

func loginDummyHash() ([]byte, error) {
	dummyOnce.Do(func() {
		dummyHash, dummyInitErr = bcrypt.GenerateFromPassword(
			[]byte("not-a-real-account-password"), bcrypt.DefaultCost,
		)
	})
	return dummyHash, dummyInitErr
}

func init() {
	_, _ = loginDummyHash()
}

func decryptPhoneIfNeeded(rawPhone string) string {
	if strings.TrimSpace(rawPhone) == "" {
		return ""
	}

	decrypted, err := config.DecryptString(rawPhone)
	if err != nil {
		return rawPhone
	}
	return decrypted
}

func userPublicPayload(user models.User) gin.H {
	payload := gin.H{
		"id":    user.ID,
		"email": user.Email,
		"name":  user.Name,
		"role":  user.Role,
	}

	if phone := decryptPhoneIfNeeded(user.Phone); phone != "" {
		payload["phone"] = phone
	}
	return payload
}

// ==================== REGISTER ====================

// Register — tạo tài khoản mới (mặc định role = customer)
func Register(c *gin.Context) {
	var input struct {
		Email    string `json:"email" binding:"required,email"`
		Password string `json:"password" binding:"required,min=6"`
		Name     string `json:"name" binding:"required"`
		Phone    string `json:"phone"`
	}

	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Dữ liệu không hợp lệ: " + err.Error()})
		return
	}

	// Kiểm tra email trùng
	var existing models.User
	result := config.DB.Where("email = ?", input.Email).First(&existing)
	if result.Error == nil {
		c.JSON(http.StatusConflict, gin.H{"error": "Email đã được sử dụng"})
		return
	}
	if !errors.Is(result.Error, gorm.ErrRecordNotFound) {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Không thể đăng ký tài khoản"})
		return
	}

	// Hash password
	hashedPassword, err := bcrypt.GenerateFromPassword([]byte(input.Password), bcrypt.DefaultCost)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Lỗi xử lý mật khẩu"})
		return
	}

	phoneCipherText := ""
	if strings.TrimSpace(input.Phone) != "" {
		cipherText, err := config.EncryptString(input.Phone)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Lỗi mã hóa số điện thoại"})
			return
		}
		phoneCipherText = cipherText
	}

	user := models.User{
		Email:    input.Email,
		Password: string(hashedPassword),
		Name:     input.Name,
		Phone:    phoneCipherText,
		Role:     "customer",
	}
	if err := config.DB.Create(&user).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Không thể đăng ký tài khoản"})
		return
	}

	// Tạo JWT ngay khi đăng ký thành công
	regToken := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"user_id": user.ID,
		"email":   user.Email,
		"role":    user.Role,
		"exp":     time.Now().Add(7 * 24 * time.Hour).Unix(),
	})
	regTokenString, _ := regToken.SignedString(middleware.JwtSecret)

	// Frontend nhận { success, user, token }
	c.JSON(http.StatusCreated, gin.H{
		"success": true,
		"message": "Đăng ký thành công!",
		"token":   regTokenString,
		"user":    userPublicPayload(user),
	})
}

// ==================== LOGIN ====================

// Login — đăng nhập, trả về JWT token
func Login(c *gin.Context) {
	var input struct {
		Identifier string `json:"identifier"`
		Email      string `json:"email"`
		Password   string `json:"password" binding:"required"`
	}

	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Dữ liệu không hợp lệ"})
		return
	}

	// Frontend gửi identifier (email hoặc username); backend hỗ trợ cả 2
	loginKey := strings.TrimSpace(input.Email)
	if loginKey == "" && input.Identifier != "" {
		loginKey = strings.TrimSpace(input.Identifier)
	}
	if loginKey == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Dữ liệu không hợp lệ"})
		return
	}

	var user models.User
	result := config.DB.Where("email = ? OR name = ?", loginKey, loginKey).First(&user)
	if errors.Is(result.Error, gorm.ErrRecordNotFound) {
		hash, err := loginDummyHash()
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Không thể đăng nhập"})
			return
		}
		_ = bcrypt.CompareHashAndPassword(hash, []byte(input.Password))
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Email hoặc mật khẩu không đúng"})
		return
	}
	if result.Error != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Không thể đăng nhập"})
		return
	}
	if bcrypt.CompareHashAndPassword([]byte(user.Password), []byte(input.Password)) != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Email hoặc mật khẩu không đúng"})
		return
	}

	// Tạo JWT (thời hạn 7 ngày)
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"user_id": user.ID,
		"email":   user.Email,
		"role":    user.Role,
		"exp":     time.Now().Add(7 * 24 * time.Hour).Unix(),
	})

	tokenString, _ := token.SignedString(middleware.JwtSecret)

	// Frontend nhận { success, user, token }
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "Đăng nhập thành công!",
		"token":   tokenString,
		"user":    userPublicPayload(user),
	})
}

// ==================== PROFILE ====================

// GetProfile — lấy thông tin user đang đăng nhập
func GetProfile(c *gin.Context) {
	userID, _ := c.Get("user_id")

	var user models.User
	if result := config.DB.First(&user, userID); result.Error != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Không tìm thấy user"})
		return
	}

	// Frontend muốn { success, user }
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"user":    userPublicPayload(user),
	})
}

// UpdateProfile — cập nhật tên và mật khẩu
func UpdateProfile(c *gin.Context) {
	userID, _ := c.Get("user_id")

	var input struct {
		Name        string `json:"name"`
		Phone       string `json:"phone"`
		OldPassword string `json:"old_password"`
		NewPassword string `json:"new_password" binding:"omitempty,min=6"`
	}

	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Dữ liệu không hợp lệ"})
		return
	}

	var user models.User
	config.DB.First(&user, userID)

	updates := map[string]interface{}{}

	if input.Name != "" {
		updates["name"] = input.Name
	}

	if strings.TrimSpace(input.Phone) != "" {
		cipherText, err := config.EncryptString(input.Phone)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Lỗi mã hóa số điện thoại"})
			return
		}
		updates["phone"] = cipherText
	}

	// Đổi mật khẩu nếu có
	if input.NewPassword != "" {
		if err := bcrypt.CompareHashAndPassword([]byte(user.Password), []byte(input.OldPassword)); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Mật khẩu cũ không đúng"})
			return
		}
		hashed, _ := bcrypt.GenerateFromPassword([]byte(input.NewPassword), bcrypt.DefaultCost)
		updates["password"] = string(hashed)
	}

	config.DB.Model(&user).Updates(updates)

	// Frontend muốn { success, message }
	c.JSON(http.StatusOK, gin.H{"success": true, "message": "Cập nhật thông tin thành công!"})
}

// Logout — đăng xuất (frontend cần, backend JWT stateless nên chỉ trả thành công)
func Logout(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "Đăng xuất thành công!",
	})
}
