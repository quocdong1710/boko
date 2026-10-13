package controllers

import (
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"

	"boko/config"
	"boko/models"
)

func TestUserPublicPayloadIncludesDecryptedPhone(t *testing.T) {
	t.Setenv("APP_AES_KEY", "0123456789abcdef")

	plainPhone := "0901234567"
	cipherText, err := config.EncryptString(plainPhone)
	if err != nil {
		t.Fatalf("encrypt phone: %v", err)
	}

	user := models.User{
		ID:    1,
		Email: "demo@example.com",
		Name:  "Demo User",
		Phone: cipherText,
		Role:  "customer",
	}

	payload := userPublicPayload(user)
	phone, ok := payload["phone"].(string)
	if !ok {
		t.Fatalf("expected phone field to be present and string, got %#v", payload["phone"])
	}
	if phone != plainPhone {
		t.Fatalf("expected decrypted phone %q, got %q", plainPhone, phone)
	}
}

func TestLoginDummyHashInitializedAndValidCost(t *testing.T) {
	hash, err := loginDummyHash()
	if err != nil {
		t.Fatalf("expected nil error from loginDummyHash, got %v", err)
	}
	if len(hash) == 0 {
		t.Fatal("expected non-empty dummy bcrypt hash")
	}
	cost, err := bcrypt.Cost(hash)
	if err != nil {
		t.Fatalf("invalid bcrypt hash: %v", err)
	}
	if cost != bcrypt.DefaultCost {
		t.Fatalf("expected cost %d, got %d", bcrypt.DefaultCost, cost)
	}

	start := time.Now()
	_ = bcrypt.CompareHashAndPassword(hash, []byte("wrong-password-probe"))
	elapsed := time.Since(start)
	if elapsed < 5*time.Millisecond {
		t.Fatalf("expected bcrypt compare to take measurable work-factor time, took %v", elapsed)
	}
}

