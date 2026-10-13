package middleware

import (
	"errors"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
)

// JwtSecret — khoá bí mật dùng để ký và xác thực JWT (được nạp từ main.go qua biến môi trường CSPRNG)
var JwtSecret []byte

// AuthOptional — middleware tuỳ chọn JWT token (giống PR VNPay của nhóm).
// Nếu token hợp lệ thì gắn user vào context; nếu không có hoặc không hợp lệ
// thì vẫn tiếp tục cho khách thực hiện
func AuthOptional(c *gin.Context) {
	authHeader := c.GetHeader("Authorization")
	if authHeader == "" || !strings.HasPrefix(authHeader, "Bearer ") {
		c.Next()
		return
	}

	tokenString := strings.TrimPrefix(authHeader, "Bearer ")
	token, err := jwt.Parse(tokenString, func(token *jwt.Token) (interface{}, error) {
		if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, errors.New("sai phương thức ký")
		}
		return JwtSecret, nil
	})

	if err == nil && token.Valid {
		if claims, ok := token.Claims.(jwt.MapClaims); ok {
			if uid, ok := claims["user_id"].(float64); ok && uid > 0 {
				c.Set("user_id", uint(uid))
			}
			if email, ok := claims["email"].(string); ok {
				c.Set("email", email)
			}
			if role, ok := claims["role"].(string); ok {
				c.Set("role", role)
			}
		}
	}

	c.Next()
}

// AuthRequired — middleware kiểm tra JWT token từ header Authorization
func AuthRequired(c *gin.Context) {
	authHeader := c.GetHeader("Authorization")
	if authHeader == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Thiếu token xác thực"})
		c.Abort()
		return
	}

	if !strings.HasPrefix(authHeader, "Bearer ") {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Sai định dạng token (cần Bearer token)"})
		c.Abort()
		return
	}

	tokenString := strings.TrimPrefix(authHeader, "Bearer ")

	token, err := jwt.Parse(tokenString, func(token *jwt.Token) (interface{}, error) {
		if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, errors.New("sai phương thức ký")
		}
		return JwtSecret, nil
	})

	if err != nil || !token.Valid {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Token không hợp lệ hoặc đã hết hạn"})
		c.Abort()
		return
	}

	claims, ok := token.Claims.(jwt.MapClaims)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Không thể đọc token"})
		c.Abort()
		return
	}

	uidFloat, okUID := claims["user_id"].(float64)
	if !okUID || uidFloat <= 0 {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Token người dùng không hợp lệ"})
		c.Abort()
		return
	}
	email, _ := claims["email"].(string)
	role, _ := claims["role"].(string)

	// Gắn thông tin user vào context để handler dùng
	c.Set("user_id", uint(uidFloat))
	c.Set("email", email)
	c.Set("role", role)

	c.Next()
}

// SellerRequired — middleware chỉ cho phép seller (dùng sau AuthRequired)
func SellerRequired(c *gin.Context) {
	role, _ := c.Get("role")
	if role != "seller" && role != "admin" {
		c.JSON(http.StatusForbidden, gin.H{"error": "Chỉ seller mới có quyền thực hiện"})
		c.Abort()
		return
	}
	c.Next()
}

// AdminRequired — middleware chỉ cho phép admin (dùng sau AuthRequired)
func AdminRequired(c *gin.Context) {
	role, _ := c.Get("role")
	if role != "admin" {
		c.JSON(http.StatusForbidden, gin.H{"error": "Chỉ admin mới có quyền thực hiện"})
		c.Abort()
		return
	}
	c.Next()
}
